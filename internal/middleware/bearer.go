package middleware

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"construct/telemetry/internal/config"
)

// BearerAuth validates Authorization: Bearer cat_... by calling
// accounts /api/auth/me. On success we attach the numeric user_id to the
// request context under UserIDKey and pass through; on failure we 401.
//
// Accounts is the source of truth for token validity; we don't duplicate
// JWT parsing here. Results are cached for 5 minutes so a chatty client
// doesn't hammer accounts once per batch.

type ctxKey int

const UserIDKey ctxKey = iota

type tokenCacheEntry struct {
	userID    uint
	expiresAt time.Time
}

var (
	tokenCache   = map[string]tokenCacheEntry{}
	tokenCacheMu sync.RWMutex
)

const tokenCacheTTL = 5 * time.Minute

func BearerAuth(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if userID, ok := gatewayUserID(r, cfg); ok {
				ctx := context.WithValue(r.Context(), UserIDKey, userID)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, `{"error":"missing bearer token"}`, http.StatusUnauthorized)
				return
			}
			token := strings.TrimPrefix(auth, "Bearer ")
			if token == "" {
				http.Error(w, `{"error":"empty bearer token"}`, http.StatusUnauthorized)
				return
			}

			userID, err := resolveUserID(cfg, token)
			if err != nil {
				http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), UserIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func gatewayUserID(r *http.Request, cfg *config.Config) (uint, bool) {
	if cfg == nil || cfg.InternalSharedSecret == "" {
		return 0, false
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Secret")), []byte(cfg.InternalSharedSecret)) != 1 {
		return 0, false
	}

	raw := strings.TrimSpace(r.Header.Get("X-Auth-User-Row-ID"))
	if raw == "" {
		return 0, false
	}

	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return uint(n), true
}

func resolveUserID(cfg *config.Config, token string) (uint, error) {
	// Cache hit?
	tokenCacheMu.RLock()
	e, ok := tokenCache[token]
	tokenCacheMu.RUnlock()
	if ok && time.Now().Before(e.expiresAt) {
		return e.userID, nil
	}

	meURL, err := accountsAuthMeURL(cfg.AccountsURL)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequest("GET", meURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[telemetry] accounts /auth/me failed: %v", err)
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, errInvalidToken
	}

	var body struct {
		Authenticated bool `json:"authenticated"`
		User          struct {
			ID uint `json:"id"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	if !body.Authenticated || body.User.ID == 0 {
		return 0, errInvalidToken
	}

	tokenCacheMu.Lock()
	tokenCache[token] = tokenCacheEntry{userID: body.User.ID, expiresAt: time.Now().Add(tokenCacheTTL)}
	tokenCacheMu.Unlock()

	return body.User.ID, nil
}

func accountsAuthMeURL(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "https://my.lisaos.dev"
	}

	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Host == "accounts.lisaos.dev" {
		u.Scheme = "https"
		u.Host = "my.lisaos.dev"
	}

	path := strings.TrimRight(u.Path, "/")
	switch {
	case path == "":
		if u.Host == "my.lisaos.dev" {
			u.Path = "/api/accounts/auth/me"
		} else {
			u.Path = "/api/auth/me"
		}
	case path == "/api":
		u.Path = "/api/accounts/auth/me"
	case path == "/api/accounts":
		u.Path = "/api/accounts/auth/me"
	default:
		u.Path = path + "/api/auth/me"
	}

	return u.String(), nil
}

// GetUserID pulls the authenticated user id out of the request context.
// Panics if BearerAuth wasn't in the chain — use only inside handlers
// registered under a BearerAuth-wrapped route.
func GetUserID(r *http.Request) uint {
	v, _ := r.Context().Value(UserIDKey).(uint)
	return v
}

type authError struct{ msg string }

func (e *authError) Error() string { return e.msg }

var (
	errInvalidToken = &authError{"invalid token"}
)
