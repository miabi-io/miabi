// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package runners

import (
	"strconv"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/runner/proto"
)

// defaultWorkdir is the checked-out source directory exposed to steps as
// MIABI_WORKDIR (MIABI_WORKSPACE_NAME/_ID unambiguously mean the tenant).
const defaultWorkdir = "/workspace"

// JobInputs is the run-derived context a caller supplies to build a JobSpec.
// The Dispatcher fills in the per-run Deadline, Creds, and runner name.
type JobInputs struct {
	Run           *models.PipelineRun
	Pipeline      string // pipeline name (MIABI_PIPELINE)
	Steps         []models.PipelineStepRun
	AppID         *uint
	AppName       string
	WorkspaceName string // workspace handle (MIABI_WORKSPACE_NAME)
	Registry      string // registry host (MIABI_REGISTRY)
	Repository    string // fully-qualified image repo (MIABI_IMAGE_REPOSITORY)
	Ref           string
	Branch        string
	SourceURL     string // git remote the runner clones + checks out at the commit
	Workdir       string // defaults to /workspace

	// Filled by the Dispatcher.
	Creds    *JobCredentials
	Deadline time.Time
}

// BuildJobSpec assembles the wire JobSpec sent to a runner: the predefined,
// non-secret MIABI_* build context plus the per-job secret credentials (as env,
// masked from the log stream by the dispatcher), and the run's steps. Returns
// the spec and the secret values to redact from live logs.
func BuildJobSpec(in JobInputs) (proto.JobSpec, []string) {
	workdir := in.Workdir
	if workdir == "" {
		workdir = defaultWorkdir
	}

	env := []string{
		kv("MIABI_WORKSPACE_NAME", in.WorkspaceName),
		kv("MIABI_WORKSPACE_ID", utos(in.Run.WorkspaceID)),
		kv("MIABI_RUN_ID", utos(in.Run.ID)),
		kv("MIABI_RUN_NUMBER", strconv.Itoa(in.Run.Number)),
		kv("MIABI_PIPELINE", in.Pipeline),
		kv("MIABI_COMMIT", in.Run.Commit),
		kv("MIABI_WORKDIR", workdir),
	}
	env = appendKV(env, "MIABI_APP_NAME", in.AppName)
	if in.AppID != nil {
		env = append(env, kv("MIABI_APP_ID", utos(*in.AppID)))
	}
	env = appendKV(env, "MIABI_REF", in.Ref)
	env = appendKV(env, "MIABI_BRANCH", in.Branch)
	env = appendKV(env, "MIABI_REGISTRY", in.Registry)
	env = appendKV(env, "MIABI_IMAGE_REPOSITORY", in.Repository)

	// Per-job secret credentials, injected as env so `docker push`/buildx auth
	// works with no login step. Their values are redacted from the log stream.
	if in.Creds != nil {
		env = appendKV(env, "MIABI_REGISTRY_USER", in.Creds.RegistryUser)
		env = appendKV(env, "MIABI_REGISTRY_TOKEN", in.Creds.RegistryToken)
		env = appendKV(env, "MIABI_JOB_TOKEN", in.Creds.JobToken)
	}

	steps := make([]proto.StepSpec, 0, len(in.Steps))
	for i := range in.Steps {
		s := &in.Steps[i]
		steps = append(steps, proto.StepSpec{
			Ordinal: s.Ordinal, Name: s.Name, Uses: s.Uses, Image: s.Image,
			Run:             shellCommand(s.Run),
			ContinueOnError: s.ContinueOnError,
			Build:           buildConfig(s),
		})
	}

	spec := proto.JobSpec{
		RunID:       in.Run.ID,
		RunNumber:   in.Run.Number,
		Pipeline:    in.Pipeline,
		WorkspaceID: in.Run.WorkspaceID,
		Workspace:   in.WorkspaceName,
		AppID:       in.AppID,
		App:         in.AppName,
		Commit:      in.Run.Commit,
		Ref:         in.Ref,
		Branch:      in.Branch,
		SourceURL:   in.SourceURL,
		Repository:  in.Repository,
		Registry:    in.Registry,
		Steps:       steps,
		Env:         env,
		Deadline:    in.Deadline,
	}
	return spec, in.Creds.Secrets()
}

// buildConfig carries a build step's Dockerfile/context choice to the runner.
//
// Returns nil when the step chose neither, which is what selects the runner's
// auto-detection (root Dockerfile → dockerfile build, else buildpacks). Sending
// an empty BuildConfig instead would be indistinguishable on the wire, but nil is
// the documented "not configured" and keeps the JSON free of an empty object.
//
// This is the step that was missing: `dockerfile:` was parsed and validated at
// the spec layer and then never reached the wire, so every pipeline build used
// the root Dockerfile no matter what the file said.
// BuildContext and BuildArgs are deliberately not sent yet: proto.BuildConfig
// gains those fields in the next runner release, and the spec layer rejects both
// keys until then rather than accepting values it would drop here — which is
// precisely how `dockerfile:` came to look supported while doing nothing.
//
// To enable them: bump github.com/miabi-io/runner past the release carrying
// BuildConfig.Context and .BuildArgs, add
//
//	Context:   strings.TrimSpace(s.BuildContext),
//	BuildArgs: s.BuildArgs,
//
// below, and delete the two guards in pipeline/spec.go marked "runner support
// pending".
func buildConfig(s *models.PipelineStepRun) *proto.BuildConfig {
	df := strings.TrimSpace(s.Dockerfile)
	if df == "" {
		return nil
	}
	return &proto.BuildConfig{Dockerfile: df}
}

// shellCommand wraps a step's `run:` script so the runner executes it in a
// non-login shell inside the step image — the same model as a GitHub Actions
// `run:` step, so pipes, `&&`, and env expansion (e.g. $MIABI_IMAGE) work. An
// empty script yields nil, leaving the image's own entrypoint/CMD in charge
// (used by `uses:` built-in steps, which carry no command).
func shellCommand(run string) []string {
	if strings.TrimSpace(run) == "" {
		return nil
	}
	return []string{"sh", "-c", run}
}

func kv(k, v string) string { return k + "=" + v }

func appendKV(env []string, k, v string) []string {
	if v == "" {
		return env
	}
	return append(env, kv(k, v))
}

func utos(u uint) string { return strconv.FormatUint(uint64(u), 10) }
