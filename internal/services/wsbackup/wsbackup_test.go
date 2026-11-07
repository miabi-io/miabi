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

package wsbackup

import (
	"context"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/storage/blob"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

// An aliased S3 config is how volume archives end up in the database prefix.
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

// A retry would otherwise fail on its predecessor's leftover container rather than on the work.
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

// Resolving would duplicate a secret that already travels and cut the link that makes rotation reach consumers.
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

// The rest of the value may be sensitive, so it keeps its secret flag and is never resolved.
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

// A wrong order doesn't fail loudly: it produces a workspace quietly missing a piece.
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
		{"secrets", "credentials", "a registry or git credential may reference a vault secret"},
		{"databases", "database-links", "a link attaches a database that must exist"},
		{"apps", "database-links", "a link attaches to an application"},
		{"apps", "routing", "a route names its application"},
		{"apps", "delivery", "pipelines and cron jobs name their application"},
		{"secrets", "apps", "an app's environment may reference a vault secret"},
		{"sealing-keys", "secrets", "a sealed secret's fingerprint is only meaningful once its key is here"},
		{"configs", "apps", "an app's mounts name configs, which must exist to attach"},
		{"databases", "connections", "the target's connections exist only once its databases do"},
		{"connections", "configs", "a config file may hold a source database's connection"},
		{"connections", "stacks", "a stack's shared env may hold a source database's connection"},
		{"connections", "apps", "an app's env may hold a source database's connection"},
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

type fakeLister map[string][]blob.Object

func (f fakeLister) List(_ context.Context, prefix string) ([]blob.Object, error) {
	return f[prefix], nil
}

// volume-bkup prints only the archive name, so the size must come from the bucket.
func TestSizeArtifactsFillsWhatHelpersDidNotReport(t *testing.T) {
	store := fakeLister{"b/vols": {
		{Key: "b/vols/data_1.tar.gz.meta.json", Size: 5},
		{Key: "b/vols/data_1.tar.gz", Size: 3013037},
		{Key: "b/vols/data_10.tar.gz", Size: 9},
	}}
	arts := []wsbundle.Artifact{
		{Subject: wsbundle.SubjectVolume, Path: "b/vols", File: "data_1.tar.gz"},
		{Subject: wsbundle.SubjectDatabase, Path: "b/dbs", File: "shop.sql.gz", SizeBytes: 386},
		{Subject: wsbundle.SubjectVolume, Path: "b/vols", Error: "archive failed"},
	}
	sizeArtifacts(context.Background(), store, arts)
	if arts[0].SizeBytes != 3013037 {
		t.Errorf("volume size = %d, want the archive's object size", arts[0].SizeBytes)
	}
	if arts[1].SizeBytes != 386 {
		t.Errorf("a size the helper reported was overwritten: %d", arts[1].SizeBytes)
	}
	if arts[2].SizeBytes != 0 {
		t.Errorf("a failed artifact was sized: %d", arts[2].SizeBytes)
	}
}

// Left as is, a migrated app points at a database on the old install.
func TestConnectionRewritePointsValuesAtTheTarget(t *testing.T) {
	src := wsbundle.Connection{
		Host: "mb-db-old-72", User: "u_ebf59e9f", Password: "oldpassword123",
		URI: "postgres://u_ebf59e9f:oldpassword123@mb-db-old-72:5432/shop",
	}
	dst := database.ConnectionInfo{
		Host: "mb-db-new-73", Port: 5432, Username: "u_abbcf4a2", Password: "newpassword456",
		URI: "postgres://u_abbcf4a2:newpassword456@mb-db-new-73:5432/shop",
	}
	r := &restoreRun{conn: newRewriter(connectionPairs(src, dst))}
	cases := map[string]string{
		src.URI:                    dst.URI,
		"jdbc:" + src.URI + "?ssl": "jdbc:" + dst.URI + "?ssl",
		"mb-db-old-72":             "mb-db-new-73",
		"u_ebf59e9f":               "u_abbcf4a2",
		"oldpassword123":           "newpassword456",
		"shop":                     "shop",
		"${{ secrets.key }}":       "${{ secrets.key }}",
	}
	for in, want := range cases {
		if got := r.rewrite(in); got != want {
			t.Errorf("rewrite(%q) = %q, want %q", in, got, want)
		}
	}
	if r.rewritten != 5 {
		t.Errorf("rewritten = %d, want the 5 values that changed", r.rewritten)
	}
}

// A short or empty source value would match inside unrelated text, and an equal one needs no rewrite.
func TestConnectionPairsSkipsUnsafeValues(t *testing.T) {
	pairs := connectionPairs(
		wsbundle.Connection{Host: "db", User: "same-user", Password: ""},
		database.ConnectionInfo{Host: "mb-db-new", Username: "same-user", Password: "x"},
	)
	if len(pairs) != 0 {
		t.Errorf("pairs = %q, want none", pairs)
	}
	if newRewriter(nil) != nil {
		t.Error("an empty rewrite should be nil, leaving values untouched")
	}
}
