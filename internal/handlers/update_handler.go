// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/services/updatecheck"
)

// UpdateHandler serves the cached result of the daily release check.
type UpdateHandler struct {
	svc *updatecheck.Service
}

func NewUpdateHandler(svc *updatecheck.Service) *UpdateHandler { return &UpdateHandler{svc: svc} }

// UpdateInfo is what the dashboard renders.
type UpdateInfo struct {
	CurrentVersion string     `json:"current_version"`
	LatestVersion  string     `json:"latest_version,omitempty"`
	ReleaseURL     string     `json:"release_url,omitempty"`
	PublishedAt    *time.Time `json:"published_at,omitempty"`
	// UpdateAvailable is false when up to date, when checks are disabled, and
	// when the admin dismissed this exact version.
	UpdateAvailable bool       `json:"update_available"`
	Enabled         bool       `json:"enabled"`
	CheckedAt       *time.Time `json:"checked_at,omitempty"`
	// LastError surfaces a silently failing checker; an admin should never mistake
	// "we could not reach GitHub for a month" for "you are up to date".
	LastError string `json:"last_error,omitempty"`
}

// DismissUpdateRequest silences the notice for one version.
type DismissUpdateRequest struct {
	Body struct {
		Version string `json:"version" required:"true"`
	} `json:"body"`
}

func (h *UpdateHandler) Get(c *okapi.Context) error {
	info := UpdateInfo{CurrentVersion: config.Version, Enabled: h.svc.Enabled()}
	st, err := h.svc.Status()
	if err != nil {
		return c.AbortInternalServerError("failed to read update status", err)
	}
	info.CheckedAt, info.LastError = st.CheckedAt, st.LastError
	// Gate on a fresh comparison, not on the cached row alone. The row is written by
	// a daily cron and describes the build that was running when it last ran, so an
	// install that upgraded an hour ago still carries the previous build's verdict —
	// which is how the notice could offer a version older than the one running.
	if st.LatestVersion != "" && updatecheck.IsNewer(config.Version, st.LatestVersion) {
		info.LatestVersion = st.LatestVersion
		info.ReleaseURL = st.ReleaseURL
		info.PublishedAt = st.PublishedAt
		info.UpdateAvailable = h.svc.Enabled() && st.DismissedVersion != st.LatestVersion
	}
	return ok(c, info)
}

// Dismiss hides the notice until a newer version appears.
func (h *UpdateHandler) Dismiss(c *okapi.Context, req *DismissUpdateRequest) error {
	if err := h.svc.Dismiss(req.Body.Version); err != nil {
		return c.AbortInternalServerError("failed to dismiss update notice", err)
	}
	return message(c, "update notice dismissed")
}
