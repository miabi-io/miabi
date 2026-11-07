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
