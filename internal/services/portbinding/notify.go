// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package portbinding

import (
	"fmt"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// ReviewNotifier tells the platform admins a binding is waiting on them. Without
// it a request sits in the queue until somebody thinks to look, which is how a
// developer ends up blocked for a week on a one-click approval.
type ReviewNotifier interface {
	NotifyAdmins(n models.Notification) error
}

// SetReviewNotifier wires admin notification for pending requests (nil-safe).
func (s *Service) SetReviewNotifier(n ReviewNotifier) { s.notify = n }

// notifyPending raises one inbox item per platform admin. Best-effort: a request
// that cannot be announced is still a valid request.
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
