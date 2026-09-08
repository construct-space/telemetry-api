package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"time"

	"construct/telemetry/internal/database"
	"construct/telemetry/internal/geoip"
	"construct/telemetry/internal/middleware"
	"construct/telemetry/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Truncation limits. Stack of 4000 bytes covers ~30 frames; longer tails
// are noise for a triage-grade signal. Message at 500 bytes keeps PII
// blast radius bounded even if a client forgot to redact.
const (
	maxMessageLen = 500
	maxStackLen   = 4000
	maxClassLen   = 120
)

// Server-side sanitization of stack traces. The client should already
// normalize user paths to <HOME>; we do it again as defense in depth in
// case an older or buggy client leaks them.
//
// Patterns covered:
//   - macOS: /Users/<name>/...
//   - Linux: /home/<name>/...
//   - Windows: C:\Users\<name>\... (and forward-slash variants)
var (
	macUserPath     = regexp.MustCompile(`/Users/[^/\s)]+`)
	linuxUserPath   = regexp.MustCompile(`/home/[^/\s)]+`)
	winUserPathBack = regexp.MustCompile(`(?i)[A-Z]:\\Users\\[^\\\s)]+`)
	winUserPathFwd  = regexp.MustCompile(`(?i)[A-Z]:/Users/[^/\s)]+`)
)

func sanitizeStack(s string) string {
	if s == "" {
		return s
	}
	s = macUserPath.ReplaceAllString(s, "<HOME>")
	s = linuxUserPath.ReplaceAllString(s, "<HOME>")
	s = winUserPathBack.ReplaceAllString(s, "<HOME>")
	s = winUserPathFwd.ReplaceAllString(s, "<HOME>")
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// fingerprint groups near-identical reports. Hash of class + first 3 stack
// frame names (function/file refs) — stable across parameter values and
// line-number drift, which makes "this same crash 200 times" obvious.
func fingerprint(class, stack string) string {
	h := sha256.New()
	h.Write([]byte(class))
	h.Write([]byte{0})
	frames := stackFrames(stack, 3)
	for _, f := range frames {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16]) // 32-char fingerprint
}

// stackFrames pulls the first n meaningful frame identifiers out of a
// stack. Format-agnostic — we just keep non-empty trimmed lines that
// don't look like noise (Error: prefix, blank, etc).
func stackFrames(stack string, n int) []string {
	out := make([]string, 0, n)
	for line := range strings.SplitSeq(stack, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Error:") || strings.HasPrefix(line, "Caused by:") {
			continue
		}
		// Strip absolute offsets that vary per build but keep the frame
		// signature: "  at toolbar3d.vue:42:7" → "toolbar3d.vue".
		line = regexp.MustCompile(`:\d+(:\d+)?$`).ReplaceAllString(line, "")
		out = append(out, line)
		if len(out) >= n {
			break
		}
	}
	return out
}

// ── Ingest ───────────────────────────────────────────────────────────────

type errorEventBody struct {
	OccurredAt string `json:"occurred_at"` // RFC3339; defaults to now if empty
	Source     string `json:"source"`      // frontend|operator|desktop
	Severity   string `json:"severity"`    // error|fatal|panic — defaults to "error"
	ErrorClass string `json:"error_class"`
	Message    string `json:"message"`
	Stack      string `json:"stack"`
	AppVersion string `json:"app_version"`
	Platform   string `json:"platform"`
	OsVersion  string `json:"os_version"`
	OsArch     string `json:"os_arch"`
}

var allowedSources = map[string]struct{}{
	"frontend": {},
	"operator": {},
	"desktop":  {},
}

var allowedSeverities = map[string]struct{}{
	"error": {},
	"fatal": {},
	"panic": {},
}

