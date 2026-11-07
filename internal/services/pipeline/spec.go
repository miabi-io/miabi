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

// Package pipeline implements Miabi's CI/CD: pipeline-as-code, run lifecycle, and the internal container runner.
package pipeline

import (
	"bytes"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/miabi-io/miabi/internal/services/versioning"
	"gopkg.in/yaml.v3"
)

const (
	apiVersion   = "miabi.io/v1"
	kindPipeline = "Pipeline"
	UsesBuild    = "build"
	// Deploys by the digest the pipeline built, with no rebuild.
	UsesDeploy = "deploy"
)

// Spec is a parsed pipeline-as-code document.
type Spec struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   SpecMeta `yaml:"metadata"`
	On         Triggers `yaml:"on"`
	// A step's own env wins on collision; values may reference ${{ secrets.NAME }}.
	Env   map[string]string `yaml:"env,omitempty"`
	Steps []Step            `yaml:"steps"`
	// Miabi still holds the counter: a repository carries only the rules, not state.
	Versioning *versioning.Settings `yaml:"versioning,omitempty"`
}

// HasBuild reports whether the pipeline builds an image, the only kind of run a version applies to.
func (s *Spec) HasBuild() bool {
	for i := range s.Steps {
		if s.Steps[i].Uses == UsesBuild {
			return true
		}
	}
	return false
}

// SpecMeta is the pipeline's identity block.
type SpecMeta struct {
	Name string `yaml:"name"`
}

// Triggers declares how a pipeline starts.
type Triggers struct {
	Push     *PushTrigger `yaml:"push,omitempty"`
	Manual   bool         `yaml:"manual,omitempty"`
	Schedule string       `yaml:"schedule,omitempty"`
}

// PushTrigger fires on a push to one of the named branches (empty = any).
type PushTrigger struct {
	Branches []string `yaml:"branches,omitempty"`
}

// Step is one unit of work in a pipeline.
type Step struct {
	Name string `yaml:"name"`
	// Image + Run execute a command in an isolated container.
	Image string `yaml:"image,omitempty"`
	Run   string `yaml:"run,omitempty"`
	// Uses selects a built-in step ("build" | "deploy") instead of a container.
	Uses string `yaml:"uses,omitempty"`
	// Relative to the source root; empty defaults to "Dockerfile".
	Dockerfile string `yaml:"dockerfile,omitempty"`
	// Independent of Dockerfile, like `docker build -f <dockerfile> <context>`; empty is the source root.
	Context string `yaml:"context,omitempty"`
	// Not for secrets: build args are baked into image history; use the step's env for credentials.
	BuildArgs map[string]string `yaml:"build-args,omitempty"`
	Cache     *bool             `yaml:"cache,omitempty"`
	// Pushed as one multi-arch image; empty builds for the runner's platform.
	Platforms []string `yaml:"platforms,omitempty"`
	// App overrides the deploy target for a `uses: deploy` step.
	App string            `yaml:"app,omitempty"`
	Env map[string]string `yaml:"env,omitempty"`
	// The step is still marked failed but the run can succeed, like GitHub Actions' continue-on-error.
	ContinueOnError bool `yaml:"continue-on-error,omitempty"`
}

// FiresOnBranch reports whether a push trigger matches the given branch.
func (t Triggers) FiresOnBranch(branch string) bool {
	if t.Push == nil {
		return false
	}
	if len(t.Push.Branches) == 0 {
		return true
	}
	for _, b := range t.Push.Branches {
		if b == branch {
			return true
		}
	}
	return false
}

