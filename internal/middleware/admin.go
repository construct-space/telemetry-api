package middleware

import (
	"crypto/subtle"
	"net/http"
	"os"
)

// AdminAuth gates /api/admin/* routes — only callers with the matching
// X-Internal-Secret header get through. Consumed by oracle-api, which
// proxies the admin surface into the oracle UI.
func AdminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret := os.Getenv("INTERNAL_SHARED_SECRET")
		if secret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Secret")), []byte(secret)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
