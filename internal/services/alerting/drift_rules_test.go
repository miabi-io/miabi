// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package alerting

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func driftEvent(t models.AppEventType, kind string) *models.AppEvent {
	return &models.AppEvent{
		WorkspaceID: 1, SubjectType: models.SubjectApp, ApplicationID: 7, Type: t,
		Severity: models.SeverityWarning, Metadata: map[string]string{"kind": kind},
	}
}

// A missing container is a warning about availability; a missing data volume is a critical storage
// condition, and the two must not resolve each other.
func TestDriftRulesSeparateWorkloadFromData(t *testing.T) {
	got := evaluate(driftEvent(models.EventDriftDetected, "container"), "api")
	if len(got) != 1 || got[0].ruleKey != "workload_missing" || got[0].dedupKey != "drift:app:7" ||
		got[0].severity != models.AlertWarning || got[0].category != models.CategoryRuntime {
		t.Fatalf("container drift = %+v; want a runtime warning keyed drift:app:7", got)
	}

	got = evaluate(driftEvent(models.EventDriftDetected, "volume"), "api")
	if len(got) != 1 || got[0].ruleKey != "data_volume_lost" || got[0].dedupKey != "datavolume:app:7" ||
		got[0].severity != models.AlertCritical || got[0].category != models.CategoryStorage {
		t.Fatalf("volume drift = %+v; want a critical storage alert keyed datavolume:app:7", got)
	}

	got = evaluate(driftEvent(models.EventDriftResolved, "container"), "api")
	if len(got) != 1 || got[0].kind != resolve || got[0].dedupKey != "drift:app:7" {
		t.Fatalf("container recovery = %+v; want it to resolve drift:app:7 only", got)
	}

	got = evaluate(driftEvent(models.EventDriftResolved, "volume"), "api")
	if len(got) != 1 || got[0].kind != resolve || got[0].dedupKey != "datavolume:app:7" {
		t.Fatalf("volume recovery = %+v; want it to resolve datavolume:app:7 only", got)
	}
}

func TestDatabaseDriftRaisesAStorageAlert(t *testing.T) {
	e := &models.AppEvent{
		WorkspaceID: 1, SubjectType: models.SubjectDatabase, DatabaseID: 5,
		Type: models.EventDriftDetected, Severity: models.SeverityError,
		Metadata: map[string]string{"kind": "volume"},
	}
	got := evaluate(e, "main")
	if len(got) != 1 || got[0].ruleKey != "data_volume_lost" || got[0].dedupKey != "datavolume:database:5" ||
		got[0].severity != models.AlertCritical || got[0].category != models.CategoryStorage {
		t.Fatalf("database drift = %+v; want a critical storage alert keyed datavolume:database:5", got)
	}

	e.Type = models.EventDriftResolved
	got = evaluate(e, "main")
	if len(got) != 1 || got[0].kind != resolve || got[0].dedupKey != "datavolume:database:5" {
		t.Fatalf("database recovery = %+v; want it to resolve datavolume:database:5", got)
	}
}
