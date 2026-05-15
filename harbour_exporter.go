package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/net/publicsuffix"
)

var errNotAuthenticated = errors.New("not authenticated")

const (
	harbourBaseURL = "https://harbour.jolla.com"
	accountBaseURL = "https://account.jolla.com"
	oauthNextParam = "/oauth2/auth/authorize/?response_type=code&redirect_uri=https%3A%2F%2Fharbour.jolla.com%2Fauth%2Fjolla%2Fcallback&client_id=9a84369b310ba77cbdd8ece9337423"
	userAgent      = "harbour-exporter/1.0"
)

type StatsResponse struct {
	Total TotalStats  `json:"total"`
	Items []ItemStats `json:"items"`
}

type TotalStats struct {
	TotalInstalls  int `json:"total_installs"`
	ActiveInstalls int `json:"active_installs"`
	Likes          int `json:"likes"`
	Reviews        int `json:"reviews"`
}

type ItemStats struct {
	ActiveInstalls int    `json:"active_installs"`
	TotalInstalls  int    `json:"total_installs"`
	Likes          int    `json:"likes"`
	Reviews        int    `json:"reviews"`
	ItemID         string `json:"item_id"`
}

type ItemInfo struct {
	Title   string
	QAState string
}

type Item struct {
	ID      string `json:"_id"`
	Title   string `json:"title"`
	QAState string `json:"qa_state"`
}

type HarbourClient struct {
	httpClient *http.Client
	username   string
	password   string
	mu         sync.Mutex
	lastAuth   time.Time
}

