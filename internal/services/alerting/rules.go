// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package alerting turns the platform's signal stream (app events today; metrics,
// jobs, cert/DNS, nodes later) into a small set of deduplicated, lifecycle-managed
// alerts, and fans them out to per-user notifications. The rule catalog here is
// pure and unit-tested; the engine (engine.go) adds Redis-backed counters/cooldowns
// and persistence. See plans/alerts-and-notifications.md.
package alerting

import (
	"fmt"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

// intentKind is what a rule wants done with an alert condition.
type intentKind int

const (
	// fire opens/refreshes an alert immediately.
	fire intentKind = iota
	// countFire opens/refreshes an alert only once a windowed signal count crosses
	// Threshold (the crash-loop dedup — N dies in a window is one alert, not N).
	countFire
	// resolve closes the active alert for DedupKey, if any (auto-resolve).
	resolve
)

// intent is one rule outcome for a signal. Pure data — the engine executes it.
type intent struct {
	kind        intentKind
	ruleKey     string
	dedupKey    string
	category    models.AlertCategory
	severity    models.AlertSeverity
	subjectType string
	subjectRef  string
	subjectLink string
	minRole     models.WorkspaceRole
	title       string
	body        string
	// platform routes fan-out to the super-admins (attributed to the system
	// workspace) instead of workspace members — for platform-scoped conditions
	// (node offline, shared-runner offline).
	platform bool

	// countFire only.
	threshold int
	window    time.Duration
}

// appSubject builds the subject fields for an application-scoped alert.
func appSubject(appID uint) (typ, ref, link string) {
	return "app", fmt.Sprintf("app:%d", appID), fmt.Sprintf("/apps/%d", appID)
}

// Evaluate maps one app event to zero or more alert intents. appName is the
// resolved display name (falls back to "app #<id>" upstream). It is pure: no I/O,
// no state — the engine handles counting, persistence and fan-out.
func evaluate(e *models.AppEvent, appName string) []intent {
	if e.ApplicationID == 0 {
		return nil
	}
	typ, ref, link := appSubject(e.ApplicationID)
	base := intent{
		category:    models.CategoryRuntime,
		subjectType: typ,
		subjectRef:  ref,
		subjectLink: link,
		minRole:     models.WorkspaceRoleDeveloper,
	}
	deployKey := fmt.Sprintf("deploy:app:%d", e.ApplicationID)
	crashKey := fmt.Sprintf("crashloop:app:%d", e.ApplicationID)
	oomKey := fmt.Sprintf("oom:app:%d", e.ApplicationID)
	unhealthyKey := fmt.Sprintf("unhealthy:app:%d", e.ApplicationID)

	switch e.Type {
	case models.EventDeployFailed:
		i := base
		i.kind, i.ruleKey, i.dedupKey = fire, "deploy_failed", deployKey
		i.category, i.severity = models.CategoryDeploy, models.AlertCritical
		i.title = fmt.Sprintf("Deploy failed — %s", appName)
		i.body = orDefault(e.Message, "The latest deploy did not complete.")
		return []intent{i}

	case models.EventDeploySucceeded:
		// A good deploy clears the prior deploy failure and any runtime condition.
		return resolves(deployKey, crashKey, oomKey)

	case models.EventContainerOOM:
		i := base
		i.kind, i.ruleKey, i.dedupKey = fire, "app_oom", oomKey
		i.severity = models.AlertCritical
		i.title = fmt.Sprintf("Out of memory — %s", appName)
		i.body = orDefault(e.Message, "The container was OOM-killed. Consider raising its memory limit.")
		return []intent{i}

	case models.EventContainerDied:
		i := base
		i.kind, i.ruleKey, i.dedupKey = countFire, "crash_loop", crashKey
		i.severity = models.AlertCritical
		i.threshold, i.window = 5, 3*time.Minute
		i.title = fmt.Sprintf("Crash-looping — %s", appName)
		i.body = orDefault(e.Message, "The container keeps exiting and restarting.")
		return []intent{i}

	case models.EventContainerHealth:
		if e.Severity == models.SeverityWarning { // "Container is unhealthy"
			i := base
			i.kind, i.ruleKey, i.dedupKey = fire, "app_unhealthy", unhealthyKey
			i.severity = models.AlertWarning
			i.title = fmt.Sprintf("Unhealthy — %s", appName)
			i.body = orDefault(e.Message, "The container's health check is failing.")
			return []intent{i}
		}
		// Healthy again → resolve unhealthy + any crash-loop / OOM condition.
		return resolves(unhealthyKey, crashKey, oomKey)
	}
	return nil
}

// resolves builds resolve intents for the given dedup keys.
func resolves(keys ...string) []intent {
	out := make([]intent, 0, len(keys))
	for _, k := range keys {
		out = append(out, intent{kind: resolve, dedupKey: k})
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
