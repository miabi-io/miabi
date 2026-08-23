// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package marketplace

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
)

func TestImageRef(t *testing.T) {
	if got := imageRef(manifest.AppSpec{Image: "ghcr.io/a/b", Tag: "1.2"}); got != "ghcr.io/a/b:1.2" {
		t.Errorf("with tag: got %q", got)
	}
	if got := imageRef(manifest.AppSpec{Image: "redis"}); got != "redis" {
		t.Errorf("no tag: got %q", got)
	}
}

func TestDiffEnv(t *testing.T) {
	oldA := manifest.AppSpec{Env: map[string]string{"KEEP": "1", "CHANGE": "old", "GONE": "x"}}
	newA := manifest.AppSpec{
		Env:       map[string]string{"KEEP": "1", "CHANGE": "new", "ADD": "{{ .inputs.X }}"},
		SecretEnv: []string{"ADD"},
	}
	var warnings []string
	got := diffEnv(oldA, newA, &warnings)

	kinds := map[string]EnvChange{}
	for _, c := range got {
		kinds[c.Key] = c
	}
	if _, ok := kinds["KEEP"]; ok {
		t.Error("unchanged key should not appear in the diff")
	}
	if kinds["CHANGE"].Kind != "changed" {
		t.Errorf("CHANGE: %+v", kinds["CHANGE"])
	}
	if kinds["GONE"].Kind != "removed" {
		t.Errorf("GONE: %+v", kinds["GONE"])
	}
	add := kinds["ADD"]
	if add.Kind != "added" || !add.Secret || !add.Templated {
		t.Errorf("ADD should be added+secret+templated, got %+v", add)
	}
}

func TestDiffStackEnv(t *testing.T) {
	oldM := &manifest.Manifest{Stack: &manifest.StackSpec{
		Env: map[string]string{"KEEP": "1", "CHANGE": "old", "GONE": "x"},
	}}
	newM := &manifest.Manifest{Stack: &manifest.StackSpec{
		Env:       map[string]string{"KEEP": "1", "CHANGE": "new", "ADD": "{{ .databases.db.host }}"},
		SecretEnv: []string{"ADD"},
	}}
	kinds := map[string]EnvChange{}
	for _, c := range diffStackEnv(oldM, newM) {
		kinds[c.Key] = c
	}
	if _, ok := kinds["KEEP"]; ok {
		t.Error("unchanged shared key should not appear in the diff")
	}
	if kinds["CHANGE"].Kind != "changed" {
		t.Errorf("CHANGE: %+v", kinds["CHANGE"])
	}
	if kinds["GONE"].Kind != "removed" {
		t.Errorf("GONE: %+v", kinds["GONE"])
	}
	if add := kinds["ADD"]; add.Kind != "added" || !add.Secret || !add.Templated {
		t.Errorf("ADD should be added+secret+templated, got %+v", add)
	}

	// No stack on either side → no diff (and no panic on nil stacks).
	if got := diffStackEnv(&manifest.Manifest{}, &manifest.Manifest{}); len(got) != 0 {
		t.Errorf("expected no diff for stack-less manifests, got %v", got)
	}
}

func TestAddedAndMounts(t *testing.T) {
	old := map[string]bool{"a": true}
	cur := map[string]bool{"a": true, "b": true}
	got := added(old, cur)
	if len(got) != 1 || got[0] != "b" {
		t.Errorf("added = %v, want [b]", got)
	}

	oldA := manifest.AppSpec{Mounts: []manifest.Mount{{Volume: "data"}}}
	newA := manifest.AppSpec{Mounts: []manifest.Mount{{Volume: "data"}, {Volume: "cache"}}}
	nm := newMounts(oldA, newA)
	if len(nm) != 1 || nm[0] != "cache" {
		t.Errorf("newMounts = %v, want [cache]", nm)
	}
	if isMountNew(oldA, manifest.Mount{Volume: "data"}) {
		t.Error("data is not a new mount")
	}
	if !isMountNew(oldA, manifest.Mount{Volume: "cache"}) {
		t.Error("cache is a new mount")
	}
}

