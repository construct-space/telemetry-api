package main

import (
	"log"
	"net/http"
	"time"

	"construct/telemetry/internal/config"
	"construct/telemetry/internal/database"
	"construct/telemetry/internal/geoip"
	"construct/telemetry/internal/handlers"
	"construct/telemetry/internal/middleware"
	"construct/telemetry/internal/models"
)

func main() {
	cfg := config.Load()
	handlers.Cfg = cfg

	geoip.Init(cfg.GeoIPDBPath)
	database.Init(cfg)
	if err := database.DB.AutoMigrate(
		&models.UserDevice{},
		&models.DailyUsage{},
		&models.DailySpaceUsage{},
		&models.DailyModelUsage{},
		&models.DailyPerf{},
		&models.DailyError{},
		&models.DailyToolUsage{},
		&models.ErrorEvent{},
		&models.IngestLog{},
	); err != nil {
		log.Fatalf("auto-migrate: %v", err)
	}

	mux := http.NewServeMux()

	// Ingest — app → telemetry, Bearer cat_* validated via accounts.
	ingestAuth := middleware.BearerAuth(cfg)
	mux.Handle("POST /api/events", ingestAuth(http.HandlerFunc(handlers.IngestEvents)))
	mux.Handle("POST /api/errors", ingestAuth(http.HandlerFunc(handlers.IngestErrorEvent)))
	mux.Handle("PUT /api/device", ingestAuth(http.HandlerFunc(handlers.UpsertDevice)))

	// Admin — oracle → telemetry, X-Internal-Secret gated.
	adminAuth := middleware.AdminAuth
	mux.Handle("GET /api/admin/usage", adminAuth(http.HandlerFunc(handlers.AdminListUsage)))
	mux.Handle("GET /api/admin/devices", adminAuth(http.HandlerFunc(handlers.AdminListDevices)))
	mux.Handle("GET /api/admin/space-usage", adminAuth(http.HandlerFunc(handlers.AdminListSpaceUsage)))
	mux.Handle("GET /api/admin/model-usage", adminAuth(http.HandlerFunc(handlers.AdminListModelUsage)))
	mux.Handle("GET /api/admin/perf", adminAuth(http.HandlerFunc(handlers.AdminListPerf)))
	mux.Handle("GET /api/admin/errors", adminAuth(http.HandlerFunc(handlers.AdminListErrors)))
	mux.Handle("GET /api/admin/error-events", adminAuth(http.HandlerFunc(handlers.AdminListErrorEvents)))
	mux.Handle("GET /api/admin/error-events/groups", adminAuth(http.HandlerFunc(handlers.AdminListErrorEventGroups)))
	mux.Handle("GET /api/admin/tools", adminAuth(http.HandlerFunc(handlers.AdminListTools)))
	mux.Handle("GET /api/admin/summary", adminAuth(http.HandlerFunc(handlers.AdminSummary)))
	mux.Handle("GET /api/admin/top/users", adminAuth(http.HandlerFunc(handlers.AdminTopUsers)))
	mux.Handle("GET /api/admin/top/models", adminAuth(http.HandlerFunc(handlers.AdminTopModels)))
	mux.Handle("GET /api/admin/top/spaces", adminAuth(http.HandlerFunc(handlers.AdminTopSpaces)))
	mux.Handle("GET /api/admin/trends", adminAuth(http.HandlerFunc(handlers.AdminTrends)))
	mux.Handle("GET /api/admin/geo", adminAuth(http.HandlerFunc(handlers.AdminGeo)))
	mux.Handle("GET /api/admin/geo/users", adminAuth(http.HandlerFunc(handlers.AdminGeoUsers)))

	// Health — unauthenticated.
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		handlers.WriteJSON(w, 200, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		handlers.WriteJSON(w, 200, map[string]any{"service": "telemetry-api", "status": "ok"})
	})

	handler := corsMiddleware(cfg, mux)

	log.Printf("[telemetry] listening on :%s", cfg.Port)
	log.Printf("[telemetry] cors allowed origins: %v", cfg.AllowedOrigins)
	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

func corsMiddleware(cfg *config.Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		for _, o := range cfg.AllowedOrigins {
			if o == origin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				break
			}
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Internal-Secret")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
