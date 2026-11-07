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

package runners

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/runner/proto"
)

// MIABI_WORKSPACE_NAME/_ID already mean the tenant, hence the separate MIABI_WORKDIR.
const defaultWorkdir = "/workspace"

// The Dispatcher fills in Deadline, Creds and runner name.
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
	// Values may still carry ${{ secrets.NAME }}; the Dispatcher resolves them.
	Env map[string]string
	// CacheCold is needed because a runner whose cache is its local daemon has no ref to rotate.
	CacheGeneration uint
	CacheTrunk      string
	CacheCold       bool

	// Filled by the Dispatcher.
	Creds    *JobCredentials
	Deadline time.Time
}

// Returns the spec and the secret values to redact from live logs.
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
	env = append(env, versionEnv(in.Run)...)

	// Placed after the platform context and before the credentials so it can never shadow either.
	env = append(env, sortedKV(in.Env)...)

	// Injected as env so push/buildx auth works without a login step; values are redacted from logs.
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
			Env:             sortedKV(s.Env),
			ContinueOnError: s.ContinueOnError,
			Build:           buildConfig(s, in),
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
	return spec, append(in.Creds.Secrets(), sourceURLSecrets(in.SourceURL)...)
}

func buildConfig(s *models.PipelineStepRun, in JobInputs) *proto.BuildConfig {
	if s.Uses != "build" {
		return nil
	}
	noCache := s.NoCache || in.CacheCold
	cacheFrom, cacheTo := CacheRefs(in.Repository, in.Branch, in.CacheTrunk, in.CacheGeneration)
	df := strings.TrimSpace(s.Dockerfile)
	buildCtx := strings.TrimSpace(s.BuildContext)
	if df == "" && buildCtx == "" && len(s.BuildArgs) == 0 && !noCache && cacheTo == "" && len(s.Platforms) == 0 {
		return nil
	}
	cfg := &proto.BuildConfig{
		Dockerfile: df, Context: buildCtx, BuildArgs: expandVersionArgs(s.BuildArgs, in.Run),
		NoCache: noCache, CacheFrom: cacheFrom, CacheTo: cacheTo, Platforms: s.Platforms,
	}
	if df == "" {
		// A config sent only for cache or platforms must not flip nil's "root Dockerfile" into silent buildpack auto-detection.
		cfg.Method = "dockerfile"
	}
	return cfg
}

// Empty yields nil so the image's own entrypoint runs (for `uses:` steps).
func shellCommand(run string) []string {
	if strings.TrimSpace(run) == "" {
		return nil
	}
	return []string{"sh", "-c", run}
}

// The tag is written only once the run succeeds; within the run MIABI_IMAGE_DIGEST is the ref.
func versionEnv(run *models.PipelineRun) []string {
	if run.Version == "" {
		return nil
	}
	core, pre, _ := strings.Cut(run.Version, "-")
	env := []string{kv("MIABI_VERSION", run.Version), kv("MIABI_IMAGE_TAG", run.ImageTag)}
	parts := strings.Split(core, ".")
	for i, name := range []string{"MIABI_VERSION_MAJOR", "MIABI_VERSION_MINOR", "MIABI_VERSION_PATCH"} {
		if i < len(parts) {
			env = append(env, kv(name, parts[i]))
		}
	}
	return appendKV(env, "MIABI_VERSION_PRERELEASE", pre)
}

// Only braced version variables expand; the runner never expands build args itself.
func expandVersionArgs(args map[string]string, run *models.PipelineRun) map[string]string {
	if len(args) == 0 || run == nil || run.Version == "" {
		return args
	}
	pairs := make([]string, 0, 12)
	for _, e := range versionEnv(run) {
		k, v, _ := strings.Cut(e, "=")
		pairs = append(pairs, "${"+k+"}", v)
	}
	r := strings.NewReplacer(pairs...)
	out := make(map[string]string, len(args))
	for k, v := range args {
		out[k] = r.Replace(v)
	}
	return out
}

func kv(k, v string) string { return k + "=" + v }

// Deterministic so a job spec is comparable across dispatches of the same run.
func sortedKV(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, kv(k, m[k]))
	}
	return out
}

func appendKV(env []string, k, v string) []string {
	if v == "" {
		return env
	}
	return append(env, kv(k, v))
}

func utos(u uint) string { return strconv.FormatUint(uint64(u), 10) }

// Context and BuildArgs predate feature advertising and older runners silently drop them, so gate by version.
const minBuildInputsVersion = "0.0.8"

// requiredRunnerVersion is the oldest runner release the run's steps can run on, or "" for any.
func requiredRunnerVersion(steps []models.PipelineStepRun) string {
	for i := range steps {
		s := &steps[i]
		if s.Uses == "build" && (strings.TrimSpace(s.BuildContext) != "" || len(s.BuildArgs) > 0) {
			return minBuildInputsVersion
		}
	}
	return ""
}

// requiredFeatures are the runner features the run's steps depend on.
func requiredFeatures(steps []models.PipelineStepRun) []string {
	for i := range steps {
		if steps[i].Uses == "build" && len(steps[i].Platforms) > 0 {
			return []string{proto.FeatureMultiPlatform}
		}
	}
	return nil
}
