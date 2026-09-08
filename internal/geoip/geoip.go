// Package geoip wraps a MaxMind GeoLite2-Country database lookup behind a
// process-global Reader. The reader is opened once at boot and shared across
// goroutines — maxminddb readers are explicitly safe for concurrent use.
//
// If the DB file is missing or fails to open we log once and degrade to a
// no-op lookup that returns "" for every IP. This keeps telemetry ingest
// working in dev (where nobody bothers with a GeoIP DB) and protects prod
// against a missing-volume foot-gun.
package geoip

import (
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/oschwald/maxminddb-golang"
)

var (
	mu     sync.RWMutex
	reader *maxminddb.Reader
)

// Init opens the GeoLite2-Country database at path. Safe to call with an
// empty path or a path that doesn't exist — both leave Lookup returning "".
func Init(path string) {
	if path == "" {
		log.Printf("[geoip] disabled (no GEOIP_DB_PATH)")
		return
	}
	r, err := maxminddb.Open(path)
	if err != nil {
		log.Printf("[geoip] disabled (open %s: %v)", path, err)
		return
	}
	mu.Lock()
	reader = r
	mu.Unlock()
	log.Printf("[geoip] loaded %s", path)
}

// Close releases the underlying file. Tests use it; main doesn't bother
// since process exit reclaims everything.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if reader != nil {
		_ = reader.Close()
		reader = nil
	}
}

// LookupCountry returns the ISO-3166 alpha-2 code for ip, or "" when the IP
// is private/unroutable, the DB is unloaded, or the range is unknown.
func LookupCountry(ip string) string {
	mu.RLock()
	r := reader
	mu.RUnlock()
	if r == nil || ip == "" {
		return ""
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	// MaxMind's free DB doesn't carry private/loopback ranges, and asking
	// returns nothing useful. Skip explicitly so we don't pay the lookup
	// cost on every dev request from 127.0.0.1.
	if parsed.IsLoopback() || parsed.IsPrivate() || parsed.IsUnspecified() {
		return ""
	}
	var rec struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	if err := r.Lookup(parsed, &rec); err != nil {
		return ""
	}
	return rec.Country.ISOCode
}

// ClientIP extracts the originating client IP from an inbound request,
// preferring the gateway-set forwarding headers over RemoteAddr.
//
// X-Forwarded-For can be a comma-separated chain "client, proxy1, proxy2"
// — we want the first hop, which is the original client. The gateway
// strips any client-supplied XFF before adding its own, so this is safe
// to trust for our deployment.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if comma := strings.IndexByte(xff, ','); comma >= 0 {
			xff = xff[:comma]
		}
		return strings.TrimSpace(xff)
	}
	if real := r.Header.Get("X-Real-IP"); real != "" {
		return strings.TrimSpace(real)
	}
	// RemoteAddr is "host:port" — strip the port.
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
