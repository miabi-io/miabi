// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"strings"
	"testing"
)

func TestConfigObjectNameIsContentAddressed(t *testing.T) {
	base := ConfigObject{Workspace: "7", Config: "prom-conf", Key: "rules/alerts.yml", Digest: "abc123def456"}
	name := base.ObjectName()
	if !strings.HasSuffix(name, "-abc123def456") {
		t.Fatalf("digest is not the suffix: %s", name)
	}
	if strings.Contains(strings.TrimSuffix(name, "-abc123def456"), "/") {
		t.Errorf("the key slug still contains a path separator: %s", name)
	}

	// A content change must yield a different object, which is what makes the
	// service update swap it rather than mutate an immutable object.
	changed := base
	changed.Digest = "ffffffffffff"
	if changed.ObjectName() == name {
		t.Error("the object name ignores the digest")
	}
}

// Truncation must eat the middle: a name that loses its digest would collide
// across content versions.
func TestConfigObjectNameTruncationKeepsDigest(t *testing.T) {
	long := ConfigObject{
		Workspace: "1234",
		Config:    strings.Repeat("very-long-config-name", 3),
		Key:       strings.Repeat("deeply/nested/path/", 4) + "file.yml",
		Digest:    "0123456789ab",
	}
	name := long.ObjectName()
	if len(name) > maxConfigNameLen {
		t.Fatalf("name is %d chars, over the %d limit: %s", len(name), maxConfigNameLen, name)
	}
	if !strings.HasSuffix(name, "-0123456789ab") {
		t.Fatalf("truncation dropped the digest: %s", name)
	}
}
