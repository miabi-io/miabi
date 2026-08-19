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

func TestBuildJobSpecEnvPrecedence(t *testing.T) {
	spec, _ := BuildJobSpec(JobInputs{
		Run:           &models.PipelineRun{ID: 1, Number: 1, WorkspaceID: 2, Commit: "abc"},
		Pipeline:      "ci",
		WorkspaceName: "acme",
		Env:           map[string]string{"NODE_ENV": "production", "SHARED": "pipeline"},
		Steps: []models.PipelineStepRun{{
			Ordinal: 0, Name: "test", Image: "node:22", Run: "npm test",
			Env: map[string]string{"CI": "true", "SHARED": "step"},
		}},
		Creds: &JobCredentials{RegistryToken: "tok", JobToken: "jt"},
	})

	// Pipeline env lands on the job, after the platform context.
	if idx := indexOfKey(spec.Env, "NODE_ENV"); idx < 0 {
		t.Fatalf("pipeline env missing from the job spec: %v", spec.Env)
	}
	if platform, pipeline := indexOfKey(spec.Env, "MIABI_PIPELINE"), indexOfKey(spec.Env, "NODE_ENV"); platform > pipeline {
		t.Error("pipeline env must come after the platform context")
	}
	// Credentials last, so a pipeline cannot shadow them.
	if creds, pipeline := indexOfKey(spec.Env, "MIABI_JOB_TOKEN"), indexOfKey(spec.Env, "SHARED"); creds < pipeline {
		t.Error("injected credentials must come after pipeline env")
	}
	// The step keeps its own, which the runner applies after the job's.
	step := spec.Steps[0]
	if got := valueForKey(step.Env, "SHARED"); got != "step" {
		t.Errorf("step SHARED = %q, want the step's own value", got)
	}
	if got := valueForKey(step.Env, "CI"); got != "true" {
		t.Errorf("step CI = %q", got)
	}
}

func TestBuildJobSpecEnvIsDeterministic(t *testing.T) {
	in := JobInputs{
		Run: &models.PipelineRun{ID: 1}, Pipeline: "ci",
		Env:   map[string]string{"A": "1", "B": "2", "C": "3", "D": "4"},
		Steps: []models.PipelineStepRun{{Ordinal: 0, Name: "s", Env: map[string]string{"X": "1", "Y": "2"}}},
	}
	first, _ := BuildJobSpec(in)
	for i := 0; i < 20; i++ {
		next, _ := BuildJobSpec(in)
		if strings.Join(next.Env, ",") != strings.Join(first.Env, ",") {
			t.Fatal("job env order varies between builds of the same run")
		}
		if strings.Join(next.Steps[0].Env, ",") != strings.Join(first.Steps[0].Env, ",") {
			t.Fatal("step env order varies between builds of the same run")
		}
	}
}

func indexOfKey(env []string, key string) int {
	for i, e := range env {
		if strings.HasPrefix(e, key+"=") {
			return i
		}
	}
	return -1
}

func valueForKey(env []string, key string) string {
	if i := indexOfKey(env, key); i >= 0 {
		return strings.TrimPrefix(env[i], key+"=")
	}
	return ""
}

func TestBuildJobSpecCarriesNoCache(t *testing.T) {
	in := JobInputs{
		Run: &models.PipelineRun{ID: 1, WorkspaceID: 1},
		Steps: []models.PipelineStepRun{
			{Ordinal: 0, Name: "cold", Uses: "build", NoCache: true},
			{Ordinal: 1, Name: "cold-with-dockerfile", Uses: "build", Dockerfile: "docker/Dockerfile", NoCache: true},
			{Ordinal: 2, Name: "warm", Uses: "build"},
		},
		Creds: &JobCredentials{},
	}
	spec, _ := BuildJobSpec(in)

	if spec.Steps[0].Build == nil || !spec.Steps[0].Build.NoCache {
		t.Fatalf("no-cache never reached the runner: %+v", spec.Steps[0].Build)
	}
	if got := spec.Steps[0].Build.Method; got != "dockerfile" {
		t.Errorf("Method = %q, want dockerfile — a NoCache-only config must keep the nil config's builder", got)
	}
	if b := spec.Steps[1].Build; b == nil || !b.NoCache || b.Dockerfile != "docker/Dockerfile" {
		t.Errorf("dockerfile + no-cache mangled: %+v", b)
	}
	if spec.Steps[1].Build.Method != "" {
		t.Errorf("Method = %q, want empty — an explicit dockerfile keeps auto-detection as before", spec.Steps[1].Build.Method)
	}
	// A cached build with nothing else configured still sends nothing at all.
	if spec.Steps[2].Build != nil {
		t.Errorf("cached, unconfigured build step should send no BuildConfig, got %+v", spec.Steps[2].Build)
	}
}

// The registry cache is what makes an invalidation stick across runners: every build imports its
// branch's ref (plus the trunk's) and exports only its own, and a generation nothing has built yet
// forces a cold rebuild for runners whose cache is local and has no ref to rotate.
func TestBuildJobSpecCarriesCacheRefs(t *testing.T) {
	in := JobInputs{
		Run:             &models.PipelineRun{ID: 1, WorkspaceID: 1},
		Repository:      "reg.example.com/ws_1/web",
		Branch:          "feat/thing",
		CacheTrunk:      "main",
		CacheGeneration: 2,
		Steps: []models.PipelineStepRun{
			{Ordinal: 0, Name: "build", Uses: "build"},
			{Ordinal: 1, Name: "test", Image: "node:20", Run: "npm test"},
		},
		Creds: &JobCredentials{},
	}
	spec, _ := BuildJobSpec(in)

	b := spec.Steps[0].Build
	if b == nil {
		t.Fatal("build step carries no BuildConfig — the cache refs never reach the runner")
	}
	if b.CacheTo != "reg.example.com/ws_1/web:cache-feat-thing-g2" {
		t.Errorf("CacheTo = %q, want the branch's own ref", b.CacheTo)
	}
	if len(b.CacheFrom) != 2 || b.CacheFrom[1] != "reg.example.com/ws_1/web:cache-main-g2" {
		t.Errorf("CacheFrom = %v, want [own, trunk]", b.CacheFrom)
	}
	if b.NoCache {
		t.Error("a generation already built must not force a cold build")
	}
	// A container step has no build cache, so it carries no config at all.
	if spec.Steps[1].Build != nil {
		t.Errorf("script step should send no BuildConfig, got %+v", spec.Steps[1].Build)
	}

	// An invalidation nothing has rebuilt yet is a cold build wherever it lands.
	in.CacheCold = true
	spec, _ = BuildJobSpec(in)
	if !spec.Steps[0].Build.NoCache {
		t.Error("a bumped generation must force the next build cold")
	}
}
