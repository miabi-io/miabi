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

package portbinding

import (
	"fmt"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// Without it a request sits unseen and a developer stays blocked on a one-click approval.
type ReviewNotifier interface {
	NotifyAdmins(n models.Notification) error
}

// SetReviewNotifier wires admin notification for pending requests (nil-safe).
func (s *Service) SetReviewNotifier(n ReviewNotifier) { s.notify = n }

// Best-effort: a request that cannot be announced is still valid.
func (s *Service) notifyPending(b *models.PortBinding) {
	if s.notify == nil || b == nil || b.Status != models.PortBindingPending {
		return
	}
	name := fmt.Sprintf("application %d", b.ApplicationID)
	if s.apps != nil {
		if app, err := s.apps.FindByID(b.ApplicationID); err == nil && app.Name != "" {
			name = app.Name
		}
	}
	err := s.notify.NotifyAdmins(models.Notification{
		Kind:     models.NotificationKindInfo,
		Severity: models.AlertInfo,
		Title:    fmt.Sprintf("Host port %d/%s awaiting review", b.HostPort, normProto(b.Protocol)),
		Body: fmt.Sprintf("%s requested host port %d/%s for container port %d. It will not publish until approved.",
			name, b.HostPort, normProto(b.Protocol), b.ContainerPort),
		SubjectLink: "/admin/ports",
		ActionText:  "Review",
	})
	if err != nil {
		logger.Warn("port binding: could not notify admins of a pending request", "binding", b.ID, "error", err)
	}
}
