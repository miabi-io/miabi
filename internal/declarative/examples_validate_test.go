// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative_test

import (
	"os"
	"path/filepath"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/services/pipeline"
)

// examplesDir is the module's examples folder, relative to this package.
const examplesDir = "../../examples"

func TestExamplesParse(t *testing.T) {
	if _, err := os.Stat(examplesDir); err != nil {
		t.Skip("examples directory not present; skipping example validation")
	}
	// Project bundle via Parse.
	data, err := os.ReadFile(filepath.Join(examplesDir, "apply", "project.yaml"))
	if err != nil {
		t.Fatalf("read project.yaml: %v", err)
	}
	set, err := d.Parse(data)
	if err != nil {
		t.Fatalf("parse apply/project.yaml: %v", err)
	}
	for _, k := range []d.Kind{d.KindApplication, d.KindDatabase, d.KindVolume, d.KindSecret, d.KindRoute} {
		if len(set.ByKind(k)) == 0 {
			t.Errorf("project.yaml missing a %s", k)
		}
	}

	// Domain/route bundle (multi-document) via Parse — Parse also runs
	// cross-reference validation, so each Route must target the included app.
	if dom, derr := os.ReadFile(filepath.Join(examplesDir, "apply", "domain.yaml")); derr != nil {
		t.Fatalf("read domain.yaml: %v", derr)
	} else if domSet, perr := d.Parse(dom); perr != nil {
		t.Fatalf("parse apply/domain.yaml: %v", perr)
	} else {
		for _, k := range []d.Kind{d.KindDomain, d.KindRoute} {
			if len(domSet.ByKind(k)) == 0 {
				t.Errorf("domain.yaml should declare at least one %s", k)
			}
		}
	}

	// App port-exposure bundle.
	if pb, derr := os.ReadFile(filepath.Join(examplesDir, "apply", "app-ports.yaml")); derr != nil {
		t.Fatalf("read app-ports.yaml: %v", derr)
	} else if _, perr := d.Parse(pb); perr != nil {
		t.Fatalf("parse apply/app-ports.yaml: %v", perr)
	}

	// Private-registry bundle: the app's spec.registry must name the declared
	// credential, so the example can't drift out of sync with the schema.
	if pr, derr := os.ReadFile(filepath.Join(examplesDir, "apply", "private-registry.yaml")); derr != nil {
		t.Fatalf("read private-registry.yaml: %v", derr)
	} else if prSet, perr := d.Parse(pr); perr != nil {
		t.Fatalf("parse apply/private-registry.yaml: %v", perr)
	} else {
		regs := prSet.ByKind(d.KindRegistry)
		if len(regs) == 0 {
			t.Fatal("private-registry.yaml should declare a Registry")
		}
		apps := prSet.ByKind(d.KindApplication)
		if len(apps) == 0 || apps[0].Application.Registry != regs[0].Metadata.Name {
			t.Errorf("private-registry.yaml app should reference registry %q", regs[0].Metadata.Name)
		}
	}

	// GitOps env folders via ParseFS.
	for _, env := range []string{"dev", "prod"} {
		dir := filepath.Join(examplesDir, "gitops", "envs", env)
		if _, err := d.ParseFS(os.DirFS(dir), "."); err != nil {
			t.Errorf("parse gitops/envs/%s: %v", env, err)
		}
	}

	// GitOps single-app example (mirrors the okapi-example marketplace template).
	if _, err := d.ParseFS(os.DirFS(filepath.Join(examplesDir, "gitops", "okapi-example")), "."); err != nil {
		t.Errorf("parse gitops/okapi-example: %v", err)
	}

	// Pipeline specs.
	for _, f := range []string{"pipeline.yaml", "pipeline-multistage.yaml"} {
		b, err := os.ReadFile(filepath.Join(examplesDir, "pipeline", f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := pipeline.ParseSpec(b); err != nil {
			t.Errorf("parse pipeline/%s: %v", f, err)
		}
	}
}
