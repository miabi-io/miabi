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
	"sort"

	"github.com/jkaninda/okapi"
	cronpkg "github.com/miabi-io/miabi/internal/cron"
)

// AdminJobHandler reports the status of scheduled background jobs.
type AdminJobHandler struct {
	cron *cronpkg.Manager
}

func NewAdminJobHandler(cron *cronpkg.Manager) *AdminJobHandler {
	return &AdminJobHandler{cron: cron}
}

// The cron snapshot is an unordered map, so sort for deterministic pagination.
func (h *AdminJobHandler) List(c *okapi.Context) error {
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	if h.cron == nil {
		return paginated(c, []cronpkg.JobStatus{}, 0, page, size)
	}
	jobs := h.cron.Snapshot()
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].Kind != jobs[j].Kind {
			return jobs[i].Kind < jobs[j].Kind
		}
		if jobs[i].Name != jobs[j].Name {
			return jobs[i].Name < jobs[j].Name
		}
		return jobs[i].ID < jobs[j].ID
	})
	total := int64(len(jobs))
	if offset > len(jobs) {
		offset = len(jobs)
	}
	end := offset + size
	if end > len(jobs) {
		end = len(jobs)
	}
	return paginated(c, jobs[offset:end], total, page, size)
}

type JobStats struct {
	Total   int            `json:"total"`
	Running int            `json:"running"`
	Failed  int            `json:"failed"`
	OK      int            `json:"ok"`
	ByKind  map[string]int `json:"by_kind"`
}

// Failed, Running and OK are disjoint and sum to Total.
func (h *AdminJobHandler) Stats(c *okapi.Context) error {
	stats := JobStats{ByKind: map[string]int{}}
	if h.cron == nil {
		return ok(c, stats)
	}
	for _, j := range h.cron.Snapshot() {
		stats.Total++
		stats.ByKind[j.Kind]++
		switch {
		case j.Running:
			stats.Running++
		case j.LastError != "":
			stats.Failed++
		default:
			stats.OK++
		}
	}
	return ok(c, stats)
}
