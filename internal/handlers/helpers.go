package handlers

import (
	"encoding/json"
	"net/http"

	"construct/telemetry/internal/config"
)

// Cfg is the package-level config handle, set from main.go at boot.
var Cfg *config.Config

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func parseBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func atoi(q string, def, max int) int {
	if q == "" {
		return def
	}
	n := 0
	for _, c := range q {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
		if n > max {
			return max
		}
	}
	if n < 1 {
		return def
	}
	return n
}
