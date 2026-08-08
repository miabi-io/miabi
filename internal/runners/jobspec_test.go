// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package runners

import (
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		}
	}
	return m
}

func TestBuildJobSpec(t *testing.T) {
	app := uint(128)
	in := JobInputs{
		Run:           &models.PipelineRun{ID: 900, Number: 57, WorkspaceID: 42, Commit: "abc123"},
		Pipeline:      "deploy",
		Steps:         []models.PipelineStepRun{{Ordinal: 0, Name: "build", Uses: "build"}, {Ordinal: 1, Name: "deploy", Uses: "deploy"}},
		AppID:         &app,
		AppName:       "web",
		WorkspaceName: "acme-prod",
		Registry:      "registry.example.com",
		Repository:    "registry.example.com/ws-42/web",
		Ref:           "refs/heads/main",
		Branch:        "main",
		Creds:         &JobCredentials{RegistryUser: "miabi-job", RegistryToken: "mb_reg_secret", JobToken: "mb_job_secret"},
		Deadline:      time.Unix(1_900_000_000, 0),
	}
	spec, mask := BuildJobSpec(in)

	if spec.RunID != 900 || spec.WorkspaceID != 42 || len(spec.Steps) != 2 || spec.Steps[1].Uses != "deploy" {
		t.Fatalf("spec core wrong: %+v", spec)
	}
	env := envMap(spec.Env)
	want := map[string]string{
		"MIABI_WORKSPACE_NAME":   "acme-prod",
		"MIABI_WORKSPACE_ID":     "42",
		"MIABI_RUN_ID":           "900",
		"MIABI_RUN_NUMBER":       "57",
		"MIABI_PIPELINE":         "deploy",
		"MIABI_COMMIT":           "abc123",
		"MIABI_WORKDIR":          "/workspace",
		"MIABI_APP_NAME":         "web",
		"MIABI_APP_ID":           "128",
		"MIABI_BRANCH":           "main",
		"MIABI_REGISTRY":         "registry.example.com",
		"MIABI_IMAGE_REPOSITORY": "registry.example.com/ws-42/web",
		"MIABI_REGISTRY_USER":    "miabi-job",
		"MIABI_REGISTRY_TOKEN":   "mb_reg_secret",
		"MIABI_JOB_TOKEN":        "mb_job_secret",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, env[k], v)
		}
	}
	// The secret token values are returned for log redaction.
	if len(mask) != 2 {
		t.Fatalf("mask = %v, want the two token values", mask)
	}
}

// Without a job token (disabled) only the registry secret is present and masked.
func TestBuildJobSpecNoJobToken(t *testing.T) {
	in := JobInputs{
		Run:   &models.PipelineRun{ID: 1, WorkspaceID: 1},
		Creds: &JobCredentials{RegistryUser: "miabi-job", RegistryToken: "mb_reg"},
	}
	spec, mask := BuildJobSpec(in)
	env := envMap(spec.Env)
	if _, ok := env["MIABI_JOB_TOKEN"]; ok {
		t.Error("MIABI_JOB_TOKEN should be absent when no job token minted")
	}
	if len(mask) != 1 || mask[0] != "mb_reg" {
		t.Errorf("mask = %v, want [mb_reg]", mask)
	}
}

// A container (image) step's `run:` must reach the runner wrapped as a
// non-login shell command; a `uses:` built-in step carries no command.
func TestBuildJobSpecStepRun(t *testing.T) {
	in := JobInputs{
		Run: &models.PipelineRun{ID: 1, WorkspaceID: 1},
		Steps: []models.PipelineStepRun{
			{Ordinal: 0, Name: "test", Image: "node:20", Run: "npm ci && npm test", ContinueOnError: true},
			{Ordinal: 1, Name: "build", Uses: "build"},
		},
		Creds: &JobCredentials{},
	}
	spec, _ := BuildJobSpec(in)
	got := spec.Steps[0].Run
	want := []string{"sh", "-c", "npm ci && npm test"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("step[0].Run = %#v, want %#v", got, want)
	}
	if !spec.Steps[0].ContinueOnError {
		t.Error("ContinueOnError should propagate to the job spec")
	}
	if spec.Steps[1].Run != nil {
		t.Errorf("uses step should carry no Run, got %#v", spec.Steps[1].Run)
	}
}

func TestShellCommand(t *testing.T) {
	if got := shellCommand(""); got != nil {
		t.Errorf("empty run: got %#v, want nil", got)
	}
	if got := shellCommand("   "); got != nil {
		t.Errorf("whitespace run: got %#v, want nil", got)
	}
	got := shellCommand("go build ./...")
	if len(got) != 3 || got[0] != "sh" || got[1] != "-c" || got[2] != "go build ./..." {
		t.Errorf("got %#v", got)
	}
}

func TestRedact(t *testing.T) {
	line := "pushing with token mb_reg_secret and mb_job_secret done"
	got := redact(line, []string{"mb_reg_secret", "mb_job_secret", ""})
	if strings.Contains(got, "mb_reg_secret") || strings.Contains(got, "mb_job_secret") {
		t.Errorf("secrets not redacted: %q", got)
	}
	if !strings.Contains(got, "••••") {
		t.Errorf("expected mask marker in %q", got)
	}
}

// The bug this guards: `dockerfile:` was parsed and validated at the spec layer, stored, and then
// never copied into the wire spec — so every pipeline build used the root Dockerfile regardless of
// what the pipeline file said, with nothing reporting that the setting had been ignored.
func TestBuildJobSpecCarriesDockerfile(t *testing.T) {
	in := JobInputs{
		Run: &models.PipelineRun{ID: 1, WorkspaceID: 1},
		Steps: []models.PipelineStepRun{
			{Ordinal: 0, Name: "build", Uses: "build", Dockerfile: "docker/Dockerfile"},
			{Ordinal: 1, Name: "plain", Uses: "build"},
			{Ordinal: 2, Name: "script", Image: "node:20", Run: "npm test"},
		},
		Creds: &JobCredentials{},
	}
	spec, _ := BuildJobSpec(in)

	if spec.Steps[0].Build == nil {
		t.Fatal("build step carries no BuildConfig — the dockerfile never reaches the runner")
	}
	if got := spec.Steps[0].Build.Dockerfile; got != "docker/Dockerfile" {
		t.Errorf("Dockerfile = %q, want docker/Dockerfile", got)
	}
	// No dockerfile means no BuildConfig at all, which is what selects the
	// runner's auto-detection; an empty struct would read as "configured".
	if spec.Steps[1].Build != nil {
		t.Errorf("unconfigured build step should send no BuildConfig, got %+v", spec.Steps[1].Build)
	}
	if spec.Steps[2].Build != nil {
		t.Errorf("script step should send no BuildConfig, got %+v", spec.Steps[2].Build)
	}
}
