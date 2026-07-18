// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// notifyCooldown bounds how often a single condition may (re)notify, so a
// re-firing alert can't ping the bell repeatedly.
// const notifyCooldown = 10 * time.Minute

// NotificationTopic is the per-user eventbus topic the bell's SSE stream
// subscribes to.
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

// SystemAdminLister returns the user ids of the platform super-admins — the
// recipients of platform-scoped alerts (node offline, engine too old, license),
// which are attributed to the system workspace rather than any tenant workspace.
type SystemAdminLister interface {
	ListAdminIDs() ([]uint, error)
}

type AppNamer interface {
	AppName(id uint) string
}

// AppNameFunc adapts a plain function to the AppNamer interface.
type AppNameFunc func(id uint) string

func (f AppNameFunc) AppName(id uint) string { return f(id) }

type Publisher interface {
	Publish(topic string, e eventbus.Event)
}

// Engine turns app-event signals into alerts and per-user notifications. It
// implements the events service's alert-sink hook (OnEvent). Fan-out is
// workspace + role scoped; hot state (crash-loop windows, notify cooldowns) is in
// the Counter (Redis in production).
type Engine struct {
	alerts  AlertStore
	inbox   InboxStore
	members MemberLister
	namer   AppNamer
	bus     Publisher
	counter Counter
	// Optional scan sources; each enables a category of the periodic scanner.
	certs   CertLister
	volumes VolumeUsageLister
	quota   QuotaLister
	// Platform-scoped alerts (category Node, …) are emitted on the system
	// workspace and fan out to the platform super-admins instead of workspace
	// members.
	sysAdmins SystemAdminLister
	now       func() time.Time
}

// SetSystemAdmins enables platform-scoped alert delivery: platform alerts fan out
// to the super-admins this lister returns.
func (e *Engine) SetSystemAdmins(admins SystemAdminLister) { e.sysAdmins = admins }

func NewEngine(alerts AlertStore, inbox InboxStore, members MemberLister, namer AppNamer, bus Publisher, counter Counter) *Engine {
	return &Engine{alerts: alerts, inbox: inbox, members: members, namer: namer, bus: bus, counter: counter, now: time.Now}
}

// OnEvent is the alert-sink hook: it receives every recorded app event (unlike
// the outbound notifier, which is filtered to the notifiable set), evaluates the
// rules, and executes the resulting intents. Best-effort — a failure is logged
// and never propagates to the recording caller.
func (e *Engine) OnEvent(ev *models.AppEvent) {
	if ev == nil || ev.WorkspaceID == 0 {
		return
	}
	appName := ev.ApplicationName
	if appName == "" && e.namer != nil {
		appName = e.namer.AppName(ev.ApplicationID)
	}
	if appName == "" {
		appName = fmt.Sprintf("app #%d", ev.ApplicationID)
	}
	ctx := context.Background()
	for _, in := range evaluate(ev, appName) {
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

// doFire opens or folds an alert and, only when it is newly opened, fans a
// notification out to the workspace's eligible members. A folded repeat (e.g. the
// 6th crash in a loop) just bumps the alert's count — it never re-notifies, which
// is the storm control that keeps 40 events at one bell item.
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

// doResolve closes the active alert for a dedup key and rewrites its delivered
// notifications to "recovered" (re-surfacing them as unread), then pushes the
// change over SSE. A no-op when nothing was open.
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

// fanOut writes one notification per recipient and pushes each over SSE.
// Recipients are the eligible workspace members (role ≥ the alert's MinRole), or —
// for a platform alert on the system workspace — the platform super-admins. The
// workspace + role boundary lives here: a user only receives an alert for a
// workspace they're in (or, for platform alerts, only if they're a super-admin).
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

// recipients resolves who should receive an alert: the system admins for a
// platform alert on the system workspace, otherwise the workspace members whose
// role meets the alert's minimum.
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
		if m.Role.AtLeast(minRole) {
			out = append(out, m.UserID)
		}
	}
	return out, nil
}

// push notifies the user's SSE stream that their inbox changed. The payload is a
// signal to refetch (count + list), keeping the wire small and the source of
// truth in Postgres.
func (e *Engine) push(userID uint) {
	if e.bus == nil {
		return
	}
	e.bus.Publish(NotificationTopic(userID), eventbus.Event{Type: "notification", Data: map[string]any{"user_id": userID}})
}

// subjectName strips a leading "<condition> — " prefix from an alert title to
// reuse the subject (the app name) in the recovered message.
func subjectName(title string) string {
	const sep = " — "
	if i := strings.Index(title, sep); i >= 0 {
		return title[i+len(sep):]
	}
	return title
}