// A config mount carries no volume name, so mount identity has to include the
// config and its key — otherwise every config mount looks like the same one.
func TestMountIdentityWithConfigs(t *testing.T) {
	oldA := manifest.AppSpec{Mounts: []manifest.Mount{
		{Volume: "data", Path: "/etc/goma"},
	}}
	newA := manifest.AppSpec{Mounts: []manifest.Mount{
		{Volume: "data", Path: "/etc/goma"},
		{Config: "config", Key: "goma.yml", Path: "/etc/goma/goma.yml"},
	}}
	nm := newMounts(oldA, newA)
	if len(nm) != 1 || nm[0] != "config config/goma.yml" {
		t.Errorf("newMounts = %v, want [config config/goma.yml]", nm)
	}
	if !isMountNew(oldA, newA.Mounts[1]) {
		t.Error("the config mount is new")
	}
	if isMountNew(newA, newA.Mounts[1]) {
		t.Error("the config mount is not new against a spec that already has it")
	}
	// Two config mounts of the same config differ by key.
	if !isMountNew(newA, manifest.Mount{Config: "config", Key: "other.yml", Path: "/etc/goma/other.yml"}) {
		t.Error("a different key is a different mount")
	}
}

func TestDiffConfigs(t *testing.T) {
	oldM := &manifest.Manifest{Configs: []manifest.Config{
		{Name: "keep", Files: map[string]string{"a.yml": "1"}},
		{Name: "edited", Files: map[string]string{"b.yml": "old"}},
		{Name: "gone", Files: map[string]string{"c.yml": "x"}},
	}}
	newM := &manifest.Manifest{Configs: []manifest.Config{
		{Name: "keep", Files: map[string]string{"a.yml": "1"}},
		{Name: "edited", Files: map[string]string{"b.yml": "new"}},
		{Name: "fresh", Files: map[string]string{"d.yml": "y"}},
	}}
	var warnings []string
	got := diffConfigs(oldM, newM, &warnings)
	if len(got) != 1 || got[0] != "fresh" {
		t.Errorf("diffConfigs = %v, want [fresh]", got)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, `"edited"`) {
		t.Errorf("expected a warning for the edited config, got %v", warnings)
	}
	if !strings.Contains(joined, `"gone"`) {
		t.Errorf("expected a warning for the removed config, got %v", warnings)
	}
	if strings.Contains(joined, `"keep"`) {
		t.Errorf("an unchanged config should not warn, got %v", warnings)
	}
	// A template with no configs on either side diffs to nothing.
	if got := diffConfigs(&manifest.Manifest{}, &manifest.Manifest{}, &warnings); len(got) != 0 {
		t.Errorf("expected no new configs, got %v", got)
	}
}

func TestIsTemplated(t *testing.T) {
	if !isTemplated("{{ .inputs.X }}") || isTemplated("literal") {
		t.Error("isTemplated mismatch")
	}
}

// A server without the config service must degrade to a warning, not a panic,
// when the target version ships configuration files.
func TestUpgradeConfigsWithoutConfigService(t *testing.T) {
	svc := &Service{}
	newM := &manifest.Manifest{
		Metadata: manifest.Metadata{Name: "goma-gateway", Version: "1.1.0"},
		Configs:  []manifest.Config{{Name: "config", Files: map[string]string{"goma.yml": "version: \"2\""}}},
	}
	res := &UpgradeApplyResult{}
	ids := svc.upgradeConfigs(1, &manifest.Manifest{}, newM, manifest.NewRenderer(manifest.Context{}), res)
	if len(ids) != 0 {
		t.Errorf("no config can be created without the service, got %v", ids)
	}
	if len(res.Warnings) != 1 {
		t.Errorf("expected one warning, got %v", res.Warnings)
	}

	// A version that ships no configs says nothing at all.
	res = &UpgradeApplyResult{}
	svc.upgradeConfigs(1, &manifest.Manifest{}, &manifest.Manifest{}, manifest.NewRenderer(manifest.Context{}), res)
	if len(res.Warnings) != 0 {
		t.Errorf("expected no warnings, got %v", res.Warnings)
	}
}
