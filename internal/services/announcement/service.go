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

// Package announcement broadcasts platform notices as ordinary per-user notification rows.
package announcement

import (
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/alerting"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Publisher pushes an inbox-changed nudge to one user's SSE stream.
type Publisher interface {
	Publish(topic string, e eventbus.Event)
}

type Service struct {
	repo  *repositories.AnnouncementRepository
	inbox *repositories.NotificationInboxRepository
	bus   Publisher
	now   func() time.Time

	mu         sync.Mutex
	lastSynced map[uint]time.Time
}

func NewService(repo *repositories.AnnouncementRepository, inbox *repositories.NotificationInboxRepository, bus Publisher) *Service {
	return &Service{repo: repo, inbox: inbox, bus: bus, now: time.Now, lastSynced: map[uint]time.Time{}}
}

// A page load hits the inbox three times and the answer cannot change in between.
const syncInterval = time.Minute

// Create defers a scheduled announcement to Tick.
func (s *Service) Create(a *models.Announcement) error {
	now := s.now().UTC()
	if a.PublishAt != nil && a.PublishAt.After(now) {
		if err := s.repo.Create(a); err != nil {
			return err
		}
		a.Status = a.State(now)
		return nil
	}
	a.PublishAt = nil
	a.PublishedAt = &now
	if err := s.repo.Create(a); err != nil {
		return err
	}
	return s.broadcast(a)
}

// Update rewrites existing deliveries so a correction replaces the notice; a widened audience lands on next backfill.
func (s *Service) Update(a *models.Announcement) error {
	if err := s.repo.Save(a); err != nil {
		return err
	}
	if a.PublishedAt == nil {
		a.Status = a.State(s.now().UTC())
		return nil
	}
	users, err := s.inbox.ApplyAnnouncementUpdate(a.ID, s.delivery(a, 0))
	if err != nil {
		return err
	}
	s.resetSyncMarks()
	s.pushAll(users)
	a.Status = a.State(s.now().UTC())
	return nil
}

// Retract deletes the announcement and every delivery it made.
func (s *Service) Retract(id uint) error {
	users, err := s.inbox.DeleteForAnnouncement(id)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	s.pushAll(users)
	return nil
}

// PublishNow sends a scheduled announcement immediately.
func (s *Service) PublishNow(a *models.Announcement) error {
	if a.PublishedAt != nil {
		return nil
	}
	now := s.now().UTC()
	a.PublishAt = nil
	a.PublishedAt = &now
	return s.broadcast(a)
}

// SyncUser makes the audience a standing rule: users created after the broadcast are still caught up.
func (s *Service) SyncUser(userID uint) {
	now := s.now().UTC()
	if !s.claimSync(userID, now) {
		return
	}
	pending, err := s.repo.UndeliveredLive(userID, now)
	if err != nil {
		logger.Warn("announcement: backfill lookup failed", "user", userID, "error", err)
		return
	}
	delivered := false
	for i := range pending {
		a := &pending[i]
		match, err := s.repo.Matches(a, userID)
		if err != nil || !match {
			continue
		}
		n, err := s.inbox.CreateBatch([]models.Notification{s.delivery(a, userID)})
		if err != nil {
			logger.Warn("announcement: backfill delivery failed", "announcement", a.ID, "user", userID, "error", err)
			continue
		}
		if n == 0 {
			continue // already delivered by a concurrent broadcast
		}
		delivered = true
		if count, err := s.inbox.CountForAnnouncement(a.ID); err == nil {
			a.Recipients = int(count)
			_ = s.repo.Save(a)
		}
	}
	if delivered {
		s.push(userID)
	}
}

// Tick needs no expiry sweep: every read path compares expires_at against now.
func (s *Service) Tick() error {
	due, err := s.repo.DueForPublish(s.now().UTC())
	if err != nil {
		return err
	}
	for i := range due {
		a := &due[i]
		if err := s.PublishNow(a); err != nil {
			logger.Warn("announcement: scheduled publish failed", "announcement", a.ID, "error", err)
		}
	}
	return nil
}

// One row per user keeps read and dismissed state per-person.
func (s *Service) broadcast(a *models.Announcement) error {
	recipients, err := s.repo.RecipientIDs(a)
	if err != nil {
		return err
	}
	rows := make([]models.Notification, 0, len(recipients))
	for _, uid := range recipients {
		rows = append(rows, s.delivery(a, uid))
	}
	if _, err := s.inbox.CreateBatch(rows); err != nil {
		return err
	}
	count, err := s.inbox.CountForAnnouncement(a.ID)
	if err != nil {
		return err
	}
	a.Recipients = int(count)
	if err := s.repo.Save(a); err != nil {
		return err
	}
	s.clearSyncMarks(recipients)
	s.pushAll(recipients)
	a.Status = a.State(s.now().UTC())
	return nil
}

// WorkspaceID stays 0 so a platform announcement follows the reader into any workspace.
func (s *Service) delivery(a *models.Announcement, userID uint) models.Notification {
	return models.Notification{
		UserID:         userID,
		WorkspaceID:    0,
		AnnouncementID: &a.ID,
		Kind:           models.NotificationKindAnnouncement,
		Category:       "",
		Severity:       a.Severity,
		Title:          strings.TrimSpace(a.Title),
		Body:           strings.TrimSpace(a.Message),
		SubjectLink:    strings.TrimSpace(a.Link),
		ActionText:     strings.TrimSpace(a.ActionText),
		Pinned:         a.Pinned,
		Dismissal:      a.Dismissal,
		ExpiresAt:      a.ExpiresAt,
	}
}

// A fresh broadcast bypasses the limit: publish clears the marks it delivered against.
func (s *Service) claimSync(userID uint, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.lastSynced[userID]; ok && now.Sub(last) < syncInterval {
		return false
	}
	if len(s.lastSynced) > 10000 {
		for uid, t := range s.lastSynced {
			if now.Sub(t) >= syncInterval {
				delete(s.lastSynced, uid)
			}
		}
	}
	s.lastSynced[userID] = now
	return true
}

func (s *Service) clearSyncMarks(userIDs []uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, uid := range userIDs {
		delete(s.lastSynced, uid)
	}
}

// An edit may widen the audience, and the newly covered users are not knowable from existing deliveries.
func (s *Service) resetSyncMarks() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.lastSynced)
}

func (s *Service) pushAll(userIDs []uint) {
	for _, uid := range userIDs {
		s.push(uid)
	}
}

// The payload is only a refetch signal; Postgres stays the source of truth.
func (s *Service) push(userID uint) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(alerting.NotificationTopic(userID), eventbus.Event{
		Type: "notification",
		Data: map[string]any{"user_id": userID},
	})
}
