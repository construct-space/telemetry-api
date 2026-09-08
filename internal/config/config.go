package config

import (
	"bufio"
	"os"
	"strings"
)

func init() {
	loadEnvFile(".env")
}

func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, "="); idx > 0 {
			k := strings.TrimSpace(line[:idx])
			v := strings.TrimSpace(line[idx+1:])
			if os.Getenv(k) == "" {
				_ = os.Setenv(k, v)
			}
		}
	}
}

type Config struct {
	Port     string
	DBDriver string
	DBHost   string
	DBPort   string
	DBUser   string
	DBPass   string
	DBName   string
	DBSSL    string

	// URL of accounts-api, used to validate Bearer cat_* tokens on ingest.
	// Calls GET /api/auth/me with the token and caches the returned user_id
	// for the token's lifetime. Defaults to the CapRover internal hostname.
	AccountsURL string

	// X-Internal-Secret shared across the mesh — gates /api/admin/* so
	// only oracle (and other trusted services) can read aggregated data.
	InternalSharedSecret string

	// Path to the GeoLite2-Country.mmdb file inside the container. Empty
	// or missing-on-disk → geoip lookup is disabled and country_code is
	// left blank on new rows. Refresh out-of-band via a sidecar/cron job.
	GeoIPDBPath string

	// CORS
	AllowedOrigins []string
}

func env(k, fb string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fb
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func Load() *Config {
	return &Config{
		// Matches the rest of the fleet (accounts/developer/source/domains/
		// delivery/billing) so telemetry is one DB namespace on the same
		// MySQL instance — simpler ops, same backup pipeline. Migration to
		// Postgres/Timescale is a follow-up once write volume earns it.
		Port:                 env("PORT", "4300"),
		DBDriver:             env("DB_DRIVER", "mysql"),
		DBHost:               env("DB_HOST", "localhost"),
		DBPort:               env("DB_PORT", "3306"),
		DBUser:               env("DB_USER", "root"),
		DBPass:               env("DB_PASS", ""),
		DBName:               env("DB_NAME", "construct_telemetry"),
		DBSSL:                env("DB_SSL", "false"),
		AccountsURL:          env("ACCOUNTS_URL", "http://srv-captain--accounts"),
		InternalSharedSecret: env("INTERNAL_SHARED_SECRET", ""),
		GeoIPDBPath:          env("GEOIP_DB_PATH", "/app/GeoLite2-Country.mmdb"),
		AllowedOrigins:       splitCSV(env("ALLOWED_ORIGINS", "")),
	}
}
