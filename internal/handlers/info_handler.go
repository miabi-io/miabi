// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/dto"
)

// AppInfo describes the running application.
type AppInfo struct {
	Name      string `json:"name" example:"Miabi"`
	Version   string `json:"version"`
	CommitID  string `json:"commit_id"`
	BuildDate string `json:"build_date,omitempty"`
	// OpenAPIDocs reports whether the interactive API reference is served at /docs.
	OpenAPIDocs bool `json:"openapi_docs"`
}

// NewInfo returns the app-info handler. openAPIDocs reflects whether the /docs
// API reference is enabled, so the web UI can show or hide its link.
func NewInfo(openAPIDocs bool) okapi.HandlerFunc {
	return func(c *okapi.Context) error {
		return c.OK(dto.Response[AppInfo]{
			Success: true,
			Data: AppInfo{
				Name:        "Miabi",
				Version:     config.Version,
				CommitID:    config.CommitID,
				BuildDate:   buildDate(),
				OpenAPIDocs: openAPIDocs,
			},
		})
	}
}

// buildDate returns the linked build date, or empty when none was linked in.
func buildDate() string {
	if config.HasBuildDate() {
		return config.BuildDate
	}
	return ""
}
