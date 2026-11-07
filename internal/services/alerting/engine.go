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

package alerting

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/eventbus"
)

func NotificationTopic(userID uint) string { return fmt.Sprintf("notifications:user:%d", userID) }

// Dependencies (interfaces so the engine is unit-testable; the repos satisfy them).
type AlertStore interface {
	Fire(a *models.Alert) (*models.Alert, bool, error)
	Resolve(workspaceID uint, dedupKey string, at time.Time) (*models.Alert, error)
	ListActiveByCategory(category models.AlertCategory) ([]models.Alert, error)
}

type InboxStore interface {
	Upsert(n *models.Notification, resurface bool) error
	ApplyAlertUpdate(alertID uint, tmpl models.Notification, resurface bool) ([]uint, error)
}

type MemberLister interface {
	ListMembers(workspaceID uint) ([]models.WorkspaceMember, error)
}

// Platform-scoped alerts belong to the system workspace, not any tenant workspace.
type SystemAdminLister interface {
	ListAdminIDs() ([]uint, error)
}

type AppNamer interface {
	AppName(id uint) string
}

// AppNameFunc adapts a plain function to the AppNamer interface.
type AppNameFunc func(id uint) string

func (f AppNameFunc) AppName(id uint) string { return f(id) }

// Optional: unwired falls back to "database #<id>".
type DatabaseNamer interface {
	DatabaseName(id uint) string
}

// DatabaseNameFunc adapts a plain function to the DatabaseNamer interface.
type DatabaseNameFunc func(id uint) string

func (f DatabaseNameFunc) DatabaseName(id uint) string { return f(id) }

type Publisher interface {
	Publish(topic string, e eventbus.Event)
}

// Hot state (crash-loop windows, notify cooldowns) lives in the Counter, Redis in production.
type Engine struct {
	alerts  AlertStore
	inbox   InboxStore
	members MemberLister
	namer   AppNamer
	dbNamer DatabaseNamer
	bus     Publisher
	counter Counter
	// Optional scan sources; each enables a category of the periodic scanner.
	certs        CertLister
	volumes      VolumeUsageLister
	quota        QuotaLister
	runners      RunnerLister
	sysWorkspace func() uint
	sysAdmins    SystemAdminLister
	now          func() time.Time
}

func (e *Engine) SetSystemAdmins(admins SystemAdminLister) { e.sysAdmins = admins }

func NewEngine(alerts AlertStore, inbox InboxStore, members MemberLister, namer AppNamer, bus Publisher, counter Counter) *Engine {
	return &Engine{alerts: alerts, inbox: inbox, members: members, namer: namer, bus: bus, counter: counter, now: time.Now}
}

// Receives every event, unlike the filtered outbound notifier; best-effort, never fails the recording caller.
func (e *Engine) OnEvent(ev *models.AppEvent) {
	if ev == nil || ev.WorkspaceID == 0 {
		return
	}
	ctx := context.Background()
	for _, in := range evaluate(ev, e.subjectName(ev)) {
		switch in.kind {
		case fire:
			e.doFire(ctx, ev.WorkspaceID, in)
		case countFire:
			n, err := e.counter.Incr(ctx, in.dedupKey, in.window)
			if err != nil {
				logger.Warn("alerting: counter incr failed", "key", in.dedupKey, "error", err)
				continue
			}
			if n < int64(in.threshold) {
				continue // not yet a crash-loop
			}
			e.doFire(ctx, ev.WorkspaceID, in)
		case resolve:
			e.doResolve(ctx, ev.WorkspaceID, in.dedupKey)
		}
	}
}

func (e *Engine) subjectName(ev *models.AppEvent) string {
	subject, id := ev.Subject()
	if subject == models.SubjectDatabase {
		if ev.DatabaseName != "" {
			return ev.DatabaseName
		}
		if e.dbNamer != nil {
			if n := e.dbNamer.DatabaseName(id); n != "" {
				return n
			}
		}
		return fmt.Sprintf("database #%d", id)
	}
	if ev.ApplicationName != "" {
		return ev.ApplicationName
	}
	if e.namer != nil {
		if n := e.namer.AppName(id); n != "" {
			return n
		}
	}
	return fmt.Sprintf("app #%d", id)
}

