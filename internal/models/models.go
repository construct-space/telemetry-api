// Package models defines the telemetry DB schema. All tables are keyed by
// (user_id, date) with additional dimension columns where relevant
// (space_id, model, provider, metric, tool, error_class), enforced via
// composite unique indexes so ingest can UPSERT with ON CONFLICT.
//
// Extended beyond the original 5-table oracle_old read-only model to also
// track errors, tool invocations, and AI spend — signals we need for day-1
// operational visibility.
package models

import "time"

// UserDevice — one row per (user_id, app_version, os_type). Upserted on boot
// so we can see device distribution across the install base.
//
// CountryCode is ISO-3166 alpha-2 derived from the request IP at boot/sync
// via GeoLite2-Country. It refreshes on every device upsert, so a user who
// travels gets their country bumped on the next app launch. Empty string
// means lookup failed (private IP, missing DB, or unknown range).
type UserDevice struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	UserID      uint      `gorm:"uniqueIndex:idx_device_uniq,priority:1;not null" json:"user_id"`
	AppVersion  string    `gorm:"uniqueIndex:idx_device_uniq,priority:2;size:20" json:"app_version"`
	OsType      string    `gorm:"uniqueIndex:idx_device_uniq,priority:3;size:20" json:"os_type"`
	OsPlatform  string    `gorm:"size:20" json:"os_platform"`
	OsArch      string    `gorm:"size:20" json:"os_arch"`
	OsVersion   string    `gorm:"size:50" json:"os_version"`
	CountryCode string    `gorm:"size:2;index" json:"country_code"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `gorm:"index" json:"last_seen_at"`
}

func (UserDevice) TableName() string { return "user_devices" }

// DailyUsage — one row per (user_id, date). The primary "how active is this
// user" rollup. Deltas from the app's hourly sync get summed into this row.
//
// CountryCode is the ISO-3166 alpha-2 country we observed on the most
// recent ingest of this row's day. Last-write-wins, not historical: if the
// user crossed a border mid-day we'd just see the final country. Good
// enough for a "DAU by country" map; not a forensic record.
type DailyUsage struct {
	ID            uint   `gorm:"primaryKey" json:"id"`
	UserID        uint   `gorm:"uniqueIndex:idx_usage_uniq,priority:1;not null;index" json:"user_id"`
	Date          string `gorm:"uniqueIndex:idx_usage_uniq,priority:2;size:10;not null" json:"date"`
	CountryCode   string `gorm:"size:2;index" json:"country_code"`
	Sessions      int    `json:"sessions"`
	ActiveMinutes int    `json:"active_minutes"`
	ChatsSent     int    `json:"chats_sent"`
	ToolCalls     int    `json:"tool_calls"`
	TokensInput   int64  `json:"tokens_input"`
	TokensOutput  int64  `json:"tokens_output"`
	FileSaves     int    `json:"file_saves"`
	GitCommits    int    `json:"git_commits"`
	Errors        int    `json:"errors"`
}

func (DailyUsage) TableName() string { return "daily_usage" }

// DailySpaceUsage — (user_id, date, space_id). Which spaces each user touched.
type DailySpaceUsage struct {
	ID            uint   `gorm:"primaryKey" json:"id"`
	UserID        uint   `gorm:"uniqueIndex:idx_space_uniq,priority:1;not null;index" json:"user_id"`
	Date          string `gorm:"uniqueIndex:idx_space_uniq,priority:2;size:10;not null" json:"date"`
	SpaceID       string `gorm:"uniqueIndex:idx_space_uniq,priority:3;size:100;not null" json:"space_id"`
	EnterCount    int    `json:"enter_count"`
	ActiveMinutes int    `json:"active_minutes"`
}

func (DailySpaceUsage) TableName() string { return "daily_space_usage" }

// DailyModelUsage — (user_id, date, provider, model). Token + request volume
// split by both provider and model, so we can see cross-provider substitution.
type DailyModelUsage struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	UserID       uint   `gorm:"uniqueIndex:idx_model_uniq,priority:1;not null;index" json:"user_id"`
	Date         string `gorm:"uniqueIndex:idx_model_uniq,priority:2;size:10;not null" json:"date"`
	Provider     string `gorm:"uniqueIndex:idx_model_uniq,priority:3;size:50;not null" json:"provider"`
	Model        string `gorm:"uniqueIndex:idx_model_uniq,priority:4;size:80;not null" json:"model"`
	RequestCount int    `json:"request_count"`
	TokensInput  int64  `json:"tokens_input"`
	TokensOutput int64  `json:"tokens_output"`
	CacheRead    int64  `json:"cache_read"`
	CacheWrite   int64  `json:"cache_write"`
	ToolCalls    int    `json:"tool_calls"`
	CostUSD      float64 `gorm:"column:cost_usd" json:"cost_usd"`
}

func (DailyModelUsage) TableName() string { return "daily_model_usage" }

// DailyPerf — (user_id, date, metric). Latency histogram storage using
// the accumulate-then-average pattern: {count, total_ms, min_ms, max_ms}
// lets you compute mean/min/max without storing every sample.
//
// For percentile latency, callers use DailyPerfBucket below instead —
// bucketed into fixed latency ranges so p50/p95/p99 can be derived from
// the sums.
type DailyPerf struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	UserID  uint   `gorm:"uniqueIndex:idx_perf_uniq,priority:1;not null;index" json:"user_id"`
	Date    string `gorm:"uniqueIndex:idx_perf_uniq,priority:2;size:10;not null" json:"date"`
	Metric  string `gorm:"uniqueIndex:idx_perf_uniq,priority:3;size:80;not null" json:"metric"`
	Count   int    `json:"count"`
	TotalMs int64  `json:"total_ms"`
	MinMs   int    `json:"min_ms"`
	MaxMs   int    `json:"max_ms"`
}

func (DailyPerf) TableName() string { return "daily_perf" }

// DailyError — per-day error class counts. Cheap signal that doesn't ship
// stack traces or messages (those stay on the device + go to a proper
// error tracker if we add one). Class examples: "provider_rate_limit",
// "network", "tool_error", "panic", "oauth".
type DailyError struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	UserID     uint   `gorm:"uniqueIndex:idx_error_uniq,priority:1;not null;index" json:"user_id"`
	Date       string `gorm:"uniqueIndex:idx_error_uniq,priority:2;size:10;not null" json:"date"`
	ErrorClass string `gorm:"uniqueIndex:idx_error_uniq,priority:3;size:80;not null" json:"error_class"`
	Count      int    `json:"count"`
}

func (DailyError) TableName() string { return "daily_errors" }

// DailyToolUsage — per-day per-tool invocation counts. Answers "which of
// the 22 builtin tools are actually being used?" Useful for dropping dead
// tools and prioritizing the ones that matter.
type DailyToolUsage struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	UserID       uint   `gorm:"uniqueIndex:idx_tool_uniq,priority:1;not null;index" json:"user_id"`
	Date         string `gorm:"uniqueIndex:idx_tool_uniq,priority:2;size:10;not null" json:"date"`
	ToolName     string `gorm:"uniqueIndex:idx_tool_uniq,priority:3;size:80;not null" json:"tool_name"`
	Invocations  int    `json:"invocations"`
	SuccessCount int    `json:"success_count"`
	ErrorCount   int    `json:"error_count"`
	TotalMs      int64  `json:"total_ms"`
}

func (DailyToolUsage) TableName() string { return "daily_tool_usage" }

// ErrorEvent — one row per individual error reported. Sits alongside
// DailyError (which keeps the cheap aggregate signal); ErrorEvent gives
// us actual stacks for triage. Source distinguishes JS runtime errors
// (frontend), Go operator panics (operator), and Rust desktop panics
// (desktop) — so a single dashboard can show "all errors today" or filter
// by surface.
//
// Privacy: opt-in (same telemetry consent flag as the rollups). Message
// is truncated to 500 chars; stack to 4000. The client is expected to
// strip user paths to <HOME>; the server does it again as a defense in
// depth (sanitize.go::SanitizeStack). Fingerprint is sha256 of
// (error_class + first 3 stack frame names) — lets dashboards group
// near-identical reports without joining on full stack text.
type ErrorEvent struct {
	ID          uint64    `gorm:"primaryKey" json:"id"`
	UserID      uint      `gorm:"index;not null" json:"user_id"`
	OccurredAt  time.Time `gorm:"index;not null" json:"occurred_at"`
	Source      string    `gorm:"size:16;index;not null" json:"source"` // frontend|operator|desktop
	Severity    string    `gorm:"size:16" json:"severity"`              // error|fatal|panic
	ErrorClass  string    `gorm:"size:120;index;not null" json:"error_class"`
	Fingerprint string    `gorm:"size:64;index;not null" json:"fingerprint"`
	Message     string    `gorm:"size:500" json:"message"`
	Stack       string    `gorm:"type:text" json:"stack"`
	AppVersion  string    `gorm:"size:20;index" json:"app_version"`
	Platform    string    `gorm:"size:20" json:"platform"`
	OsVersion   string    `gorm:"size:50" json:"os_version"`
	OsArch      string    `gorm:"size:20" json:"os_arch"`
	CountryCode string    `gorm:"size:2;index" json:"country_code"`
	ReceivedAt  time.Time `gorm:"index;not null" json:"received_at"`
}

func (ErrorEvent) TableName() string { return "error_events" }

// IngestLog — audit breadcrumb for each sync batch the app sends. Keeps the
// last N batches so ops can see "did this user's device sync recently".
// Not used for rollups — those come from the daily_* tables.
type IngestLog struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     uint      `gorm:"index;not null" json:"user_id"`
	AppVersion string    `gorm:"size:20" json:"app_version"`
	EventCount int       `json:"event_count"`
	ReceivedAt time.Time `gorm:"index" json:"received_at"`
}

func (IngestLog) TableName() string { return "ingest_log" }
