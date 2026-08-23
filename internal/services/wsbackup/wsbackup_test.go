// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package wsbackup

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
)

// Each subject writes under its own path, and pointing one at a path must never
// mutate the shared config the next one reads — an aliased S3 config is how
// volume archives end up in the database prefix.
func TestWithPathDoesNotMutateTheSharedConfig(t *testing.T) {
	cfg := &backup.S3Config{Bucket: "acme", Path: "original"}
	dbs := withPath(cfg, "bundles/ref/databases")
	vols := withPath(cfg, "bundles/ref/volumes")

	if cfg.Path != "original" {
		t.Fatalf("the shared config was mutated: %q", cfg.Path)
	}
	if dbs.Path != "bundles/ref/databases" || vols.Path != "bundles/ref/volumes" {
		t.Fatalf("paths crossed: %q / %q", dbs.Path, vols.Path)
	}
	if dbs.Bucket != "acme" || vols.Bucket != "acme" {
		t.Fatal("withPath dropped the bucket")
	}
}

// Two helper runs must not collide on a container name, or a retry fails on the
// corpse of its predecessor rather than on the work.
func TestOneShotNamesAreUnique(t *testing.T) {
	a := oneShotName("mb-wsb-volbkup", 7)
	b := oneShotName("mb-wsb-volbkup", 7)
	if a == b {
		t.Fatalf("two helper runs share a name: %q", a)
	}
	if !strings.HasPrefix(a, "mb-wsb-volbkup-7-") {
		t.Fatalf("name lost its subject: %q", a)
	}
}

// A run that could not carry everything must report it: the report is what an
// operator reads to learn a database did not come back.
func TestReportFailuresAreVisible(t *testing.T) {
	var r models.BundleReport
	r.Add("app", "api", "created", "")
	r.Add("database", "pg/orders", "failed", "instance is not running")
	r.Add("secret", "stripe-key", "skipped", "already exists")
	r.Note("Point DNS at this platform.")

	fails := r.Failures()
	if len(fails) != 1 || fails[0].Name != "pg/orders" {
		t.Fatalf("Failures() = %+v", fails)
	}
	if len(r.Items) != 3 || len(r.Notes) != 1 {
		t.Fatalf("report lost entries: %+v", r)
	}
}

// A variable that points at the vault must cross as the pointer. Resolving it would write a second copy
// of a secret that already travels in the same bundle, and would cut the link that makes rotating it on
// the target reach every consumer.
func TestEnvReferencesTravelAsReferences(t *testing.T) {
	cases := []struct {
		name       string
		stored     string
		isSecret   bool
		wantValue  string
		wantSecret bool
	}{
		{
			name:      "plain reference stays a reference",
			stored:    "${{ secrets.STRIPE_KEY }}",
			wantValue: "${{ secrets.STRIPE_KEY }}",
		},
		{
			name:      "whitespace around a reference is normalized away",
			stored:    "  ${{ secrets.STRIPE_KEY }} ",
			wantValue: "${{ secrets.STRIPE_KEY }}",
		},
		{
			name:      "a reference the platform stores tightly spaced",
			stored:    "${{secrets.DB_URL}}",
			wantValue: "${{secrets.DB_URL}}",
		},
		{
			name:      "an ordinary value is untouched",
			stored:    "https://api.example.com",
			wantValue: "https://api.example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := envEntry("KEY", tc.stored, tc.isSecret)
			if err != nil {
				t.Fatalf("envEntry: %v", err)
			}
			if got.Value != tc.wantValue {
				t.Errorf("value = %q, want %q", got.Value, tc.wantValue)
			}
			if got.IsSecret != tc.wantSecret {
				t.Errorf("is_secret = %v, want %v", got.IsSecret, tc.wantSecret)
			}
		})
	}
}

// A value that merely mentions a reference among other text is not a pointer:
// the rest of it may be sensitive, so it keeps its secret flag — and is still
// never resolved.
func TestPartialReferenceKeepsItsSecretFlag(t *testing.T) {
	got, err := envEntry("DSN", "postgres://u:${{ secrets.PW }}@db/app", false)
	if err != nil {
		t.Fatalf("envEntry: %v", err)
	}
	if got.Value != "postgres://u:${{ secrets.PW }}@db/app" {
		t.Fatalf("a partial reference was rewritten: %q", got.Value)
	}
	if got.IsSecret {
		t.Fatal("a non-secret variable was promoted to secret")
	}
}

// The restore order is a dependency graph, and getting it wrong does not fail
// loudly — it produces a workspace quietly missing a piece. Each constraint here
// stands for a way that has already gone wrong, or would.
func TestRestoreStepsRunInDependencyOrder(t *testing.T) {
	r := &restoreRun{}
	at := map[string]int{}
	for i, step := range r.steps() {
		if _, dup := at[step.name]; dup {
			t.Fatalf("step %q appears twice", step.name)
		}
		at[step.name] = i
	}

	constraints := []struct{ before, after, why string }{
		{"domains", "certificates", "importing a certificate validates its names against the workspace's domains"},
		{"certificates", "routing", "a custom-TLS route names a stored certificate"},
		{"volumes", "apps", "an app's mounts name volumes"},
		{"stacks", "apps", "an app names its stack"},
		{"credentials", "apps", "an app names its registry and git credentials"},
		{"databases", "database-links", "a link attaches a database that must exist"},
		{"apps", "database-links", "a link attaches to an application"},
		{"apps", "routing", "a route names its application"},
		{"apps", "delivery", "pipelines and cron jobs name their application"},
		{"secrets", "apps", "an app's environment may reference a vault secret"},
		{"configs", "apps", "an app's mounts name configs, which must exist to attach"},
		{"delivery", "members", "delivery is the last thing that creates resources"},
	}
	for _, c := range constraints {
		i, ok := at[c.before]
		j, ok2 := at[c.after]
		if !ok || !ok2 {
			t.Fatalf("missing step in the order: %q or %q", c.before, c.after)
		}
		if i >= j {
			t.Errorf("%q must run before %q: %s", c.before, c.after, c.why)
		}
	}
}

// tail bounds helper output so a failed run's error stays readable.
func TestTailBoundsHelperOutput(t *testing.T) {
	if got := tail("short"); got != "short" {
		t.Fatalf("tail(short) = %q", got)
	}
	long := strings.Repeat("x", 5000)
	got := tail(long)
	if len(got) > 2100 || !strings.HasPrefix(got, "…") {
		t.Fatalf("tail did not bound the output: %d bytes", len(got))
	}
}
