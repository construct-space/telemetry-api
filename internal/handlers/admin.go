package handlers

import (
	"net/http"
	"strings"
	"time"

	"construct/telemetry/internal/database"
	"construct/telemetry/internal/models"

	"gorm.io/gorm"
)

// Paginated envelope shared across every admin list endpoint. Same shape
// delivery-api + domains-api use so oracle's Pagination widget drops in
// with no adapter.
func paginated[T any](w http.ResponseWriter, q *gorm.DB, r *http.Request, order string) {
	page := atoi(r.URL.Query().Get("page"), 1, 100000)
	limit := atoi(r.URL.Query().Get("limit"), 25, 500)

	var total int64
	q.Count(&total)

	var rows []T
	q.Order(order).Offset((page - 1) * limit).Limit(limit).Find(&rows)

	WriteJSON(w, 200, map[string]any{
		"data":  rows,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

// GET /api/admin/usage
func AdminListUsage(w http.ResponseWriter, r *http.Request) {
	q := database.DB.Model(&models.DailyUsage{})
	if userID := r.URL.Query().Get("user_id"); userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	if date := r.URL.Query().Get("date"); date != "" {
		q = q.Where("date = ?", date)
	}
	paginated[models.DailyUsage](w, q, r, "date desc, user_id")
}

// GET /api/admin/devices
func AdminListDevices(w http.ResponseWriter, r *http.Request) {
	q := database.DB.Model(&models.UserDevice{})
	if userID := r.URL.Query().Get("user_id"); userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	if plat := r.URL.Query().Get("os_type"); plat != "" {
		q = q.Where("os_type = ?", plat)
	}
	paginated[models.UserDevice](w, q, r, "last_seen_at desc")
}

// GET /api/admin/space-usage
func AdminListSpaceUsage(w http.ResponseWriter, r *http.Request) {
	q := database.DB.Model(&models.DailySpaceUsage{})
	if space := r.URL.Query().Get("space_id"); space != "" {
		q = q.Where("space_id = ?", space)
	}
	if userID := r.URL.Query().Get("user_id"); userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	paginated[models.DailySpaceUsage](w, q, r, "date desc, active_minutes desc")
}

// GET /api/admin/model-usage
func AdminListModelUsage(w http.ResponseWriter, r *http.Request) {
	q := database.DB.Model(&models.DailyModelUsage{})
	if provider := r.URL.Query().Get("provider"); provider != "" {
		q = q.Where("provider = ?", provider)
	}
	if model := r.URL.Query().Get("model"); model != "" {
		q = q.Where("model = ?", model)
	}
	if userID := r.URL.Query().Get("user_id"); userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	paginated[models.DailyModelUsage](w, q, r, "date desc, request_count desc")
}

// GET /api/admin/perf
func AdminListPerf(w http.ResponseWriter, r *http.Request) {
	q := database.DB.Model(&models.DailyPerf{})
	if metric := r.URL.Query().Get("metric"); metric != "" {
		q = q.Where("metric = ?", metric)
	}
	if userID := r.URL.Query().Get("user_id"); userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	paginated[models.DailyPerf](w, q, r, "date desc, count desc")
}

// GET /api/admin/errors
func AdminListErrors(w http.ResponseWriter, r *http.Request) {
	q := database.DB.Model(&models.DailyError{})
	if class := r.URL.Query().Get("error_class"); class != "" {
		q = q.Where("error_class = ?", class)
	}
	if userID := r.URL.Query().Get("user_id"); userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	paginated[models.DailyError](w, q, r, "date desc, count desc")
}

// GET /api/admin/tools
func AdminListTools(w http.ResponseWriter, r *http.Request) {
	q := database.DB.Model(&models.DailyToolUsage{})
	if tool := r.URL.Query().Get("tool_name"); tool != "" {
		q = q.Where("tool_name = ?", tool)
	}
	if userID := r.URL.Query().Get("user_id"); userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	paginated[models.DailyToolUsage](w, q, r, "date desc, invocations desc")
}

// GET /api/admin/summary?days=7
// Headline KPIs for the oracle dashboard overview — 24h active users, 7d
// sum of sessions/tokens/cost, top providers by cost, error rate trend.
func AdminSummary(w http.ResponseWriter, r *http.Request) {
	days := atoi(r.URL.Query().Get("days"), 7, 90)
	since := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	today := time.Now().UTC().Format("2006-01-02")
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	// Headline totals across the window
	var totals struct {
		Sessions     int64
		ActiveMinutes int64
		TokensInput  int64
		TokensOutput int64
		Errors       int64
		ActiveUsers  int64
	}
	database.DB.Raw(`
		SELECT
			COALESCE(SUM(sessions),0)       AS sessions,
			COALESCE(SUM(active_minutes),0) AS active_minutes,
			COALESCE(SUM(tokens_input),0)   AS tokens_input,
			COALESCE(SUM(tokens_output),0)  AS tokens_output,
			COALESCE(SUM(errors),0)         AS errors,
			COUNT(DISTINCT user_id)         AS active_users
		FROM daily_usage
		WHERE date >= ?
	`, since).Scan(&totals)

	// Active users today + yesterday, for delta arrow in the UI
	var todayActive, yActive int64
	database.DB.Model(&models.DailyUsage{}).Where("date = ?", today).Distinct("user_id").Count(&todayActive)
	database.DB.Model(&models.DailyUsage{}).Where("date = ?", yesterday).Distinct("user_id").Count(&yActive)

	// Top providers by total cost in window
	type providerAgg struct {
		Provider     string  `json:"provider"`
		CostUSD      float64 `json:"cost_usd"`
		Requests     int64   `json:"requests"`
		TokensInput  int64   `json:"tokens_input"`
		TokensOutput int64   `json:"tokens_output"`
	}
	var topProviders []providerAgg
	database.DB.Raw(`
		SELECT provider,
		       COALESCE(SUM(cost_usd),0)      AS cost_usd,
		       COALESCE(SUM(request_count),0) AS requests,
		       COALESCE(SUM(tokens_input),0)  AS tokens_input,
		       COALESCE(SUM(tokens_output),0) AS tokens_output
		FROM   daily_model_usage
		WHERE  date >= ?
		GROUP  BY provider
		ORDER  BY cost_usd DESC
		LIMIT  10
	`, since).Scan(&topProviders)

	// Top errors (breaks the silence when something's on fire)
	type errorAgg struct {
		ErrorClass string `json:"error_class"`
		Count      int64  `json:"count"`
	}
	var topErrors []errorAgg
	database.DB.Raw(`
		SELECT error_class, COALESCE(SUM(count),0) AS count
		FROM   daily_errors
		WHERE  date >= ?
		GROUP  BY error_class
		ORDER  BY count DESC
		LIMIT  10
	`, since).Scan(&topErrors)

	// Devices split by OS — cheap aggregate, bounded-cardinality
	type osAgg struct {
		OsType string `json:"os_type"`
		Count  int64  `json:"count"`
	}
	var osMix []osAgg
	database.DB.Raw(`
		SELECT os_type, COUNT(DISTINCT user_id) AS count
		FROM   user_devices
		GROUP  BY os_type
		ORDER  BY count DESC
	`).Scan(&osMix)

	WriteJSON(w, 200, map[string]any{
		"window_days":      days,
		"totals":           totals,
		"active_today":     todayActive,
		"active_yesterday": yActive,
		"top_providers":    topProviders,
		"top_errors":       topErrors,
		"os_mix":           osMix,
	})
}

// GET /api/admin/top/users?days=7&limit=10
// Who are our heaviest users by active-minutes? For ops to spot power users
// and regressions (someone falling off).
func AdminTopUsers(w http.ResponseWriter, r *http.Request) {
	days := atoi(r.URL.Query().Get("days"), 7, 90)
	limit := atoi(r.URL.Query().Get("limit"), 10, 100)
	since := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")

	type row struct {
		UserID        uint  `json:"user_id"`
		Sessions      int64 `json:"sessions"`
		ActiveMinutes int64 `json:"active_minutes"`
		TokensTotal   int64 `json:"tokens_total"`
		Errors        int64 `json:"errors"`
	}
	var rows []row
	database.DB.Raw(`
		SELECT user_id,
		       COALESCE(SUM(sessions),0)                               AS sessions,
		       COALESCE(SUM(active_minutes),0)                         AS active_minutes,
		       COALESCE(SUM(tokens_input + tokens_output),0)           AS tokens_total,
		       COALESCE(SUM(errors),0)                                 AS errors
		FROM   daily_usage
		WHERE  date >= ?
		GROUP  BY user_id
		ORDER  BY active_minutes DESC
		LIMIT  ?
	`, since, limit).Scan(&rows)

	WriteJSON(w, 200, map[string]any{"data": rows, "window_days": days})
}

// GET /api/admin/top/models?days=7&limit=10
func AdminTopModels(w http.ResponseWriter, r *http.Request) {
	days := atoi(r.URL.Query().Get("days"), 7, 90)
	limit := atoi(r.URL.Query().Get("limit"), 10, 100)
	since := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")

	type row struct {
		Provider     string  `json:"provider"`
		Model        string  `json:"model"`
		Requests     int64   `json:"requests"`
		TokensInput  int64   `json:"tokens_input"`
		TokensOutput int64   `json:"tokens_output"`
		CostUSD      float64 `json:"cost_usd"`
	}
	var rows []row
	database.DB.Raw(`
		SELECT provider, model,
		       COALESCE(SUM(request_count),0) AS requests,
		       COALESCE(SUM(tokens_input),0)  AS tokens_input,
		       COALESCE(SUM(tokens_output),0) AS tokens_output,
		       COALESCE(SUM(cost_usd),0)      AS cost_usd
		FROM   daily_model_usage
		WHERE  date >= ?
		GROUP  BY provider, model
		ORDER  BY requests DESC
		LIMIT  ?
	`, since, limit).Scan(&rows)

	WriteJSON(w, 200, map[string]any{"data": rows, "window_days": days})
}

// GET /api/admin/top/spaces?days=7&limit=10
func AdminTopSpaces(w http.ResponseWriter, r *http.Request) {
	days := atoi(r.URL.Query().Get("days"), 7, 90)
	limit := atoi(r.URL.Query().Get("limit"), 10, 100)
	since := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")

	type row struct {
		SpaceID       string `json:"space_id"`
		EnterCount    int64  `json:"enter_count"`
		ActiveMinutes int64  `json:"active_minutes"`
		UniqueUsers   int64  `json:"unique_users"`
	}
	var rows []row
	database.DB.Raw(`
		SELECT space_id,
		       COALESCE(SUM(enter_count),0)    AS enter_count,
		       COALESCE(SUM(active_minutes),0) AS active_minutes,
		       COUNT(DISTINCT user_id)         AS unique_users
		FROM   daily_space_usage
		WHERE  date >= ?
		GROUP  BY space_id
		ORDER  BY active_minutes DESC
		LIMIT  ?
	`, since, limit).Scan(&rows)

	WriteJSON(w, 200, map[string]any{"data": rows, "window_days": days})
}

// GET /api/admin/geo/users?country=XK&days=7
// Drill-down into a single country: which users were active there in the
// window, with per-user totals so the panel can rank them by usage instead
// of dumping an unsorted list. Country code match is case-insensitive.
func AdminGeoUsers(w http.ResponseWriter, r *http.Request) {
	cc := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("country")))
	if cc == "" {
		WriteJSON(w, 400, map[string]string{"error": "country is required"})
		return
	}
	days := atoi(r.URL.Query().Get("days"), 7, 365)
	limit := atoi(r.URL.Query().Get("limit"), 100, 1000)
	since := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")

	type row struct {
		UserID        uint  `json:"user_id"`
		Sessions      int64 `json:"sessions"`
		ActiveMinutes int64 `json:"active_minutes"`
		TokensTotal   int64 `json:"tokens_total"`
		Errors        int64 `json:"errors"`
		LastActive    string `json:"last_active"`
	}
	var rows []row
	database.DB.Raw(`
		SELECT user_id,
		       COALESCE(SUM(sessions),0)                     AS sessions,
		       COALESCE(SUM(active_minutes),0)               AS active_minutes,
		       COALESCE(SUM(tokens_input + tokens_output),0) AS tokens_total,
		       COALESCE(SUM(errors),0)                       AS errors,
		       MAX(date)                                     AS last_active
		FROM   daily_usage
		WHERE  date >= ? AND country_code = ?
		GROUP  BY user_id
		ORDER  BY active_minutes DESC
		LIMIT  ?
	`, since, cc, limit).Scan(&rows)

	WriteJSON(w, 200, map[string]any{"data": rows, "country_code": cc, "window_days": days})
}

// GET /api/admin/geo?days=7
// Active-user breakdown by ISO country code over the rolling window.
// Powers a map/list view in oracle. Excludes rows with no country (dev
// boxes, private-IP ingest, missing GeoIP DB) so the totals reflect what
// we actually classified rather than implying "unknown" is a country.
func AdminGeo(w http.ResponseWriter, r *http.Request) {
	days := atoi(r.URL.Query().Get("days"), 7, 365)
	since := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")

	type row struct {
		CountryCode   string `json:"country_code"`
		ActiveUsers   int64  `json:"active_users"`
		Sessions      int64  `json:"sessions"`
		ActiveMinutes int64  `json:"active_minutes"`
	}
	var rows []row
	database.DB.Raw(`
		SELECT country_code,
		       COUNT(DISTINCT user_id)         AS active_users,
		       COALESCE(SUM(sessions),0)       AS sessions,
		       COALESCE(SUM(active_minutes),0) AS active_minutes
		FROM   daily_usage
		WHERE  date >= ? AND country_code <> ''
		GROUP  BY country_code
		ORDER  BY active_users DESC
	`, since).Scan(&rows)

	WriteJSON(w, 200, map[string]any{"data": rows, "window_days": days})
}

// GET /api/admin/trends?days=30
// Daily time-series for headline counters so the dashboard can draw a
// tiny sparkline against each KPI card.
func AdminTrends(w http.ResponseWriter, r *http.Request) {
	days := atoi(r.URL.Query().Get("days"), 30, 365)
	since := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")

	type dayRow struct {
		Date          string  `json:"date"`
		Sessions      int64   `json:"sessions"`
		ActiveUsers   int64   `json:"active_users"`
		TokensTotal   int64   `json:"tokens_total"`
		CostUSD       float64 `json:"cost_usd"`
		Errors        int64   `json:"errors"`
	}
	var rows []dayRow
	database.DB.Raw(`
		SELECT u.date                                                       AS date,
		       COALESCE(SUM(u.sessions),0)                                  AS sessions,
		       COUNT(DISTINCT u.user_id)                                    AS active_users,
		       COALESCE(SUM(u.tokens_input + u.tokens_output),0)            AS tokens_total,
		       COALESCE((SELECT SUM(cost_usd)
		                 FROM   daily_model_usage m
		                 WHERE  m.date = u.date),0)                         AS cost_usd,
		       COALESCE(SUM(u.errors),0)                                    AS errors
		FROM   daily_usage u
		WHERE  u.date >= ?
		GROUP  BY u.date
		ORDER  BY u.date ASC
	`, since).Scan(&rows)

	WriteJSON(w, 200, map[string]any{"data": rows, "window_days": days})
}