// POST /api/errors
//
// One error per request — keeps the contract simple and lets a misbehaving
// client be rate-limited at the edge without dropping useful aggregate
// data (DailyError still increments here, so even if individual events
// get throttled, the count signal survives).
func IngestErrorEvent(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == 0 {
		WriteJSON(w, 401, map[string]string{"error": "unauthenticated"})
		return
	}

	var body errorEventBody
	if err := parseBody(r, &body); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid body: " + err.Error()})
		return
	}
	if body.ErrorClass == "" {
		WriteJSON(w, 400, map[string]string{"error": "error_class is required"})
		return
	}
	if _, ok := allowedSources[body.Source]; !ok {
		WriteJSON(w, 400, map[string]string{"error": "source must be frontend|operator|desktop"})
		return
	}
	if body.Severity == "" {
		body.Severity = "error"
	}
	if _, ok := allowedSeverities[body.Severity]; !ok {
		WriteJSON(w, 400, map[string]string{"error": "severity must be error|fatal|panic"})
		return
	}

	occurred := time.Now().UTC()
	if body.OccurredAt != "" {
		if t, err := time.Parse(time.RFC3339, body.OccurredAt); err == nil {
			occurred = t.UTC()
		}
	}
	// Reject far-future or ancient timestamps so a misconfigured clock
	// can't pollute the table; clamp instead of failing the request.
	now := time.Now().UTC()
	if occurred.After(now.Add(5 * time.Minute)) {
		occurred = now
	}
	if occurred.Before(now.Add(-30 * 24 * time.Hour)) {
		occurred = now.Add(-30 * 24 * time.Hour)
	}

	class := truncate(body.ErrorClass, maxClassLen)
	msg := truncate(body.Message, maxMessageLen)
	stack := truncate(sanitizeStack(body.Stack), maxStackLen)

	country := geoip.LookupCountry(geoip.ClientIP(r))

	row := models.ErrorEvent{
		UserID:      userID,
		OccurredAt:  occurred,
		Source:      body.Source,
		Severity:    body.Severity,
		ErrorClass:  class,
		Fingerprint: fingerprint(class, stack),
		Message:     msg,
		Stack:       stack,
		AppVersion:  body.AppVersion,
		Platform:    body.Platform,
		OsVersion:   body.OsVersion,
		OsArch:      body.OsArch,
		CountryCode: country,
		ReceivedAt:  now,
	}

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		// Bump the daily aggregate too so existing dashboards stay accurate
		// without joining the new table. Same upsert pattern as the batch
		// path uses.
		date := occurred.Format("2006-01-02")
		agg := models.DailyError{UserID: userID, Date: date, ErrorClass: class, Count: 1}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "date"}, {Name: "error_class"}},
			DoUpdates: clause.Assignments(map[string]any{"count": gorm.Expr("count + ?", 1)}),
		}).Create(&agg).Error
	}); err != nil {
		WriteJSON(w, 500, map[string]string{"error": "ingest failed: " + err.Error()})
		return
	}

	WriteJSON(w, 202, map[string]any{"id": row.ID, "fingerprint": row.Fingerprint})
}

// ── Admin query ──────────────────────────────────────────────────────────

// GET /api/admin/error-events
//
// Filters: ?source=frontend&class=type_error.toolbar3d&fingerprint=<hex>
//          &user_id=<id>&since=<rfc3339>&until=<rfc3339>
//
// Default sort is occurred_at desc so the latest crashes surface first.
// `class` here matches `error_class` exactly; for prefix search use the
// dedicated grouped endpoint below.
func AdminListErrorEvents(w http.ResponseWriter, r *http.Request) {
	q := database.DB.Model(&models.ErrorEvent{})
	if v := r.URL.Query().Get("source"); v != "" {
		q = q.Where("source = ?", v)
	}
	if v := r.URL.Query().Get("class"); v != "" {
		q = q.Where("error_class = ?", v)
	}
	if v := r.URL.Query().Get("fingerprint"); v != "" {
		q = q.Where("fingerprint = ?", v)
	}
	if v := r.URL.Query().Get("user_id"); v != "" {
		q = q.Where("user_id = ?", v)
	}
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q = q.Where("occurred_at >= ?", t)
		}
	}
	if v := r.URL.Query().Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q = q.Where("occurred_at < ?", t)
		}
	}
	paginated[models.ErrorEvent](w, q, r, "occurred_at desc")
}

// GET /api/admin/error-events/groups
//
// Bucketed view: one row per (fingerprint, class) with count + first/last
// seen + a sample event ID for drilling in. Answers "what are the top
// distinct crashes this week" without scanning every row in the UI.
type errorEventGroup struct {
	Fingerprint string    `json:"fingerprint"`
	ErrorClass  string    `json:"error_class"`
	Source      string    `json:"source"`
	Count       int64     `json:"count"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	SampleID    uint64    `json:"sample_id"`
}

func AdminListErrorEventGroups(w http.ResponseWriter, r *http.Request) {
	q := database.DB.Model(&models.ErrorEvent{}).
		Select("fingerprint, MIN(error_class) AS error_class, MIN(source) AS source, " +
			"COUNT(*) AS count, MIN(occurred_at) AS first_seen, MAX(occurred_at) AS last_seen, " +
			"MAX(id) AS sample_id").
		Group("fingerprint")

	if v := r.URL.Query().Get("source"); v != "" {
		q = q.Where("source = ?", v)
	}
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q = q.Where("occurred_at >= ?", t)
		}
	}

	var groups []errorEventGroup
	if err := q.Order("count DESC").Limit(200).Find(&groups).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	WriteJSON(w, 200, map[string]any{"data": groups, "total": len(groups)})
}
