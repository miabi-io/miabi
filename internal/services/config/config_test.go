// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestDigestIsOrderIndependent(t *testing.T) {
	a := Digest(map[string]string{"a.txt": "one", "b.txt": "two"})
	b := Digest(map[string]string{"b.txt": "two", "a.txt": "one"})
	if a != b {
		t.Fatalf("digest depends on key order: %s vs %s", a, b)
	}
	if c := Digest(map[string]string{"a.txt": "one", "b.txt": "three"}); c == a {
		t.Fatal("digest ignored a content change")
	}
	// Key and content are separated, so moving bytes across the boundary changes it.
	if Digest(map[string]string{"ab.txt": "x"}) == Digest(map[string]string{"a.txt": "b.txtx"}) {
		t.Fatal("digest is ambiguous across the key/content boundary")
	}
}

func TestValidateData(t *testing.T) {
	if err := ValidateData(nil); err == nil {
		t.Error("empty data should be rejected")
	}
	if err := ValidateData(map[string]string{"../escape": "x"}); err == nil {
		t.Error("traversal key should be rejected")
	}
	if err := ValidateData(map[string]string{"/abs": "x"}); err == nil {
		t.Error("absolute key should be rejected")
	}
	if err := ValidateData(map[string]string{"ok/nested.yml": "x"}); err != nil {
		t.Errorf("nested key rejected: %v", err)
	}
	if err := ValidateData(map[string]string{"big.txt": strings.Repeat("x", MaxFileBytes+1)}); err == nil {
		t.Error("oversized file should be rejected")
	}
	over := map[string]string{}
	for _, k := range []string{"a", "b", "c"} {
		over[k+".txt"] = strings.Repeat("x", MaxFileBytes)
	}
	if err := ValidateData(over); err == nil {
		t.Error("oversized total should be rejected")
	}
}

func TestValidateMode(t *testing.T) {
	for _, m := range []string{"", "644", "0644", "0444"} {
		if err := ValidateMode(m); err != nil {
			t.Errorf("mode %q: %v", m, err)
		}
	}
	for _, m := range []string{"04755", "2755", "1755", "0999", "64", "abcd"} {
		if err := ValidateMode(m); err == nil {
			t.Errorf("mode %q should be rejected", m)
		}
	}
}

// Projection is the only place a mount becomes files, so both forms are pinned.
func TestProjectDirectoryPrefixAndSingleKey(t *testing.T) {
	svc := &Service{}
	cfg := &models.Config{Mode: "0644"}
	data := map[string]string{"prometheus.yml": "global: {}", "rules/alerts.yml": "groups: []"}

	files, err := svc.project(cfg, data, models.AppMount{Path: "/etc/prometheus"})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	// Sorted by key, so the projection is deterministic across deploys.
	if files[0].Path != "/etc/prometheus/prometheus.yml" || files[1].Path != "/etc/prometheus/rules/alerts.yml" {
		t.Fatalf("unexpected paths: %s, %s", files[0].Path, files[1].Path)
	}

	one, err := svc.project(cfg, data, models.AppMount{Path: "/etc/prometheus/rules/alerts.yml", ConfigKey: "rules/alerts.yml", Mode: "0444"})
	if err != nil {
		t.Fatalf("project single: %v", err)
	}
	if len(one) != 1 || one[0].Path != "/etc/prometheus/rules/alerts.yml" {
		t.Fatalf("unexpected single projection: %+v", one)
	}
	if one[0].Mode != "0444" {
		t.Errorf("mount mode did not override the config default: %q", one[0].Mode)
	}

	if _, err := svc.project(cfg, data, models.AppMount{Path: "/x", ConfigKey: "missing.yml"}); err == nil {
		t.Error("a mount naming an absent key should fail")
	}
}

// Two mounts of the same key at different modes must not collide on one
// materialized path, which is why the file digest covers mode as well.
func TestFileDigestCoversMode(t *testing.T) {
	a := ProjectedFile{Path: "/x", Content: "same", Mode: "0644"}
	b := ProjectedFile{Path: "/x", Content: "same", Mode: "0444"}
	if a.FileDigest() == b.FileDigest() {
		t.Fatal("file digest ignores mode, so differing modes would share a path")
	}
	if a.FileDigest() != (ProjectedFile{Path: "/other", Content: "same", Mode: "0644"}).FileDigest() {
		t.Fatal("file digest should not depend on the mount path")
	}
}
