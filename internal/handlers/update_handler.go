// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
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
	CurrentVersion  string     `json:"current_version"`
	LatestVersion   string     `json:"latest_version,omitempty"`
	ReleaseURL      string     `json:"release_url,omitempty"`
	PublishedAt     *time.Time `json:"published_at,omitempty"`
	UpdateAvailable bool       `json:"update_available"`
	// Security marks a release published to fix a vulnerability; the notice is shown prominently.
	Security  bool       `json:"security"`
	Enabled   bool       `json:"enabled"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	// A failing checker must never read as "you are up to date".
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
	// The cached row is from a daily cron and may describe the previous build, offering an older version.
	if st.LatestVersion != "" && updatecheck.IsNewer(config.Version, st.LatestVersion) {
		info.LatestVersion = st.LatestVersion
		info.ReleaseURL = st.ReleaseURL
		info.PublishedAt = st.PublishedAt
		info.Security = st.Reason == updatecheck.ReasonSecurity
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
