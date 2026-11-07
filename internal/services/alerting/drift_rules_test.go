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

func driftEvent(t models.AppEventType, kind string) *models.AppEvent {
	return &models.AppEvent{
		WorkspaceID: 1, SubjectType: models.SubjectApp, ApplicationID: 7, Type: t,
		Severity: models.SeverityWarning, Metadata: map[string]string{"kind": kind},
	}
}

// The two must not resolve each other.
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

	// The workload returning says nothing about the app's data.
	got = evaluate(driftEvent(models.EventDriftResolved, "container"), "api")
	if len(got) != 2 || got[0].kind != resolve || got[0].dedupKey != "drift:app:7" ||
		got[1].kind != resolve || got[1].dedupKey != "reconcile:app:7" {
		t.Fatalf("container recovery = %+v; want it to resolve drift:app:7 and reconcile:app:7", got)
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