func NewHarbourClient(username, password string) (*HarbourClient, error) {
	jar, err := cookiejar.New(&cookiejar.Options{
		PublicSuffixList: publicsuffix.List,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	return &HarbourClient{
		httpClient: &http.Client{
			Jar:     jar,
			Timeout: 10 * time.Second,
		},
		username: username,
		password: password,
	}, nil
}

func (c *HarbourClient) hasValidSession() bool {
	harbourURL, _ := url.Parse(harbourBaseURL)
	for _, ck := range c.httpClient.Jar.Cookies(harbourURL) {
		if ck.Name == "jolla-harbour" {
			return true
		}
	}
	return false
}

func (c *HarbourClient) authenticate() error {
	if c.hasValidSession() {
		return nil
	}

	req, err := http.NewRequest("GET", harbourBaseURL+"/auth/jolla", nil)
	if err != nil {
		return fmt.Errorf("failed to create auth request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("auth GET failed: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("failed to read auth response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth returned status %d: %s", resp.StatusCode, string(body))
	}

	csrfToken, err := extractCSRFToken(string(body))
	if err != nil {
		return fmt.Errorf("failed to extract CSRF token: %w", err)
	}

	loginURL := resp.Request.URL.String()

	form := url.Values{}
	form.Set("csrfmiddlewaretoken", csrfToken)
	form.Set("username", c.username)
	form.Set("password", c.password)
	form.Set("next", oauthNextParam)

	postReq, err := http.NewRequest("POST", loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create login request: %w", err)
	}
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.Header.Set("User-Agent", userAgent)
	postReq.Header.Set("Referer", loginURL)
	postReq.Header.Set("Origin", accountBaseURL)

	resp, err = c.httpClient.Do(postReq)
	if err != nil {
		return fmt.Errorf("login POST failed: %w", err)
	}
	resp.Body.Close()

	if !c.hasValidSession() {
		return fmt.Errorf("authentication failed: jolla-harbour cookie not found")
	}

	c.lastAuth = time.Now()
	return nil
}

func extractCSRFToken(html string) (string, error) {
	re := regexp.MustCompile(`name=['"]csrfmiddlewaretoken['"]\s+value=['"]([^'"]+)['"]`)
	matches := re.FindStringSubmatch(html)
	if len(matches) < 2 {
		return "", fmt.Errorf("csrfmiddlewaretoken not found in HTML")
	}
	return matches[1], nil
}

func (c *HarbourClient) doAPIRequest(urlStr string) (*http.Response, error) {
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", harbourBaseURL+"/dashboard")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, errNotAuthenticated
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/json") {
		resp.Body.Close()
		return nil, errNotAuthenticated
	}

	return resp, nil
}

func (c *HarbourClient) doAuthenticatedAPIRequest(path string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if time.Since(c.lastAuth) > 30*time.Minute {
		if err := c.authenticate(); err != nil {
			return nil, fmt.Errorf("re-auth failed: %w", err)
		}
	}

	resp, err := c.doAPIRequest(harbourBaseURL + path)
	if err != nil && errors.Is(err, errNotAuthenticated) {
		if authErr := c.authenticate(); authErr != nil {
			return nil, fmt.Errorf("re-auth attempt failed: %w", authErr)
		}
		resp, err = c.doAPIRequest(harbourBaseURL + path)
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

func (c *HarbourClient) FetchStats() (*StatsResponse, error) {
	body, err := c.doAuthenticatedAPIRequest("/api/stats/")
	if err != nil {
		return nil, err
	}

	var stats StatsResponse
	if err := json.Unmarshal(body, &stats); err != nil {
		return nil, fmt.Errorf("failed to parse stats: %w", err)
	}

	return &stats, nil
}

func (c *HarbourClient) FetchItemInfo() (map[string]ItemInfo, error) {
	body, err := c.doAuthenticatedAPIRequest("/api/items/")
	if err != nil {
		return nil, err
	}

	var items []Item
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("failed to parse items: %w", err)
	}

	infoMap := make(map[string]ItemInfo, len(items))
	for _, item := range items {
		infoMap[item.ID] = ItemInfo{
			Title:   item.Title,
			QAState: item.QAState,
		}
	}

	return infoMap, nil
}

type HarbourCollector struct {
	client *HarbourClient
	mu     sync.Mutex

	totalInstalls  *prometheus.Desc
	activeInstalls *prometheus.Desc
	totalLikes     *prometheus.Desc
	totalReviews   *prometheus.Desc

	appDownloads      *prometheus.Desc
	appActiveInstalls *prometheus.Desc
	appLikes          *prometheus.Desc
	appReviews        *prometheus.Desc
	appQAState        *prometheus.Desc

	up             *prometheus.Desc
	scrapeDuration *prometheus.Desc
}

func NewHarbourCollector(client *HarbourClient) *HarbourCollector {
	return &HarbourCollector{
		client: client,

		totalInstalls: prometheus.NewDesc(
			"harbour_total_installs",
			"Total number of app installations across all apps",
			nil, nil,
		),
		activeInstalls: prometheus.NewDesc(
			"harbour_active_installs",
			"Number of currently active app installations across all apps",
			nil, nil,
		),
		totalLikes: prometheus.NewDesc(
			"harbour_total_likes",
			"Total number of likes across all apps",
			nil, nil,
		),
		totalReviews: prometheus.NewDesc(
			"harbour_total_reviews",
			"Total number of reviews across all apps",
			nil, nil,
		),
		appDownloads: prometheus.NewDesc(
			"harbour_app_downloads",
			"Number of downloads per application",
			[]string{"app_id", "app_name"}, nil,
		),
		appActiveInstalls: prometheus.NewDesc(
			"harbour_app_active_installs",
			"Number of active installs per application",
			[]string{"app_id", "app_name"}, nil,
		),
		appLikes: prometheus.NewDesc(
			"harbour_app_likes",
			"Number of likes per application",
			[]string{"app_id", "app_name"}, nil,
		),
		appReviews: prometheus.NewDesc(
			"harbour_app_reviews",
			"Number of reviews per application",
			[]string{"app_id", "app_name"}, nil,
		),
		appQAState: prometheus.NewDesc(
			"harbour_app_qa_state",
			"QA state of the application (1 = current state)",
			[]string{"app_id", "app_name", "qa_state"}, nil,
		),
		up: prometheus.NewDesc(
			"harbour_up",
			"Whether the Harbour API was reachable (1) or not (0)",
			nil, nil,
		),
		scrapeDuration: prometheus.NewDesc(
			"harbour_scrape_duration_seconds",
			"Duration of the last Harbour API scrape",
			nil, nil,
		),
	}
}

func (c *HarbourCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.totalInstalls
	ch <- c.activeInstalls
	ch <- c.totalLikes
	ch <- c.totalReviews
	ch <- c.appDownloads
	ch <- c.appActiveInstalls
	ch <- c.appLikes
	ch <- c.appReviews
	ch <- c.appQAState
	ch <- c.up
	ch <- c.scrapeDuration
}

func (c *HarbourCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	start := time.Now()

	stats, err := c.client.FetchStats()
	if err != nil {
		log.Printf("ERROR: failed to fetch stats: %v", err)
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0)
		ch <- prometheus.MustNewConstMetric(c.scrapeDuration, prometheus.GaugeValue, time.Since(start).Seconds())
		return
	}

	itemInfo, err := c.client.FetchItemInfo()
	if err != nil {
		log.Printf("WARN: failed to fetch item info: %v", err)
		itemInfo = make(map[string]ItemInfo)
	}

	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(c.scrapeDuration, prometheus.GaugeValue, time.Since(start).Seconds())

	ch <- prometheus.MustNewConstMetric(c.totalInstalls, prometheus.GaugeValue, float64(stats.Total.TotalInstalls))
	ch <- prometheus.MustNewConstMetric(c.activeInstalls, prometheus.GaugeValue, float64(stats.Total.ActiveInstalls))
	ch <- prometheus.MustNewConstMetric(c.totalLikes, prometheus.GaugeValue, float64(stats.Total.Likes))
	ch <- prometheus.MustNewConstMetric(c.totalReviews, prometheus.GaugeValue, float64(stats.Total.Reviews))

	for _, item := range stats.Items {
		info := itemInfo[item.ItemID]
		appName := info.Title
		if appName == "" {
			appName = item.ItemID
		}

		ch <- prometheus.MustNewConstMetric(c.appDownloads, prometheus.GaugeValue, float64(item.TotalInstalls), item.ItemID, appName)
		ch <- prometheus.MustNewConstMetric(c.appActiveInstalls, prometheus.GaugeValue, float64(item.ActiveInstalls), item.ItemID, appName)
		ch <- prometheus.MustNewConstMetric(c.appLikes, prometheus.GaugeValue, float64(item.Likes), item.ItemID, appName)
		ch <- prometheus.MustNewConstMetric(c.appReviews, prometheus.GaugeValue, float64(item.Reviews), item.ItemID, appName)

		if info.QAState != "" {
			ch <- prometheus.MustNewConstMetric(c.appQAState, prometheus.GaugeValue, 1, item.ItemID, appName, info.QAState)
		}
	}
}

func main() {
	listenAddr := flag.String("web.listen-address", ":9101", "Address to listen on for HTTP requests")
	username := flag.String("username", "", "Jolla account username")
	password := flag.String("password", "", "Jolla account password")
	flag.Parse()

	if *username == "" {
		*username = os.Getenv("HARBOUR_USERNAME")
	}
	if *password == "" {
		*password = os.Getenv("HARBOUR_PASSWORD")
	}

	if *username == "" || *password == "" {
		log.Fatal("Username and password are required. Set via -username/-password flags or HARBOUR_USERNAME/HARBOUR_PASSWORD env vars.")
	}

	client, err := NewHarbourClient(*username, *password)
	if err != nil {
		log.Fatalf("Failed to create Harbour client: %v", err)
	}

	if err := client.authenticate(); err != nil {
		log.Fatalf("Initial authentication failed: %v", err)
	}
	log.Println("Successfully authenticated with Harbour")

	collector := NewHarbourCollector(client)
	prometheus.MustRegister(collector)

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html>
<head><title>Harbour Exporter</title></head>
<body>
<h1>Harbour Exporter</h1>
<p><a href="/metrics">Metrics</a></p>
</body>
</html>`)
	})

	log.Printf("Starting Harbour exporter on %s", *listenAddr)
	log.Fatal(http.ListenAndServe(*listenAddr, nil))
}
