// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package announcement broadcasts platform notices into per-user inboxes. It
// shares the alerting engine's delivery surface: an announcement becomes ordinary
// models.Notification rows, so the bell, the SSE stream and the notifications page
// need no announcement-specific code.
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

// Service owns an announcement's lifecycle: broadcast, edit, retract, and the
// scheduled publish.
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

// syncInterval bounds how often one user's backfill re-queries. A page load hits
// the inbox three times (list, badge, banners) and the answer cannot change in
// between, so without it the read path triples its work to re-discover nothing.
const syncInterval = time.Minute

// Create persists the announcement and broadcasts it, unless it is scheduled for
// later — in which case Tick publishes it when its time arrives.
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

// Update rewrites the announcement and every delivery it has made, so a correction
// replaces the original notice instead of arriving beside it. A widened audience is
// delivered by the next backfill, which is the one place membership is decided.
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

// SyncUser delivers any live announcement the user is entitled to but has not
// received, which makes the audience a standing rule rather than a snapshot: a
// user created after the broadcast is still caught up when they next open the app.
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

// Tick publishes announcements whose scheduled time has arrived. Expiry needs no
// sweep: every read path compares expires_at against now, so a lapsed notice stops
// showing the moment it lapses rather than when a job next runs.
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

// broadcast resolves the audience and writes one delivery per recipient. A row per
// user rather than one shared row keeps read and dismissed state per-person, which
// is the whole point of an inbox.
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

// delivery renders the announcement as one inbox row. WorkspaceID stays 0: an
// announcement belongs to the platform, so it follows the reader into whichever
// workspace they have open rather than hiding behind a workspace filter.
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

// claimSync rate-limits one user's backfill and reports whether the caller owns
// this round. A fresh broadcast bypasses it: publish clears the marks it delivered
// against, so an announcement is never delayed by an interval it did not exist for.
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

// clearSyncMarks drops the backfill rate-limit for the given users, so a broadcast
// that just landed is picked up on their very next read rather than up to a minute
// later.
func (s *Service) clearSyncMarks(userIDs []uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, uid := range userIDs {
		delete(s.lastSynced, uid)
	}
}

// resetSyncMarks clears every backfill rate-limit. An edit may have widened the
// audience, and the users it newly covers are not knowable from the deliveries
// that already exist.
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

// push nudges the user's SSE stream that their inbox changed. The payload is a
// signal to refetch, keeping the wire small and Postgres the source of truth —
// the same contract the alerting engine uses.
func (s *Service) push(userID uint) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(alerting.NotificationTopic(userID), eventbus.Event{
		Type: "notification",
		Data: map[string]any{"user_id": userID},
	})
}
