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
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func dbEvent(t models.AppEventType, sev models.AppEventSeverity) *models.AppEvent {
	return &models.AppEvent{
		WorkspaceID: 1, SubjectType: models.SubjectDatabase, DatabaseID: 12,
		Type: t, Severity: sev,
	}
}

// The app rules would drop a database event outright since its ApplicationID is 0.
func TestEvaluateDispatchesOnSubject(t *testing.T) {
	got := evaluate(dbEvent(models.EventContainerOOM, models.SeverityError), "orders-db")
	if len(got) != 1 {
		t.Fatalf("got %d intents, want 1", len(got))
	}
	if got[0].subjectRef != "database:12" || got[0].subjectLink != "/databases/12" {
		t.Errorf("subject = %q/%q, want database:12 and /databases/12", got[0].subjectRef, got[0].subjectLink)
	}
	if got[0].category != models.CategoryDatabase || got[0].severity != models.AlertCritical {
		t.Errorf("category/severity = %v/%v, want database/critical", got[0].category, got[0].severity)
	}
	if got[0].dedupKey == "oom:app:12" {
		t.Error("database OOM reused the app dedup key: an app and a database sharing an id would fold into one alert")
	}
}

// The backup service already reports outcomes via Signal, so event rules must stay silent to avoid double alerts.
func TestDatabaseBackupOutcomesRaiseNoIntent(t *testing.T) {
	for _, typ := range []models.AppEventType{
		models.EventDatabaseBackupFailed,
		models.EventDatabaseBackupSucceeded,
		models.EventDatabaseRestoreFailed,
	} {
		if got := evaluate(dbEvent(typ, models.SeverityError), "orders-db"); len(got) != 0 {
			t.Errorf("%s produced %d intents, want 0 (Signal path already alerts)", typ, len(got))
		}
	}
}

func TestDatabaseRecoveryResolves(t *testing.T) {
	cases := []struct {
		typ  models.AppEventType
		sev  models.AppEventSeverity
		want int
	}{
		{models.EventDatabaseProvisioned, models.SeverityInfo, 1},
		{models.EventDatabaseUpgraded, models.SeverityInfo, 1},
		{models.EventDatabaseStarted, models.SeverityInfo, 3},
		{models.EventContainerHealth, models.SeverityInfo, 3},
	}
	for _, c := range cases {
		got := evaluate(dbEvent(c.typ, c.sev), "orders-db")
		if len(got) != c.want {
			t.Errorf("%s produced %d resolves, want %d", c.typ, len(got), c.want)
		}
		for _, in := range got {
			if in.kind != resolve {
				t.Errorf("%s produced a non-resolve intent", c.typ)
			}
		}
	}
}

// Rows predating SubjectType must keep alerting as application events.
func TestAppEventsUnaffected(t *testing.T) {
	e := &models.AppEvent{WorkspaceID: 1, ApplicationID: 5, Type: models.EventContainerOOM, Severity: models.SeverityError}
	got := evaluate(e, "web")
	if len(got) != 1 || got[0].subjectRef != "app:5" {
		t.Fatalf("legacy app event did not evaluate as an app event: %+v", got)
	}
}
