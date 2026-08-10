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

	// Label-routed bundle: the fronted app must actually carry the labels a
	// label-reading proxy discovers it by, and both apps must be in the stack
	// that gives them a shared network.
	if cl, derr := os.ReadFile(filepath.Join(examplesDir, "apply", "container-labels.yaml")); derr != nil {
		t.Fatalf("read container-labels.yaml: %v", derr)
	} else if clSet, perr := d.Parse([]byte(cl)); perr != nil {
		t.Fatalf("parse apply/container-labels.yaml: %v", perr)
	} else {
		labelled := 0
		for _, a := range clSet.ByKind(d.KindApplication) {
			if a.Application.Stack == "" {
				t.Errorf("application %q should join the stack it is routed on", a.Metadata.Name)
			}
			if len(a.Application.ContainerLabels) > 0 {
				labelled++
			}
		}
		if labelled == 0 {
			t.Error("container-labels.yaml should set containerLabels on the fronted app")
		}
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

	// Config bundle: every mount must resolve to a config in the bundle, and the
	// example must keep demonstrating both projection forms (whole set under a
	// directory, and a single pinned key) — that is what it exists to show.
	if cb, derr := os.ReadFile(filepath.Join(examplesDir, "apply", "config.yaml")); derr != nil {
		t.Fatalf("read config.yaml: %v", derr)
	} else if cbSet, perr := d.Parse(cb); perr != nil {
		t.Fatalf("parse apply/config.yaml: %v", perr)
	} else {
		if len(cbSet.ByKind(d.KindConfig)) == 0 {
			t.Fatal("config.yaml should declare at least one Config")
		}
		var dirMount, keyMount bool
		for _, a := range cbSet.ByKind(d.KindApplication) {
			for _, mt := range a.Application.Mounts {
				switch {
				case mt.Config == "":
				case mt.Key == "":
					dirMount = true
				default:
					keyMount = true
				}
			}
		}
		if !dirMount || !keyMount {
			t.Errorf("config.yaml should mount a config both as a directory and by key (dir=%v key=%v)", dirMount, keyMount)
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
