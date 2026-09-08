package handlers

import (
	"net/http"
	"time"

	"construct/telemetry/internal/database"
	"construct/telemetry/internal/geoip"
	"construct/telemetry/internal/middleware"
	"construct/telemetry/internal/models"
)

// PUT /api/device
// Upserts a row in user_devices keyed by (user_id, app_version, os_type).
// Called by the app on boot and after an update — keeps first_seen_at on
// the first write, rolls last_seen_at on every subsequent write.
func UpsertDevice(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == 0 {
		WriteJSON(w, 401, map[string]string{"error": "unauthenticated"})
		return
	}

	var body struct {
		AppVersion string `json:"app_version"`
		OsType     string `json:"os_type"`
		OsPlatform string `json:"os_platform"`
		OsArch     string `json:"os_arch"`
		OsVersion  string `json:"os_version"`
	}
	if err := parseBody(r, &body); err != nil {
		WriteJSON(w, 400, map[string]string{"error": "invalid body"})
		return
	}

	now := time.Now().UTC()
	country := geoip.LookupCountry(geoip.ClientIP(r))

	var existing models.UserDevice
	err := database.DB.Where(
		"user_id = ? AND app_version = ? AND os_type = ?",
		userID, body.AppVersion, body.OsType,
	).First(&existing).Error

	if err == nil {
		// Known device — bump last_seen + refresh the plat/arch/version
		// columns in case they changed between boots. Only overwrite
		// country_code when we got a real lookup; an empty result (e.g.
		// dev box on a private IP) shouldn't erase a known location.
		existing.LastSeenAt = now
		existing.OsPlatform = body.OsPlatform
		existing.OsArch = body.OsArch
		existing.OsVersion = body.OsVersion
		if country != "" {
			existing.CountryCode = country
		}
		if err := database.DB.Save(&existing).Error; err != nil {
			WriteJSON(w, 500, map[string]string{"error": "save failed"})
			return
		}
		WriteJSON(w, 200, map[string]any{"device": existing, "created": false})
		return
	}

	// New device row.
	d := models.UserDevice{
		UserID:      userID,
		AppVersion:  body.AppVersion,
		OsType:      body.OsType,
		OsPlatform:  body.OsPlatform,
		OsArch:      body.OsArch,
		OsVersion:   body.OsVersion,
		CountryCode: country,
		FirstSeenAt: now,
		LastSeenAt:  now,
	}
	if err := database.DB.Create(&d).Error; err != nil {
		WriteJSON(w, 500, map[string]string{"error": "create failed"})
		return
	}
	WriteJSON(w, 201, map[string]any{"device": d, "created": true})
}
