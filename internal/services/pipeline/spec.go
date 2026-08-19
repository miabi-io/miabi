// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pipeline implements Miabi's CI/CD: pipeline-as-code versioned with the app, PipelineRun lifecycle,
// and the internal runner that executes steps in isolated containers. It is the imperative, push-based half
// of the GitOps and CI/CD model — a pipeline turns a commit into an image and a Release.
package pipeline

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	apiVersion   = "miabi.io/v1"
	kindPipeline = "Pipeline"
	// UsesBuild is the built-in step that builds the shared workspace into an
	// image, captures its digest, and records an Image row. It is what turns a
	// commit into a deployable artifact.
	UsesBuild = "build"
	// UsesDeploy is the built-in terminal step that deploys an app by the image
	// the pipeline built (deploy-by-digest, no rebuild).
	UsesDeploy = "deploy"
)

// Spec is a parsed pipeline-as-code document.
type Spec struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   SpecMeta `yaml:"metadata"`
	On         Triggers `yaml:"on"`
	// Env applies to every step; a step's own env wins on a key collision. Values
	// may reference workspace secrets as ${{ secrets.NAME }}.
	Env   map[string]string `yaml:"env,omitempty"`
	Steps []Step            `yaml:"steps"`
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
	// Dockerfile names the Dockerfile for a `uses: build` step, relative to the
	// checked-out source root. Empty defaults to "Dockerfile".
	Dockerfile string `yaml:"dockerfile,omitempty"`
	// Context is the build context directory for a `uses: build` step, relative to the checked-out source root;
	// empty means the root itself. Independent of Dockerfile, matching `docker build -f <dockerfile> <context>`:
	// a monorepo commonly keeps `docker/Dockerfile` while building from the root.
	Context string `yaml:"context,omitempty"`
	// BuildArgs are Dockerfile ARG values for a `uses: build` step. NOT for secrets: a build arg is baked into
	// the image's history, so anyone who can pull the image can read it back. Use the step's env for
	// credentials.
	BuildArgs map[string]string `yaml:"build-args,omitempty"`
	Cache     *bool             `yaml:"cache,omitempty"`
	// App overrides the deploy target for a `uses: deploy` step.
	App string            `yaml:"app,omitempty"`
	Env map[string]string `yaml:"env,omitempty"`
	// ContinueOnError keeps the run going when this step fails (the step is still
	// marked failed, the run can still succeed) — like GitHub Actions'
	// `continue-on-error: true`.
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

// ParseSpec decodes and validates a kind: Pipeline document. Unknown fields are
// rejected so typos surface.
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
		// A built-in step runs no container of its own, so a step-level env would
		// be accepted and dropped — the pipeline-level env is what reaches it.
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
		for k := range st.BuildArgs {
			if !validArgName(k) {
				return nil, fmt.Errorf("step %q: build-arg name %q is not a valid Dockerfile ARG (letters, digits and underscore; not starting with a digit)", st.Name, k)
			}
		}
		// runner support pending: the wire format gains BuildConfig.Context and BuildArgs in the next runner
		// release. Refuse both until then instead of accepting a value and dropping it — a build that quietly
		// ignores its context or its args produces a wrong image and blames nothing.
		if st.Context != "" {
			return nil, fmt.Errorf("step %q: 'context' needs a newer runner; upgrade your runners, or move the build so the repository root is the context", st.Name)
		}
		if len(st.BuildArgs) > 0 {
			return nil, fmt.Errorf("step %q: 'build-args' needs a newer runner; upgrade your runners, or bake the values into the Dockerfile", st.Name)
		}
		if err := validBuildPath("dockerfile", st.Dockerfile); err != nil {
			return nil, fmt.Errorf("step %q: %w", st.Name, err)
		}
		if err := validBuildPath("context", st.Context); err != nil {
			return nil, fmt.Errorf("step %q: %w", st.Name, err)
		}
	}
	return &s, nil
}

func (s Step) NoCache() bool { return s.Cache != nil && !*s.Cache }

// validArgName reports whether k is usable as a Dockerfile ARG name. Docker accepts a good deal more and then
// does nothing useful with it — a name carrying "=" or a space silently produces an arg the Dockerfile can
// never reference — so the pipeline is stricter than the tool, and says so while the operator is still there.
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

// validBuildPath keeps a build path inside the checked-out source. Both values are joined against the runner's
// workdir, so an absolute path or a `..` segment would reach the runner's own filesystem — and a pipeline is
// attacker-supplied (anyone who can push to a branch can edit it) on a runner shared across a workspace.
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

// reservedEnvPrefix is the platform's own build context. A pipeline that could
// redefine it would shadow the job token the deploy step authenticates with.
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
