// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package wsbundle

import (
	"strings"
	"testing"
)

// The schema is a compatibility gate, and getting it wrong strands bundles.
// An older document must still restore — refusing one would make every bundle
// already taken unrestorable — and a newer one must be refused, because
// accepting it would silently drop whatever that version added (configs, say)
// and look like it worked.
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

// Schema 1 documents predate configs. They must still open, with no configs
// rather than an error.
func TestOlderDocumentWithoutConfigsIsValid(t *testing.T) {
	st := &State{Schema: 1, Workspace: Workspace{Name: "acme"}}
	if err := st.Validate(); err != nil {
		t.Fatalf("a schema-1 bundle no longer restores: %v", err)
	}
	if len(st.Configs) != 0 {
		t.Errorf("expected no configs, got %d", len(st.Configs))
	}
}

// Configs and their contents survive a seal/open round trip — they are the part
// of the bundle that only exists sealed.
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
	// The mount is what makes the config reachable; carrying one without the other
	// restores a workspace that looks right and does not run.
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
