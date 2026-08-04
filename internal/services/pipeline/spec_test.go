// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package pipeline

import (
	"strings"
	"testing"
)

const validPipeline = `
apiVersion: miabi.io/v1
kind: Pipeline
metadata: { name: web }
on:
  push: { branches: [main] }
  manual: true
steps:
  - name: build
    image: gcr.io/kaniko-project/executor
    run: "--dockerfile=Dockerfile --destination=$IMAGE"
  - name: test
    image: node:20
    run: "npm test"
  - name: deploy
    uses: deploy
    app: web
`

func TestParseSpecValid(t *testing.T) {
	s, err := ParseSpec([]byte(validPipeline))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(s.Steps) != 3 {
		t.Fatalf("want 3 steps, got %d", len(s.Steps))
	}
	if s.Steps[2].Uses != UsesDeploy {
		t.Errorf("deploy step not recognized: %q", s.Steps[2].Uses)
	}
	if !s.On.FiresOnBranch("main") || s.On.FiresOnBranch("dev") {
		t.Error("push branch matching is wrong")
	}
}

func TestParseSpecContinueOnError(t *testing.T) {
	const y = `
apiVersion: miabi.io/v1
kind: Pipeline
metadata: { name: web }
steps:
  - name: scan
    image: aquasec/trivy:latest
    continue-on-error: true
    run: "trivy image $MIABI_IMAGE"
  - name: deploy
    uses: deploy
`
	s, err := ParseSpec([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !s.Steps[0].ContinueOnError {
		t.Error("continue-on-error not parsed on the scan step")
	}
	if s.Steps[1].ContinueOnError {
		t.Error("continue-on-error should default to false when omitted")
	}
}

func TestParseSpecAcceptsBuildStep(t *testing.T) {
	const y = `
apiVersion: miabi.io/v1
kind: Pipeline
metadata: { name: web }
steps:
  - name: build
    uses: build
    dockerfile: docker/Dockerfile
  - name: deploy
    uses: deploy
`
	s, err := ParseSpec([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Steps[0].Uses != UsesBuild {
		t.Errorf("build step not recognized: %q", s.Steps[0].Uses)
	}
	if s.Steps[0].Dockerfile != "docker/Dockerfile" {
		t.Errorf("dockerfile not parsed: %q", s.Steps[0].Dockerfile)
	}
}

func TestParseSpecRejectsDockerfileOnNonBuildStep(t *testing.T) {
	const y = `
apiVersion: miabi.io/v1
kind: Pipeline
metadata: { name: x }
steps:
  - name: test
    image: node:20
    run: "npm test"
    dockerfile: Dockerfile
`
	if _, err := ParseSpec([]byte(y)); err == nil {
		t.Fatal("expected error: dockerfile is only valid on a build step")
	}
}

func TestParseSpecRejectsUnknownBuiltin(t *testing.T) {
	const y = `
apiVersion: miabi.io/v1
kind: Pipeline
metadata: { name: x }
steps:
  - name: nope
    uses: teleport
`
	if _, err := ParseSpec([]byte(y)); err == nil {
		t.Fatal("expected error for unknown built-in step")
	}
}

func TestParseSpecRequiresImageOrUses(t *testing.T) {
	const y = `
apiVersion: miabi.io/v1
kind: Pipeline
metadata: { name: x }
steps:
  - name: empty
    run: "echo hi"
`
	if _, err := ParseSpec([]byte(y)); err == nil {
		t.Fatal("expected error for step with neither image nor uses")
	}
}

func TestParseSpecRejectsDuplicateStepNames(t *testing.T) {
	const y = `
apiVersion: miabi.io/v1
kind: Pipeline
metadata: { name: x }
steps:
  - name: a
    image: busybox
  - name: a
    image: busybox
`
	if _, err := ParseSpec([]byte(y)); err == nil {
		t.Fatal("expected error for duplicate step names")
	}
}

// A build path is joined against the runner's workdir, so it must not be able to
// climb out of the checked-out source.
func TestParseSpecRejectsEscapingBuildPaths(t *testing.T) {
	for _, bad := range []string{"/etc/passwd", "../../etc/passwd", "a/../../.."} {
		for _, field := range []string{"dockerfile", "context"} {
			y := "apiVersion: miabi.io/v1\nkind: Pipeline\nmetadata: { name: web }\nsteps:\n  - name: build\n    uses: build\n    " + field + ": " + bad + "\n"
			if _, err := ParseSpec([]byte(y)); err == nil {
				t.Errorf("%s: %q was accepted — it resolves outside the repository", field, bad)
			}
		}
	}
}

// Until the runner protocol carries it, `context:` must fail loudly rather than
// be accepted and dropped — silently building from the wrong directory produces a
// wrong image and blames nothing. Delete this test with the guard it covers.
func TestParseSpecRejectsContextUntilRunnerSupport(t *testing.T) {
	y := "apiVersion: miabi.io/v1\nkind: Pipeline\nmetadata: { name: web }\nsteps:\n  - name: build\n    uses: build\n    context: services/api\n"
	_, err := ParseSpec([]byte(y))
	if err == nil {
		t.Fatal("context was accepted, but nothing forwards it to the runner yet")
	}
	if !strings.Contains(err.Error(), "newer runner") {
		t.Errorf("error should say what to do, got: %v", err)
	}
}

// Build-arg names have to be usable from the Dockerfile. Docker accepts a name
// with "=" or a space and then produces an ARG nothing can reference, so the
// pipeline rejects it at parse time instead.
func TestParseSpecValidatesBuildArgNames(t *testing.T) {
	spec := func(k string) []byte {
		return []byte("apiVersion: miabi.io/v1\nkind: Pipeline\nmetadata: { name: web }\nsteps:\n" +
			"  - name: build\n    uses: build\n    build-args:\n      \"" + k + "\": v\n")
	}
	for _, bad := range []string{"1VERSION", "APP ENV", "APP-ENV", "APP=ENV", ""} {
		if _, err := ParseSpec(spec(bad)); err == nil {
			t.Errorf("build-arg name %q was accepted", bad)
		} else if strings.Contains(err.Error(), "newer runner") {
			t.Errorf("build-arg name %q reached the runner gate; it should fail validation first", bad)
		}
	}
	// A well-formed name gets past validation and stops at the runner gate.
	if _, err := ParseSpec(spec("APP_ENV2")); err == nil || !strings.Contains(err.Error(), "newer runner") {
		t.Errorf("valid build-arg name should reach the runner gate, got: %v", err)
	}
}

// build-args belongs to a build step, like dockerfile and context.
func TestParseSpecRejectsBuildArgsOnNonBuildStep(t *testing.T) {
	y := []byte("apiVersion: miabi.io/v1\nkind: Pipeline\nmetadata: { name: web }\nsteps:\n" +
		"  - name: test\n    image: node:20\n    run: npm test\n    build-args:\n      A: b\n")
	if _, err := ParseSpec(y); err == nil {
		t.Fatal("expected error: build-args is only valid on a build step")
	}
}
