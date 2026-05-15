# Harbour (Jolla Store) exporter

A [Harbour](https://harbour.jolla.com) exporter for [Prometheus](https://prometheus.io/).

## Getting Started

To run it:

```bash
./harbour_exporter [flags]
```

Help on flags:

```bash
./harbour_exporter -help
```

## Usage

**NOTE**: The exporter fetches information from Harbour on every scrape, therefore having a too short scrape interval can impose load on Jolla. Don't be evil!

Username and password are required either via flags or environment variables, e.g.:

```bash
export HARBOUR_PASSWORD=mypassword
./harbour_exporter -username myusername
```

### Docker

[![Docker Repository on Quay](https://quay.io/repository/ilpianista/harbour-exporter/status)][quay]

To run the haproxy exporter as a Docker container, run:

```bash
docker run -p 9101:9101 -e HARBOUR_PASSWORD=mypassword quay.io/ilpianista/harbour-exporter:latest -username=myusername
```

### Building

```bash
make build
```

## Donate

The SailfishOS Community Team is on Liberapay:

[![Liberapay receiving](https://img.shields.io/liberapay/receives/SailfishOScommunityTeam?logo=liberapay&label=SailfishOSCommunity)](https://liberapay.com/SailfishOScommunityTeam)

[![Liberapay receiving](https://img.shields.io/liberapay/receives/ilpianista?logo=liberapay&label=ilpianista)](https://liberapay.com/ilpianista)

## License

MIT

[quay]: https://quay.io/repository/ilpianista/harbour-exporter
