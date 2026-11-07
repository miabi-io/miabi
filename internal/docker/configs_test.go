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

	// A content change must yield a different object, since configs are immutable.
	changed := base
	changed.Digest = "ffffffffffff"
	if changed.ObjectName() == name {
		t.Error("the object name ignores the digest")
	}
}

// A name that loses its digest would collide across content versions.
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
