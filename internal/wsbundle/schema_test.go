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

package wsbundle

import (
	"strings"
	"testing"
)

// Older schemas must restore, and newer ones must be refused rather than silently dropping new fields.
func TestStateSchemaCompatibilityWindow(t *testing.T) {
	cases := []struct {
		name    string
		schema  int
		wantErr bool
	}{
		{"the oldest readable document", MinStateSchema, false},
		{"what this build writes", StateSchema, false},
		{"before the window", MinStateSchema - 1, true},
		{"from a newer Miabi", StateSchema + 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &State{Schema: c.schema, Workspace: Workspace{Name: "acme"}}
			err := st.Validate()
			if c.wantErr != (err != nil) {
				t.Fatalf("schema %d: err = %v, wantErr = %v", c.schema, err, c.wantErr)
			}
			// A refusal has to say what to do about it, not just that it failed.
			if c.wantErr && c.schema > StateSchema && !strings.Contains(err.Error(), "newer Miabi") {
				t.Errorf("a future schema should tell the operator to upgrade, got %q", err)
			}
		})
	}
}

func TestOlderDocumentWithoutConfigsIsValid(t *testing.T) {
	st := &State{Schema: 1, Workspace: Workspace{Name: "acme"}}
	if err := st.Validate(); err != nil {
		t.Fatalf("a schema-1 bundle no longer restores: %v", err)
	}
	if len(st.Configs) != 0 {
		t.Errorf("expected no configs, got %d", len(st.Configs))
	}
}

func TestConfigsSurviveSealing(t *testing.T) {
	const pass = "correct-horse-battery-staple-7"
	st := &State{
		Schema:    StateSchema,
		Workspace: Workspace{Name: "acme"},
		Configs: []Config{{
			Name: "nginx", Data: map[string]string{"nginx.conf": "server { listen 80; }"},
			Mode: "0644", Sensitive: true, Delimiters: []string{"[[", "]]"},
		}},
		Apps: []Application{{
			Name:   "web",
			Mounts: []Mount{{Config: "nginx", Key: "nginx.conf", Path: "/etc/nginx/nginx.conf", Mode: "0444", ReadOnly: true}},
		}},
	}

	sealed, err := Seal(st, pass)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(sealed, pass)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Configs) != 1 {
		t.Fatalf("configs did not survive sealing: %d", len(got.Configs))
	}
	c := got.Configs[0]
	if c.Data["nginx.conf"] != "server { listen 80; }" {
		t.Errorf("config contents changed: %q", c.Data["nginx.conf"])
	}
	if !c.Sensitive || c.Mode != "0644" || len(c.Delimiters) != 2 {
		t.Errorf("config settings did not survive: %+v", c)
	}
	// Carrying a config without its mount restores a workspace that looks right and does not run.
	if len(got.Apps) != 1 || len(got.Apps[0].Mounts) != 1 {
		t.Fatalf("the app's config mount did not survive: %+v", got.Apps)
	}
	m := got.Apps[0].Mounts[0]
	if m.Config != "nginx" || m.Key != "nginx.conf" || m.Path != "/etc/nginx/nginx.conf" || m.Mode != "0444" || !m.ReadOnly {
		t.Errorf("config mount changed: %+v", m)
	}
	if m.Volume != "" {
		t.Errorf("a config mount must not carry a volume name: %q", m.Volume)
	}
}
