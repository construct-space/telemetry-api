package handlers

import (
	"net/http"
	"time"

	"construct/telemetry/internal/database"
	"construct/telemetry/internal/geoip"
	"construct/telemetry/internal/middleware"
	"construct/telemetry/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// POST /api/events
// Accepts a batch of pre-aggregated daily deltas from an app instance.
// Every counter is INCREMENTED into the matching row — so if the client
// retries a batch after a network blip, numbers double. The client must
// dedupe using its own cursor file; we don't hash+dedupe server-side
// because that'd mean storing every batch indefinitely.
//
// Keeping the server stateless on dedup is a deliberate trade: clients
// can lose a few hours of granularity after a crash, but the server
// stays simple (no "was this batch already applied" table, no retention
// logic on something that isn't the truth anyway).

type ingestUsageDelta struct {
	Sessions      int   `json:"sessions"`
	ActiveMinutes int   `json:"active_minutes"`
	ChatsSent     int   `json:"chats_sent"`
	ToolCalls     int   `json:"tool_calls"`
	TokensInput   int64 `json:"tokens_input"`
	TokensOutput  int64 `json:"tokens_output"`
	FileSaves     int   `json:"file_saves"`
	GitCommits    int   `json:"git_commits"`
	Errors        int   `json:"errors"`
}

type ingestSpaceDelta struct {
	SpaceID       string `json:"space_id"`
	EnterCount    int    `json:"enter_count"`
	ActiveMinutes int    `json:"active_minutes"`
}

type ingestModelDelta struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	RequestCount int     `json:"request_count"`
	TokensInput  int64   `json:"tokens_input"`
	TokensOutput int64   `json:"tokens_output"`
	CacheRead    int64   `json:"cache_read"`
	CacheWrite   int64   `json:"cache_write"`
	ToolCalls    int     `json:"tool_calls"`
	CostUSD      float64 `json:"cost_usd"`
}

type ingestPerfDelta struct {
	Metric  string `json:"metric"`
	Count   int    `json:"count"`
	TotalMs int64  `json:"total_ms"`
	MinMs   int    `json:"min_ms"`
	MaxMs   int    `json:"max_ms"`
}

type ingestErrorDelta struct {
	ErrorClass string `json:"error_class"`
	Count      int    `json:"count"`
}

type ingestToolDelta struct {
	ToolName     string `json:"tool_name"`
	Invocations  int    `json:"invocations"`
	SuccessCount int    `json:"success_count"`
	ErrorCount   int    `json:"error_count"`
	TotalMs      int64  `json:"total_ms"`
}

type ingestBatch struct {
	Date       string             `json:"date"` // YYYY-MM-DD, UTC
	AppVersion string             `json:"app_version,omitempty"`
	Usage      *ingestUsageDelta  `json:"usage,omitempty"`
	Spaces     []ingestSpaceDelta `json:"spaces,omitempty"`
	Models     []ingestModelDelta `json:"models,omitempty"`
	Perf       []ingestPerfDelta  `json:"perf,omitempty"`
	Errors     []ingestErrorDelta `json:"errors,omitempty"`
	Tools      []ingestToolDelta  `json:"tools,omitempty"`
}

