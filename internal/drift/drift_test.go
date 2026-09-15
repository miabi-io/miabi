// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package drift

import (
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

func TestOwnerOf(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		wantKind string
		wantID   uint
		wantOK   bool
	}{
		{"app", map[string]string{docker.LabelApp: "42"}, OwnerApp, 42, true},
		{"database", map[string]string{docker.LabelDatabase: "7"}, OwnerDatabase, 7, true},
		{"volume", map[string]string{docker.LabelVolume: "3"}, OwnerVolume, 3, true},
		{"stack only", map[string]string{docker.LabelStack: "9"}, OwnerStack, 9, true},
		// An app container carries both app + stack labels; the app is the owner.
		{"app wins over stack", map[string]string{docker.LabelApp: "42", docker.LabelStack: "9"}, OwnerApp, 42, true},
		{"gateway role is infra", map[string]string{docker.LabelRole: "node-gateway", docker.LabelWorkspace: "1"}, "", 0, false},
		{"redis role is infra", map[string]string{docker.LabelRole: "node-gateway-redis"}, "", 0, false},
		// Even with an app label, a role-tagged resource is infra and not orphan-eligible.
		{"role beats app", map[string]string{docker.LabelRole: "node-gateway", docker.LabelApp: "42"}, "", 0, false},
		{"job not orphan-eligible", map[string]string{docker.LabelJob: "5", docker.LabelApp: "42"}, "", 0, false},
		{"unmanaged", map[string]string{"com.docker.compose.project": "x"}, "", 0, false},
		{"empty", nil, "", 0, false},
		{"bad id", map[string]string{docker.LabelApp: "not-a-number"}, OwnerApp, 0, false},
		{"zero id", map[string]string{docker.LabelApp: "0"}, OwnerApp, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, id, ok := OwnerOf(tt.labels)
			if ok != tt.wantOK || id != tt.wantID || (tt.wantOK && kind != tt.wantKind) {
				t.Fatalf("OwnerOf(%v) = (%q, %d, %v), want (%q, %d, %v)",
					tt.labels, kind, id, ok, tt.wantKind, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestVolumeOwner(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		wantKind string
		wantID   uint
		wantOK   bool
	}{
		{"volume row", map[string]string{docker.LabelVolume: "3", docker.LabelWorkspace: "1"}, OwnerVolume, 3, true},
		{"database data volume", map[string]string{docker.LabelDatabase: "5", docker.LabelWorkspace: "1"}, OwnerDatabase, 5, true},
		{"volume label wins", map[string]string{docker.LabelVolume: "3", docker.LabelDatabase: "5"}, OwnerVolume, 3, true},
		// Deleting an app or a stack never makes its data volume an orphan by label.
		{"app label is not an owner", map[string]string{docker.LabelApp: "42"}, "", 0, false},
		{"stack label is not an owner", map[string]string{docker.LabelStack: "9"}, "", 0, false},
		{"infra", map[string]string{docker.LabelRole: "gateway", docker.LabelVolume: "3"}, "", 0, false},
		{"workspace only", map[string]string{docker.LabelWorkspace: "1"}, "", 0, false},
		{"bad id", map[string]string{docker.LabelVolume: "x"}, OwnerVolume, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, id, ok := VolumeOwner(tt.labels)
			if ok != tt.wantOK || id != tt.wantID || (tt.wantOK && kind != tt.wantKind) {
				t.Fatalf("VolumeOwner(%v) = (%q, %d, %v), want (%q, %d, %v)",
					tt.labels, kind, id, ok, tt.wantKind, tt.wantID, tt.wantOK)
			}
		})
	}
}
