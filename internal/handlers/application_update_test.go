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

package handlers

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func runningApp() *models.Application {
	reg, stack := uint(4), uint(9)
	return &models.Application{
		Image: "ghcr.io/acme/web", Tag: "2.1.0", Port: 8080,
		MemoryBytes: 512 << 20, NanoCPUs: 1e9, RegistryID: &reg, StackID: &stack,
		RunAsUser: "1000", HealthcheckType: models.HealthcheckNone,
	}
}

func patch(t *testing.T, body string) *UpdateAppRequest {
	t.Helper()
	req := &UpdateAppRequest{}
	if err := bindJSON(t, req, body); err != nil {
		t.Fatal(err)
	}
	return req
}

// Issue #451: a PATCH with only healthcheck fields must not clear the tag or anything else unsent.
func TestPartialUpdateKeepsUnsentFields(t *testing.T) {
	req := patch(t, `{"healthcheck_type":"http","healthcheck_http_path":"/healthz","healthcheck_port":8080}`)
	app := runningApp()
	req.Body.applyTo(app)

	if app.HealthcheckType != models.HealthcheckHTTP || app.HealthcheckHTTPPath != "/healthz" || app.HealthcheckPort != 8080 {
		t.Errorf("healthcheck not applied: %+v", app)
	}
	want := runningApp()
	if app.Tag != want.Tag || app.Port != want.Port || app.MemoryBytes != want.MemoryBytes || app.NanoCPUs != want.NanoCPUs ||
		app.RunAsUser != want.RunAsUser || app.RegistryID == nil || *app.RegistryID != 4 || app.StackID == nil || *app.StackID != 9 {
		t.Errorf("unsent fields changed: %+v", app)
	}
	for _, key := range []string{"ports", "network_ids"} {
		if req.Body.sent(key) {
			t.Errorf("%s reported as sent; the handler would replace them with nothing", key)
		}
	}
}

// Sending a field as empty still clears it, so a client that sends the whole object keeps working.
func TestSentEmptyFieldClears(t *testing.T) {
	req := patch(t, `{"tag":"","registry_id":null,"memory_bytes":0}`)
	app := runningApp()
	req.Body.applyTo(app)
	if app.Tag != "" || app.RegistryID != nil || app.MemoryBytes != 0 {
		t.Errorf("explicit empties not applied: %+v", app)
	}
	if app.Port != 8080 {
		t.Error("an unsent port must be kept")
	}
}

func TestStartPeriodAppliesOnItsOwn(t *testing.T) {
	app := runningApp()
	patch(t, `{"healthcheck_start_period_seconds":20}`).Body.applyTo(app)
	if app.HealthcheckStartPeriodSeconds != 20 {
		t.Errorf("start period = %d, want 20", app.HealthcheckStartPeriodSeconds)
	}
}

func TestClearingTagChangesSource(t *testing.T) {
	app := runningApp()
	cases := map[string]bool{
		`{"healthcheck_type":"http"}`:                false,
		`{"tag":"2.1.0"}`:                            false,
		`{"image":"ghcr.io/acme/web","tag":"2.1.0"}`: false,
		`{"tag":""}`:                                 true,
		`{"tag":"2.2.0"}`:                            true,
		`{"image":"ghcr.io/acme/api"}`:               true,
	}
	for body, want := range cases {
		if got := patch(t, body).Body.changesSource(app); got != want {
			t.Errorf("%s: changesSource = %v, want %v", body, got, want)
		}
	}
}

// A body built in code rather than decoded from JSON counts every field as sent: the old behaviour.
func TestBodyNotFromJSONCountsAllSent(t *testing.T) {
	var b UpdateAppBody
	if !b.sent("tag") {
		t.Error("a body without key tracking must treat every key as sent")
	}
}
