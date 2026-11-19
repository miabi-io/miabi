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

package declarative

import (
	"strings"
	"testing"
)

func TestJobEnvFingerprint(t *testing.T) {
	base := JobSpec{App: "api", Command: []string{"migrate"}}
	plain := base.Fingerprint()
	withEnv := base
	withEnv.Env = map[string]string{"MODE": "full"}
	if withEnv.Fingerprint() == plain {
		t.Fatal("env must change what a run executes")
	}
	off := false
	isolated := base
	isolated.InheritEnv = &off
	if isolated.Fingerprint() == plain {
		t.Fatal("inheritEnv: false must change the fingerprint")
	}
	on := true
	explicit := base
	explicit.InheritEnv = &on
	if explicit.Fingerprint() != plain {
		t.Fatal("a Job without env must keep the fingerprint it was stored with")
	}
}

func TestCronJobEnvDiff(t *testing.T) {
	actual := Resource{Kind: KindCronJob, CronJob: &CronJobSpec{App: "api", Schedule: "0 2 * * *", Command: []string{"x"},
		Env: map[string]string{"MODE": "nightly", "TOKEN": ""}, SecretEnv: []string{"TOKEN"}}}
	desired := Resource{Kind: KindCronJob, CronJob: &CronJobSpec{App: "api", Schedule: "0 2 * * *", Command: []string{"x"},
		Env: map[string]string{"MODE": "nightly", "TOKEN": "new-value"}, SecretEnv: []string{"TOKEN"}}}
	if d := diffFields(actual, desired); len(d) != 0 {
		t.Fatalf("a secret value must not surface or diff: %+v", d)
	}
	desired.CronJob.Env = map[string]string{"MODE": "weekly"}
	desired.CronJob.SecretEnv = nil
	off := false
	desired.CronJob.InheritEnv = &off
	fields := map[string]bool{}
	for _, f := range diffFields(actual, desired) {
		fields[f.Field] = true
	}
	if !fields["env.MODE"] || !fields["env.TOKEN"] || !fields["inheritEnv"] {
		t.Fatalf("want env.MODE, env.TOKEN and inheritEnv to diff, got %v", fields)
	}
}

func TestJobSecretEnvMustBeInEnv(t *testing.T) {
	doc := "apiVersion: miabi.io/v1\nkind: Job\nmetadata: {name: m}\nspec:\n  app: api\n  command: [migrate]\n  secretEnv: [TOKEN]\n"
	if _, err := Parse([]byte(doc)); err == nil || !strings.Contains(err.Error(), "not in env") {
		t.Fatalf("err = %v", err)
	}
}

func TestJobEnvReferencesAreChecked(t *testing.T) {
	doc := "apiVersion: miabi.io/v1\nkind: CronJob\nmetadata: {name: c}\nspec:\n  app: api\n  schedule: \"0 2 * * *\"\n  command: [x]\n  env:\n    KEY: \"{{ .secrets.export-key }}\"\n"
	set, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	err = CheckReferences(set.References(), KnownNames{"applications": {"api": true}, "secrets": {}})
	if err == nil || !strings.Contains(err.Error(), `unknown secret "export-key"`) {
		t.Fatalf("err = %v, want the unknown secret named", err)
	}
}