// ParseSpec rejects unknown fields so typos surface.
func ParseSpec(data []byte) (*Spec, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var s Spec
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parse pipeline: %w", err)
	}
	if s.APIVersion != apiVersion {
		return nil, fmt.Errorf("apiVersion must be %q, got %q", apiVersion, s.APIVersion)
	}
	if s.Kind != kindPipeline {
		return nil, fmt.Errorf("kind must be %q, got %q", kindPipeline, s.Kind)
	}
	if len(s.Steps) == 0 {
		return nil, fmt.Errorf("pipeline must declare at least one step")
	}
	if err := validEnv("pipeline env", s.Env); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for i, st := range s.Steps {
		if strings.TrimSpace(st.Name) == "" {
			return nil, fmt.Errorf("step %d: name is required", i+1)
		}
		if names[st.Name] {
			return nil, fmt.Errorf("duplicate step name %q", st.Name)
		}
		names[st.Name] = true
		if err := validEnv(fmt.Sprintf("step %q env", st.Name), st.Env); err != nil {
			return nil, err
		}
		// Built-in steps run no container, so a step env would be silently dropped.
		if len(st.Env) > 0 && st.Uses != "" {
			return nil, fmt.Errorf("step %q: 'env' applies to container steps; move it to the pipeline-level env for a %q step", st.Name, st.Uses)
		}
		if st.Uses == "" && strings.TrimSpace(st.Image) == "" {
			return nil, fmt.Errorf("step %q: image is required (or use a built-in via 'uses')", st.Name)
		}
		if st.Uses != "" && st.Uses != UsesDeploy && st.Uses != UsesBuild {
			return nil, fmt.Errorf("step %q: unknown built-in %q", st.Name, st.Uses)
		}
		if st.Dockerfile != "" && st.Uses != UsesBuild {
			return nil, fmt.Errorf("step %q: 'dockerfile' is only valid on a 'uses: build' step", st.Name)
		}
		if st.Context != "" && st.Uses != UsesBuild {
			return nil, fmt.Errorf("step %q: 'context' is only valid on a 'uses: build' step", st.Name)
		}
		if len(st.BuildArgs) > 0 && st.Uses != UsesBuild {
			return nil, fmt.Errorf("step %q: 'build-args' is only valid on a 'uses: build' step", st.Name)
		}
		if st.Cache != nil && st.Uses != UsesBuild {
			return nil, fmt.Errorf("step %q: 'cache' is only valid on a 'uses: build' step", st.Name)
		}
		if len(st.Platforms) > 0 && st.Uses != UsesBuild {
			return nil, fmt.Errorf("step %q: 'platforms' is only valid on a 'uses: build' step", st.Name)
		}
		platforms, err := normalizePlatforms(st.Platforms)
		if err != nil {
			return nil, fmt.Errorf("step %q: %w", st.Name, err)
		}
		s.Steps[i].Platforms = platforms
		for k := range st.BuildArgs {
			if !validArgName(k) {
				return nil, fmt.Errorf("step %q: build-arg name %q is not a valid Dockerfile ARG (letters, digits and underscore; not starting with a digit)", st.Name, k)
			}
		}
		if err := validBuildPath("dockerfile", st.Dockerfile); err != nil {
			return nil, fmt.Errorf("step %q: %w", st.Name, err)
		}
		if err := validBuildPath("context", st.Context); err != nil {
			return nil, fmt.Errorf("step %q: %w", st.Name, err)
		}
	}
	if s.Versioning != nil {
		if !s.HasBuild() {
			return nil, fmt.Errorf("versioning: applies to pipelines with a 'uses: build' step")
		}
		if err := versioning.ValidateSettings(s.Versioning); err != nil {
			return nil, fmt.Errorf("versioning: %w", err)
		}
	}
	return &s, nil
}

func (s Step) NoCache() bool { return s.Cache != nil && !*s.Cache }

// buildPlatforms are the platforms a build may name: those BuildKit builds Linux images for.
var buildPlatforms = map[string]bool{
	"linux/amd64": true, "linux/arm64": true, "linux/arm/v7": true, "linux/arm/v6": true,
	"linux/386": true, "linux/ppc64le": true, "linux/s390x": true, "linux/riscv64": true,
}

// Refused here rather than failing minutes into the build with the builder's own wording.
func normalizePlatforms(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "linux/arm64/v8" {
			p = "linux/arm64"
		}
		if !buildPlatforms[p] {
			return nil, fmt.Errorf("unsupported platform %q; use one of %s", p, strings.Join(slices.Sorted(maps.Keys(buildPlatforms)), ", "))
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// Stricter than Docker: names with "=" or spaces silently produce args the Dockerfile can never reference.
func validArgName(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Pipelines are attacker-supplied on a shared runner; absolute or `..` paths would reach its filesystem.
func validBuildPath(field, p string) error {
	if p == "" {
		return nil
	}
	if strings.HasPrefix(p, "/") || filepath.IsAbs(p) {
		return fmt.Errorf("'%s' must be relative to the repository root, got %q", field, p)
	}
	// Clean resolves "a/../b"; anything still climbing after that escapes.
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("'%s' must stay inside the repository, got %q", field, p)
	}
	return nil
}

// envKeyRe is the POSIX-ish shell variable name a step can actually read back.
var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Redefining it would shadow the job token the deploy step authenticates with.
const reservedEnvPrefix = "MIABI_"

func validEnv(what string, env map[string]string) error {
	for k := range env {
		switch {
		case !envKeyRe.MatchString(k):
			return fmt.Errorf("%s: %q is not a valid environment variable name", what, k)
		case strings.HasPrefix(strings.ToUpper(k), reservedEnvPrefix):
			return fmt.Errorf("%s: %q is reserved — %s* is the platform's build context", what, k, reservedEnvPrefix)
		}
	}
	return nil
}
