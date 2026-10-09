// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// AuditEntry is an audit log row with its actor resolved. Actor is nil for a system action or an
// actor whose account no longer exists.
type AuditEntry struct {
	models.AuditLog
	Actor *AuditActor `json:"actor,omitempty"`
}

// withActors resolves every entry's actor in one query. A lookup failure leaves the actors unset
// rather than failing the listing: the ids are still there.
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