func clampI64(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampInt(v, lo, hi int) int {
	return int(clampI64(int64(v), int64(lo), int64(hi)))
}

// clampModelDeltas bounds the client-supplied usage/cost deltas. cost_usd is
// the billing/analytics-sensitive one; token + request counts are bounded so
// a single batch can't inject implausible totals.
func clampModelDeltas(b *ingestBatch) {
	const maxTokens = 1_000_000_000_000 // 1e12
	const maxCount = 100_000_000
	for i := range b.Models {
		m := &b.Models[i]
		if m.CostUSD < 0 {
			m.CostUSD = 0
		} else if m.CostUSD > 1_000_000 {
			m.CostUSD = 1_000_000
		}
		m.TokensInput = clampI64(m.TokensInput, 0, maxTokens)
		m.TokensOutput = clampI64(m.TokensOutput, 0, maxTokens)
		m.CacheRead = clampI64(m.CacheRead, 0, maxTokens)
		m.CacheWrite = clampI64(m.CacheWrite, 0, maxTokens)
		m.RequestCount = clampInt(m.RequestCount, 0, maxCount)
		m.ToolCalls = clampInt(m.ToolCalls, 0, maxCount)
	}
}

func IngestEvents(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == 0 {
		WriteJSON(w, 401, map[string]string{"error": "unauthenticated"})
		return
	}

	var batch ingestBatch
	if err := parseBody(r, &batch); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid body: " + err.Error()})
		return
	}
	if batch.Date == "" {
		WriteJSON(w, 400, map[string]string{"error": "date is required (YYYY-MM-DD, UTC)"})
		return
	}
	if _, err := time.Parse("2006-01-02", batch.Date); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "date must be YYYY-MM-DD"})
		return
	}

	// SECURITY: these deltas are client-supplied. Clamp per-field maxima (and
	// floor at 0) so a hostile client can't poison aggregate usage/cost
	// analytics with absurd or negative values. Bounds are generous — a real
	// day's usage for one user never approaches them.
	clampModelDeltas(&batch)

	// Reject too-old dates to protect against clock-skew or replay from a
	// misbehaving client — our daily buckets don't benefit from 90-day-old
	// writes because the insight value has rotted.
	if parsed, _ := time.Parse("2006-01-02", batch.Date); time.Since(parsed) > 90*24*time.Hour {
		WriteJSON(w, 400, map[string]string{"error": "date too old (> 90 days)"})
		return
	}

	// Country gets stamped onto the daily-usage row so map-style queries
	// don't need to join user_devices. last-write-wins on the same date —
	// see DailyUsage.CountryCode godoc for why that's fine.
	country := geoip.LookupCountry(geoip.ClientIP(r))

	events := 0
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if batch.Usage != nil {
			if err := upsertUsage(tx, userID, batch.Date, *batch.Usage, country); err != nil {
				return err
			}
			events++
		}
		for _, s := range batch.Spaces {
			if s.SpaceID == "" {
				continue
			}
			if err := upsertSpace(tx, userID, batch.Date, s); err != nil {
				return err
			}
			events++
		}
		for _, m := range batch.Models {
			if m.Model == "" {
				continue
			}
			if m.Provider == "" {
				m.Provider = "unknown"
			}
			if err := upsertModel(tx, userID, batch.Date, m); err != nil {
				return err
			}
			events++
		}
		for _, p := range batch.Perf {
			if p.Metric == "" || p.Count <= 0 {
				continue
			}
			if err := upsertPerf(tx, userID, batch.Date, p); err != nil {
				return err
			}
			events++
		}
		for _, e := range batch.Errors {
			if e.ErrorClass == "" || e.Count <= 0 {
				continue
			}
			if err := upsertError(tx, userID, batch.Date, e); err != nil {
				return err
			}
			events++
		}
		for _, t := range batch.Tools {
			if t.ToolName == "" || t.Invocations <= 0 {
				continue
			}
			if err := upsertTool(tx, userID, batch.Date, t); err != nil {
				return err
			}
			events++
		}
		return nil
	}); err != nil {
		WriteJSON(w, 500, map[string]string{"error": "ingest failed: " + err.Error()})
		return
	}

	// Append an audit breadcrumb. Not in the transaction — a failed log
	// shouldn't roll back successfully ingested deltas.
	_ = database.DB.Create(&models.IngestLog{
		UserID:     userID,
		AppVersion: batch.AppVersion,
		EventCount: events,
		ReceivedAt: time.Now().UTC(),
	}).Error

	WriteJSON(w, 202, map[string]any{"accepted": events})
}

// ── UPSERT helpers ───────────────────────────────────────────────────────
// "x = x + ?" accumulator pattern. GORM's clause.OnConflict + Assignments
// translates to the right dialect per driver:
//   MySQL:    INSERT ... ON DUPLICATE KEY UPDATE col = col + ?
//   Postgres: INSERT ... ON CONFLICT (...) DO UPDATE SET col = col + ?
// Using the bare column name (no table prefix) is portable across both —
// in MySQL's UPDATE clause and in Postgres's DO UPDATE, an unqualified
// column on the RHS always resolves to the existing row, not the proposed
// insert. Postgres would also accept EXCLUDED.col for the incoming value
// but we don't need that: the delta is passed as a parameter.
// LEAST/GREATEST are supported by both MySQL 8+ and Postgres.

func upsertUsage(tx *gorm.DB, userID uint, date string, d ingestUsageDelta, country string) error {
	row := models.DailyUsage{
		UserID: userID, Date: date, CountryCode: country,
		Sessions: d.Sessions, ActiveMinutes: d.ActiveMinutes,
		ChatsSent: d.ChatsSent, ToolCalls: d.ToolCalls,
		TokensInput: d.TokensInput, TokensOutput: d.TokensOutput,
		FileSaves: d.FileSaves, GitCommits: d.GitCommits, Errors: d.Errors,
	}
	updates := map[string]any{
		"sessions":       gorm.Expr("sessions + ?", d.Sessions),
		"active_minutes": gorm.Expr("active_minutes + ?", d.ActiveMinutes),
		"chats_sent":     gorm.Expr("chats_sent + ?", d.ChatsSent),
		"tool_calls":     gorm.Expr("tool_calls + ?", d.ToolCalls),
		"tokens_input":   gorm.Expr("tokens_input + ?", d.TokensInput),
		"tokens_output":  gorm.Expr("tokens_output + ?", d.TokensOutput),
		"file_saves":     gorm.Expr("file_saves + ?", d.FileSaves),
		"git_commits":    gorm.Expr("git_commits + ?", d.GitCommits),
		"errors":         gorm.Expr("errors + ?", d.Errors),
	}
	// Don't clobber a previously-stored country with "" if today's lookup
	// failed (private IP, missing DB). Only overwrite when we have a value.
	if country != "" {
		updates["country_code"] = country
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "date"}},
		DoUpdates: clause.Assignments(updates),
	}).Create(&row).Error
}

