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

package worker

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func envMapOf(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

func TestBuildEnvManagedLayer(t *testing.T) {
	app := &models.Application{
		ID: 7, WorkspaceID: 3, Name: "api", Port: 8080, NanoCPUs: 1_500_000_000, MemoryBytes: 512 << 20,
		EnvVars: []models.AppEnvVar{{Key: "MIABI_PORT", Value: "9000"}, {Key: "OWN", Value: "x"}},
	}
	run := managedRun{kind: "app", deploymentID: 42, release: 12, image: "ghcr.io/acme/api:v1.4.2", imageTag: "v1.4.2", commit: "abc123"}
	env, err := (&runtimeBuilder{}).buildEnv(app, run)
	if err != nil {
		t.Fatal(err)
	}
	got := envMapOf(env)
	want := map[string]string{
		"MIABI_APP_NAME": "api", "MIABI_APP_ID": "7", "MIABI_WORKSPACE_ID": "3", "MIABI_KIND": "app",
		"MIABI_DEPLOYMENT_ID": "42", "MIABI_RELEASE": "12", "MIABI_VERSION": "1.4.2", "MIABI_IMAGE_TAG": "v1.4.2",
		"MIABI_COMMIT": "abc123", "MIABI_IMAGE": "ghcr.io/acme/api:v1.4.2", "MIABI_INTERNAL_URL": "http://api:8080",
		"MIABI_REPLICA": "1", "MIABI_CPU_LIMIT": "1.5", "MIABI_MEMORY_LIMIT_MB": "512",
		"MIABI_PORT": "9000", "OWN": "x",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	for _, unset := range []string{"MIABI_WORKSPACE_NAME", "MIABI_STACK", "MIABI_JOB_NAME", "MIABI_IMAGE_DIGEST", "MIABI_PUBLIC_URL", "MIABI_NODE"} {
		if _, ok := got[unset]; ok {
			t.Errorf("%s is set without a value", unset)
		}
	}
	if n := len(env); n != len(got) {
		t.Fatalf("duplicate keys in env: %v", env)
	}
}

func TestBuildEnvAppOverridesManaged(t *testing.T) {
	app := &models.Application{Name: "api", EnvVars: []models.AppEnvVar{{Key: "MIABI_APP_NAME", Value: "mine"}}}
	env, err := (&runtimeBuilder{}).buildEnv(app, managedRun{})
	if err != nil {
		t.Fatal(err)
	}
	if envMapOf(env)["MIABI_APP_NAME"] != "mine" {
		t.Fatalf("app value should win: %v", env)
	}
}

func TestBuildEnvManagedOptOut(t *testing.T) {
	app := &models.Application{Name: "api", Port: 80, MiabiEnvDisabled: true}
	env, err := (&runtimeBuilder{}).buildEnv(app, managedRun{kind: "app", deploymentID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 0 {
		t.Fatalf("opted-out app got %v", env)
	}
}

func TestJobRunKinds(t *testing.T) {
	cron := uint(5)
	h := &JobHandler{}
	if r := h.jobRun(&models.Job{ID: 9, Name: "migrate"}); r.kind != "job" || r.jobRunID != 9 || r.jobName != "migrate" {
		t.Fatalf("job run = %+v", r)
	}
	if r := h.jobRun(&models.Job{ID: 10, Name: "nightly", CronJobID: &cron}); r.kind != "cronjob" {
		t.Fatalf("cron run = %+v", r)
	}
}
