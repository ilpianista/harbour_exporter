ARG ARCH="amd64"
ARG OS="linux"
FROM quay.io/prometheus/busybox-${OS}-${ARCH}:latest
LABEL maintainer="Andrea Scarpino <andrea@scarpino.dev>"

ARG ARCH="amd64"
ARG OS="linux"
COPY .build/${OS}-${ARCH}/harbour_exporter /bin/harbour_exporter

EXPOSE      9101
USER        nobody
ENTRYPOINT  [ "/bin/harbour_exporter" ]