func upsertSpace(tx *gorm.DB, userID uint, date string, d ingestSpaceDelta) error {
	row := models.DailySpaceUsage{
		UserID: userID, Date: date, SpaceID: d.SpaceID,
		EnterCount: d.EnterCount, ActiveMinutes: d.ActiveMinutes,
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "date"}, {Name: "space_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"enter_count":    gorm.Expr("enter_count + ?", d.EnterCount),
			"active_minutes": gorm.Expr("active_minutes + ?", d.ActiveMinutes),
		}),
	}).Create(&row).Error
}

func upsertModel(tx *gorm.DB, userID uint, date string, d ingestModelDelta) error {
	row := models.DailyModelUsage{
		UserID: userID, Date: date, Provider: d.Provider, Model: d.Model,
		RequestCount: d.RequestCount, TokensInput: d.TokensInput,
		TokensOutput: d.TokensOutput, CacheRead: d.CacheRead,
		CacheWrite: d.CacheWrite, ToolCalls: d.ToolCalls, CostUSD: d.CostUSD,
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "date"}, {Name: "provider"}, {Name: "model"}},
		DoUpdates: clause.Assignments(map[string]any{
			"request_count": gorm.Expr("request_count + ?", d.RequestCount),
			"tokens_input":  gorm.Expr("tokens_input + ?", d.TokensInput),
			"tokens_output": gorm.Expr("tokens_output + ?", d.TokensOutput),
			"cache_read":    gorm.Expr("cache_read + ?", d.CacheRead),
			"cache_write":   gorm.Expr("cache_write + ?", d.CacheWrite),
			"tool_calls":    gorm.Expr("tool_calls + ?", d.ToolCalls),
			"cost_usd":      gorm.Expr("cost_usd + ?", d.CostUSD),
		}),
	}).Create(&row).Error
}

func upsertPerf(tx *gorm.DB, userID uint, date string, d ingestPerfDelta) error {
	// min/max need different semantics than the counters — use
	// LEAST/GREATEST for a proper rolling bound. For new rows the seed
	// value is the incoming one.
	row := models.DailyPerf{
		UserID: userID, Date: date, Metric: d.Metric,
		Count: d.Count, TotalMs: d.TotalMs, MinMs: d.MinMs, MaxMs: d.MaxMs,
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "date"}, {Name: "metric"}},
		DoUpdates: clause.Assignments(map[string]any{
			"count":    gorm.Expr("count + ?", d.Count),
			"total_ms": gorm.Expr("total_ms + ?", d.TotalMs),
			"min_ms":   gorm.Expr("LEAST(min_ms, ?)", d.MinMs),
			"max_ms":   gorm.Expr("GREATEST(max_ms, ?)", d.MaxMs),
		}),
	}).Create(&row).Error
}

func upsertError(tx *gorm.DB, userID uint, date string, d ingestErrorDelta) error {
	row := models.DailyError{
		UserID: userID, Date: date, ErrorClass: d.ErrorClass, Count: d.Count,
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "date"}, {Name: "error_class"}},
		DoUpdates: clause.Assignments(map[string]any{
			"count": gorm.Expr("count + ?", d.Count),
		}),
	}).Create(&row).Error
}

func upsertTool(tx *gorm.DB, userID uint, date string, d ingestToolDelta) error {
	row := models.DailyToolUsage{
		UserID: userID, Date: date, ToolName: d.ToolName,
		Invocations: d.Invocations, SuccessCount: d.SuccessCount,
		ErrorCount: d.ErrorCount, TotalMs: d.TotalMs,
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "date"}, {Name: "tool_name"}},
		DoUpdates: clause.Assignments(map[string]any{
			"invocations":   gorm.Expr("invocations + ?", d.Invocations),
			"success_count": gorm.Expr("success_count + ?", d.SuccessCount),
			"error_count":   gorm.Expr("error_count + ?", d.ErrorCount),
			"total_ms":      gorm.Expr("total_ms + ?", d.TotalMs),
		}),
	}).Create(&row).Error
}
