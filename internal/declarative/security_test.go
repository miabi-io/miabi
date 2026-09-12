// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative_test

import (
	"reflect"
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
)

func parseApp(t *testing.T, spec string) *d.ApplicationSpec {
	t.Helper()
	set, err := d.Parse(appManifest(spec))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := set.Get("Application/web")
	return r.Application
}

func TestSecurityBlockParses(t *testing.T) {
	a := parseApp(t, "  security:\n    runAsUser: \"1000:1000\"\n    readOnlyRootFilesystem: true\n    noNewPrivileges: true\n"+
		"    capabilities:\n      add: [NET_BIND_SERVICE]\n      drop: [ALL]\n")
	if a.RunAsUser() != "1000:1000" || !a.ReadOnlyRootFilesystem() || !a.NoNewPrivileges() ||
		!reflect.DeepEqual(a.AddCapabilities(), []string{"NET_BIND_SERVICE"}) || !reflect.DeepEqual(a.DropCapabilities(), []string{"ALL"}) {
		t.Errorf("security = %+v", a.Security)
	}
}

// Manifests written before the fields were grouped keep applying.
func TestDeprecatedSpellingsStillParse(t *testing.T) {
	a := parseApp(t, "  runAsUser: node\n  strategy: canary\n  security:\n    addCapabilities: [NET_ADMIN]\n")
	if a.RunAsUser() != "node" || a.Strategy() != "canary" || !reflect.DeepEqual(a.AddCapabilities(), []string{"NET_ADMIN"}) {
		t.Errorf("spec = %+v, security = %+v", a, a.Security)
	}
	if a.DeprecatedRunAsUser != "" || a.DeprecatedStrategy != "" || a.Security.DeprecatedAddCapabilities != nil {
		t.Error("an old spelling was left set after it moved into its block")
	}
}

func TestInvalidSecurityIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, spec, want string }{
		{"strategy twice", "  strategy: canary\n  deployment:\n    strategy: rolling\n", "old spelling of deployment.strategy"},
		{"runAsUser twice", "  runAsUser: node\n  security:\n    runAsUser: \"1000\"\n", "old spelling of security.runAsUser"},
		{"capabilities twice", "  security:\n    addCapabilities: [NET_ADMIN]\n    capabilities:\n      add: [NET_RAW]\n", "old spelling of security.capabilities.add"},
		{"unknown drop", "  security:\n    capabilities:\n      drop: [NOT_A_CAP]\n", "not a Linux capability"},
		{"added and dropped", "  security:\n    capabilities:\n      add: [NET_ADMIN]\n      drop: [CAP_NET_ADMIN]\n", "both added and dropped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Parse(appManifest(tc.spec))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Hardening is the manifest's to state: removing it from the file converges, like a revoked grant.
func TestRemovedHardeningConverges(t *testing.T) {
	on := true
	live := d.ApplicationSpec{Security: &d.SecuritySpec{
		ReadOnlyRootFilesystem: true, NoNewPrivileges: &on, Capabilities: &d.CapabilitiesSpec{Drop: []string{"ALL"}},
	}}
	got := map[string]bool{}
	for _, f := range appFields(d.ApplicationSpec{}, live) {
		got[f.Field] = true
	}
	for _, want := range []string{"security.readOnlyRootFilesystem", "security.noNewPrivileges", "security.capabilities.drop"} {
		if !got[want] {
			t.Errorf("removing %s planned no change (fields %v)", want, got)
		}
	}
}

func TestCapabilitySpellingIsNotDrift(t *testing.T) {
	desired := d.ApplicationSpec{Security: &d.SecuritySpec{Capabilities: &d.CapabilitiesSpec{Add: []string{"cap_net_admin"}}}}
	live := d.ApplicationSpec{Security: &d.SecuritySpec{Capabilities: &d.CapabilitiesSpec{Add: []string{"NET_ADMIN"}}}}
	if fields := appFields(desired, live); len(fields) != 0 {
		t.Errorf("fields = %+v, want no drift", fields)
	}
}

func dbManifest(spec string) []byte {
	return []byte("apiVersion: miabi.io/v1\nkind: Database\nmetadata:\n  name: pg\nspec:\n  engine: postgres\n" + spec)
}

func TestDatabaseInstanceAndPlacement(t *testing.T) {
	for _, tc := range []struct{ name, spec, instance, location string }{
		{"new spelling", "  instance: dedicated\n  placement:\n    location: eu\n", "dedicated", "eu"},
		{"old placement string", "  placement: shared\n", "shared", ""},
		{"default", "", "auto", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, err := d.Parse(dbManifest(tc.spec))
			if err != nil {
				t.Fatal(err)
			}
			r, _ := set.Get("Database/pg")
			if r.Database.Instance != tc.instance || r.Database.Location() != tc.location {
				t.Errorf("instance = %q, location = %q", r.Database.Instance, r.Database.Location())
			}
		})
	}
}

// GitOps repositories written before instance existed nest databases in a Project; they must keep applying.
func TestProjectDatabaseOldPlacementStillApplies(t *testing.T) {
	set, err := d.Parse([]byte(`apiVersion: miabi.io/v1
kind: Project
metadata: { name: posta }
spec:
  resources:
    - apiVersion: miabi.io/v1
      kind: Database
      metadata: { name: posta-db }
      spec: { engine: postgres, version: "17-alpine", placement: auto }
    - apiVersion: miabi.io/v1
      kind: Database
      metadata: { name: posta-redis }
      spec: { engine: redis, version: "8-alpine", placement: dedicated }
`))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"posta-db": "auto", "posta-redis": "dedicated"} {
		r, ok := set.Get("Database/" + name)
		if !ok || r.Database.Instance != want || r.Database.Placement != nil {
			t.Errorf("%s: database = %+v, want instance %q", name, r.Database, want)
		}
	}
}

func TestDatabasePlacementIsStrict(t *testing.T) {
	if _, err := d.Parse(dbManifest("  placement: dedicated\n  instance: shared\n")); err == nil || !strings.Contains(err.Error(), "old spelling of instance") {
		t.Errorf("err = %v, want both spellings refused", err)
	}
	if _, err := d.Parse(dbManifest("  placement:\n    zone: a\n")); err == nil {
		t.Error("an unknown placement field was accepted")
	}
}
