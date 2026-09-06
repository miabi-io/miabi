// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package alerting

import (
	"fmt"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

type intentKind int

const (
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

func appSubject(appID uint) (typ, ref, link string) {
	return "app", fmt.Sprintf("app:%d", appID), fmt.Sprintf("/apps/%d", appID)
}

func databaseSubject(dbID uint) (typ, ref, link string) {
	return "database", fmt.Sprintf("database:%d", dbID), fmt.Sprintf("/databases/%d", dbID)
}

// evaluate maps one event to zero or more alert intents, dispatching on its subject. name is the
// resolved display name of that subject. It is pure: no I/O, no state — the engine handles
// counting, persistence and fan-out.
func evaluate(e *models.AppEvent, name string) []intent {
	if subject, id := e.Subject(); subject == models.SubjectDatabase {
		return evaluateDatabase(e, id, name)
	}
	return evaluateApp(e, name)
}

// evaluateDatabase maps a database instance event to alert intents. Backup outcomes are
// deliberately absent: the backup service already raises those through the Signal path
// ("backup_failed"/"backup_ok"), and firing here as well would open a second alert for one
// failure.
func evaluateDatabase(e *models.AppEvent, dbID uint, name string) []intent {
	if dbID == 0 {
		return nil
	}
	typ, ref, link := databaseSubject(dbID)
	base := intent{
		category:    models.CategoryDatabase,
		subjectType: typ,
		subjectRef:  ref,
		subjectLink: link,
		minRole:     models.WorkspaceRoleDeveloper,
	}
	crashKey := fmt.Sprintf("crashloop:database:%d", dbID)
	oomKey := fmt.Sprintf("oom:database:%d", dbID)
	unhealthyKey := fmt.Sprintf("unhealthy:database:%d", dbID)
	provisionKey := fmt.Sprintf("provision:database:%d", dbID)
	upgradeKey := fmt.Sprintf("upgrade:database:%d", dbID)

	switch e.Type {
	case models.EventContainerOOM:
		i := base
		i.kind, i.ruleKey, i.dedupKey = fire, "database_oom", oomKey
		i.severity = models.AlertCritical
		i.title = fmt.Sprintf("Out of memory — %s", name)
		i.body = orDefault(e.Message, "The database container was OOM-killed. Consider raising its memory limit.")
		return []intent{i}

	case models.EventContainerDied:
		i := base
		i.kind, i.ruleKey, i.dedupKey = countFire, "database_crash_loop", crashKey
		i.severity = models.AlertCritical
		i.threshold, i.window = 5, 3*time.Minute
		i.title = fmt.Sprintf("Crash-looping — %s", name)
		i.body = orDefault(e.Message, "The database container keeps exiting and restarting.")
		return []intent{i}

	case models.EventContainerHealth:
		if e.Severity == models.SeverityWarning {
			i := base
			i.kind, i.ruleKey, i.dedupKey = fire, "database_unhealthy", unhealthyKey
			i.severity = models.AlertWarning
			i.title = fmt.Sprintf("Unhealthy — %s", name)
			i.body = orDefault(e.Message, "The database container's health check is failing.")
			return []intent{i}
		}
		return resolves(unhealthyKey, crashKey, oomKey)

	case models.EventDatabaseProvisionFailed:
		i := base
		i.kind, i.ruleKey, i.dedupKey = fire, "database_provision_failed", provisionKey
		i.severity = models.AlertCritical
		i.title = fmt.Sprintf("Provisioning failed — %s", name)
		i.body = orDefault(e.Message, "The database instance did not finish provisioning.")
		return []intent{i}

	case models.EventDatabaseProvisioned:
		return resolves(provisionKey)

	case models.EventDatabaseUpgradeFailed:
		i := base
		i.kind, i.ruleKey, i.dedupKey = fire, "database_upgrade_failed", upgradeKey
		i.severity = models.AlertCritical
		i.title = fmt.Sprintf("Upgrade failed — %s", name)
		i.body = orDefault(e.Message, "The database upgrade did not complete.")
		return []intent{i}

	case models.EventDatabaseUpgraded:
		return resolves(upgradeKey)

	case models.EventDatabaseStarted:
		return resolves(crashKey, oomKey, unhealthyKey)
	}
	return nil
}

// evaluateApp maps one application event to zero or more alert intents. appName is the
// resolved display name (falls back to "app #<id>" upstream).
func evaluateApp(e *models.AppEvent, appName string) []intent {
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
