package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"construct/telemetry/internal/config"
)

func TestAccountsAuthMeURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		base string
		want string
	}{
		{
			name: "empty defaults to gateway",
			base: "",
			want: "https://my.lisaos.dev/api/accounts/auth/me",
		},
		{
			name: "legacy public accounts host rewrites to gateway",
			base: "https://accounts.lisaos.dev",
			want: "https://my.lisaos.dev/api/accounts/auth/me",
		},
		{
			name: "legacy public accounts host with trailing slash rewrites to gateway",
			base: "https://accounts.lisaos.dev/",
			want: "https://my.lisaos.dev/api/accounts/auth/me",
		},
		{
			name: "gateway root gets accounts prefix",
			base: "https://my.lisaos.dev",
			want: "https://my.lisaos.dev/api/accounts/auth/me",
		},
		{
			name: "gateway api root gets accounts prefix",
			base: "https://my.lisaos.dev/api",
			want: "https://my.lisaos.dev/api/accounts/auth/me",
		},
		{
			name: "gateway accounts root appends auth path",
			base: "https://my.lisaos.dev/api/accounts",
			want: "https://my.lisaos.dev/api/accounts/auth/me",
		},
		{
			name: "internal service root stays service-relative",
			base: "http://srv-captain--accounts",
			want: "http://srv-captain--accounts/api/auth/me",
		},
		{
			name: "custom service base path stays service-relative",
			base: "http://localhost:8000/accounts",
			want: "http://localhost:8000/accounts/api/auth/me",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := accountsAuthMeURL(tt.base)
			if err != nil {
				t.Fatalf("accountsAuthMeURL(%q) returned error: %v", tt.base, err)
			}
			if got != tt.want {
				t.Fatalf("accountsAuthMeURL(%q) = %q, want %q", tt.base, got, tt.want)
			}
		})
	}
}

func TestGatewayUserID(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{InternalSharedSecret: "shared-secret"}

	t.Run("accepts signed numeric row id", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/api/device", nil)
		req.Header.Set("X-Internal-Secret", "shared-secret")
		req.Header.Set("X-Auth-User-Row-ID", "42")

		got, ok := gatewayUserID(req, cfg)
		if !ok {
			t.Fatalf("expected gatewayUserID to accept signed row id")
		}
		if got != 42 {
			t.Fatalf("gatewayUserID returned %d, want 42", got)
		}
	})

	t.Run("rejects missing row id", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/api/device", nil)
		req.Header.Set("X-Internal-Secret", "shared-secret")

		if got, ok := gatewayUserID(req, cfg); ok || got != 0 {
			t.Fatalf("gatewayUserID should reject missing row id, got (%d, %v)", got, ok)
		}
	})

	t.Run("rejects wrong secret", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/api/device", nil)
		req.Header.Set("X-Internal-Secret", "wrong-secret")
		req.Header.Set("X-Auth-User-Row-ID", "42")

		if got, ok := gatewayUserID(req, cfg); ok || got != 0 {
			t.Fatalf("gatewayUserID should reject wrong secret, got (%d, %v)", got, ok)
		}
	})

	t.Run("rejects non numeric row id", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/api/device", nil)
		req.Header.Set("X-Internal-Secret", "shared-secret")
		req.Header.Set("X-Auth-User-Row-ID", "user-uuid")

		if got, ok := gatewayUserID(req, cfg); ok || got != 0 {
			t.Fatalf("gatewayUserID should reject non numeric row id, got (%d, %v)", got, ok)
		}
	})
}