// SetDatabaseNamer wires the database display-name resolver (optional).
func (e *Engine) SetDatabaseNamer(n DatabaseNamer) { e.dbNamer = n }

// Folded repeats only bump the count and never re-notify: the storm control keeping 40 events at one bell item.
func (e *Engine) doFire(ctx context.Context, workspaceID uint, in intent) {
	now := e.now().UTC()
	a := &models.Alert{
		WorkspaceID: workspaceID, RuleKey: in.ruleKey, Category: in.category,
		Severity: in.severity, State: models.AlertFiring, DedupKey: in.dedupKey,
		SubjectType: in.subjectType, SubjectRef: in.subjectRef, SubjectLink: in.subjectLink,
		MinRole: in.minRole, Title: in.title, Body: in.body, LastSeen: now,
	}
	alert, created, err := e.alerts.Fire(a)
	if err != nil {
		logger.Warn("alerting: fire failed", "key", in.dedupKey, "error", err)
		return
	}
	if !created {
		return // folded into the existing alert; count bumped, no re-notify
	}
	e.fanOut(workspaceID, alert, true, in.platform)
}

// A no-op when nothing was open.
func (e *Engine) doResolve(ctx context.Context, workspaceID uint, dedupKey string) {
	alert, err := e.alerts.Resolve(workspaceID, dedupKey, e.now().UTC())
	if err != nil {
		return // ErrRecordNotFound = nothing to resolve; other errors are best-effort
	}
	_ = e.counter.Reset(ctx, dedupKey)

	tmpl := models.Notification{
		Kind: "info", Severity: models.AlertInfo, SubjectLink: alert.SubjectLink,
		Title: "Recovered — " + subjectName(alert.Title), Body: "The condition has cleared.",
	}
	users, err := e.inbox.ApplyAlertUpdate(alert.ID, tmpl, true)
	if err != nil {
		logger.Warn("alerting: resolve fan-out failed", "alert", alert.ID, "error", err)
		return
	}
	for _, uid := range users {
		e.push(uid)
	}
}

// A user only ever receives an alert for a workspace they are in, at or above the alert's MinRole.
func (e *Engine) fanOut(workspaceID uint, alert *models.Alert, resurface, platform bool) {
	recipients, err := e.recipients(workspaceID, alert.MinRole, platform)
	if err != nil {
		logger.Warn("alerting: resolve recipients failed", "workspace", workspaceID, "error", err)
		return
	}
	for _, uid := range recipients {
		n := &models.Notification{
			UserID: uid, WorkspaceID: workspaceID, AlertID: &alert.ID,
			Kind: "alert", Category: alert.Category, Severity: alert.Severity,
			Title: alert.Title, Body: alert.Body, SubjectLink: alert.SubjectLink,
		}
		if err := e.inbox.Upsert(n, resurface); err != nil {
			logger.Warn("alerting: notification upsert failed", "user", uid, "error", err)
			continue
		}
		e.push(uid)
	}
}

// Service accounts are members but read no inbox.
func (e *Engine) recipients(workspaceID uint, minRole models.WorkspaceRole, platform bool) ([]uint, error) {
	if e.sysAdmins != nil && platform {
		return e.sysAdmins.ListAdminIDs()
	}
	members, err := e.members.ListMembers(workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]uint, 0, len(members))
	for _, m := range members {
		if m.Role.AtLeast(minRole) && !m.User.IsService() {
			out = append(out, m.UserID)
		}
	}
	return out, nil
}

// The payload is only a refetch signal, keeping the wire small and Postgres the source of truth.
func (e *Engine) push(userID uint) {
	if e.bus == nil {
		return
	}
	e.bus.Publish(NotificationTopic(userID), eventbus.Event{Type: "notification", Data: map[string]any{"user_id": userID}})
}

func subjectName(title string) string {
	const sep = " — "
	if i := strings.Index(title, sep); i >= 0 {
		return title[i+len(sep):]
	}
	return title
}
