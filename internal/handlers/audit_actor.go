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
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// AuditActor is who performed an audited action, resolved for display: the log stores only an id.
type AuditActor struct {
	ID    uint   `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	// Kind is "service" for a service account, so an automated action reads apart from a person's.
	Kind string `json:"kind"`
}

// Actor is nil for a system action or an actor whose account no longer exists.
type AuditEntry struct {
	models.AuditLog
	Actor *AuditActor `json:"actor,omitempty"`
}

// A lookup failure leaves actors unset rather than failing the listing.
func withActors(users *repositories.UserRepository, entries []models.AuditLog) []AuditEntry {
	out := make([]AuditEntry, len(entries))
	ids := make([]uint, 0, len(entries))
	seen := map[uint]bool{}
	for i, e := range entries {
		out[i].AuditLog = e
		if e.ActorID != nil && *e.ActorID != 0 && !seen[*e.ActorID] {
			seen[*e.ActorID] = true
			ids = append(ids, *e.ActorID)
		}
	}
	if users == nil || len(ids) == 0 {
		return out
	}
	found, err := users.FindByIDs(ids)
	if err != nil {
		return out
	}
	byID := make(map[uint]*AuditActor, len(found))
	for _, u := range found {
		byID[u.ID] = &AuditActor{ID: u.ID, Name: u.Name, Email: u.Email, Kind: u.Kind}
	}
	for i := range out {
		if id := out[i].ActorID; id != nil {
			out[i].Actor = byID[*id]
		}
	}
	return out
}
