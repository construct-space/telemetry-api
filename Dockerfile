FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o telemetry .

# GeoIP DB stage — pulls DB-IP Lite Country at build time. CC0-licensed,
# refreshed monthly, no signup or license key required. Same MMDB format
# as MaxMind so the maxminddb-golang reader handles it transparently.
#
# DB-IP publishes monthly snapshots at a predictable URL keyed by year-month.
# The build arg lets ops pin a known-good month; default tracks the snapshot
# we last verified. If the download fails (network blip, retired month) we
# log and continue — geoip degrades to a no-op, ingest still works.
FROM alpine:3.21 AS geoip
ARG DBIP_MONTH="2026-04"
RUN apk add --no-cache curl gzip
WORKDIR /geoip
RUN curl -sSLf -o dbip.mmdb.gz "https://download.db-ip.com/free/dbip-country-lite-${DBIP_MONTH}.mmdb.gz" \
      && gunzip dbip.mmdb.gz \
      && mv dbip.mmdb GeoLite2-Country.mmdb \
      || (echo "[geoip] DB-IP download failed for ${DBIP_MONTH}, shipping without DB" && rm -f dbip.mmdb.gz && touch /geoip/.no-db)

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /app/telemetry .
COPY --from=geoip /geoip/* /app/
ENV PORT=80
ENV GEOIP_DB_PATH=/app/GeoLite2-Country.mmdb
EXPOSE 80
CMD ["./telemetry"]
