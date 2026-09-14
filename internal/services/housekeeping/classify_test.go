// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package housekeeping

import "testing"

func TestOwnerOf(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		wantKind string
		wantID   uint
		wantOK   bool
	}{
		{"app", map[string]string{labelApp: "42"}, OwnerApp, 42, true},
		{"database", map[string]string{labelDatabase: "7"}, OwnerDatabase, 7, true},
		{"volume", map[string]string{labelVolume: "3"}, OwnerVolume, 3, true},
		{"stack only", map[string]string{labelStack: "9"}, OwnerStack, 9, true},
		// An app container carries both app + stack labels; the app is the owner.
		{"app wins over stack", map[string]string{labelApp: "42", labelStack: "9"}, OwnerApp, 42, true},
		{"gateway role is infra", map[string]string{labelRole: "node-gateway", labelWorkspace: "1"}, "", 0, false},
		{"redis role is infra", map[string]string{labelRole: "node-gateway-redis"}, "", 0, false},
		// Even with an app label, a role-tagged resource is infra and not orphan-eligible.
		{"role beats app", map[string]string{labelRole: "node-gateway", labelApp: "42"}, "", 0, false},
		{"job not orphan-eligible", map[string]string{labelJob: "5", labelApp: "42"}, "", 0, false},
		{"unmanaged", map[string]string{"com.docker.compose.project": "x"}, "", 0, false},
		{"empty", nil, "", 0, false},
		{"bad id", map[string]string{labelApp: "not-a-number"}, OwnerApp, 0, false},
		{"zero id", map[string]string{labelApp: "0"}, OwnerApp, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, id, ok := ownerOf(tt.labels)
			if ok != tt.wantOK || id != tt.wantID || (tt.wantOK && kind != tt.wantKind) {
				t.Fatalf("ownerOf(%v) = (%q, %d, %v), want (%q, %d, %v)",
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
		{"volume row", map[string]string{labelVolume: "3", labelWorkspace: "1"}, OwnerVolume, 3, true},
		{"database data volume", map[string]string{labelDatabase: "5", labelWorkspace: "1"}, OwnerDatabase, 5, true},
		{"volume label wins", map[string]string{labelVolume: "3", labelDatabase: "5"}, OwnerVolume, 3, true},
		// Deleting an app or a stack never makes its data volume an orphan by label.
		{"app label is not an owner", map[string]string{labelApp: "42"}, "", 0, false},
		{"stack label is not an owner", map[string]string{labelStack: "9"}, "", 0, false},
		{"infra", map[string]string{labelRole: "gateway", labelVolume: "3"}, "", 0, false},
		{"workspace only", map[string]string{labelWorkspace: "1"}, "", 0, false},
		{"bad id", map[string]string{labelVolume: "x"}, OwnerVolume, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, id, ok := volumeOwner(tt.labels)
			if ok != tt.wantOK || id != tt.wantID || (tt.wantOK && kind != tt.wantKind) {
				t.Fatalf("volumeOwner(%v) = (%q, %d, %v), want (%q, %d, %v)",
					tt.labels, kind, id, ok, tt.wantKind, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestIsMiabiVolumeName(t *testing.T) {
	for name, want := range map[string]bool{
		"mb-vol-3-uploads":    true,
		"mb-vol-12-my-data-2": true,
		"mb-vol-3-":           false,
		"mb-vol-3":            false,
		"mb-vol-0-uploads":    false,
		"mb-vol-x-uploads":    false,
		"mb-backups-3":        false,
		"mb-db-k3x9-5-data":   false,
		"uploads":             false,
		"":                    false,
	} {
		if got := isMiabiVolumeName(name); got != want {
			t.Errorf("isMiabiVolumeName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestIsManaged(t *testing.T) {
	cases := []struct {
		labels map[string]string
		want   bool
	}{
		{map[string]string{labelApp: "1"}, true},
		{map[string]string{"io.miabi.deployment": "3"}, true},
		{map[string]string{labelRole: "node-gateway"}, true},
		{map[string]string{"com.docker.compose.service": "web"}, false},
		{nil, false},
		{map[string]string{}, false},
	}
	for _, c := range cases {
		if got := isManaged(c.labels); got != c.want {
			t.Errorf("isManaged(%v) = %v, want %v", c.labels, got, c.want)
		}
	}
}

func TestIsPlatformInfra(t *testing.T) {
	if !isPlatformInfra(map[string]string{labelRole: "node-gateway"}) {
		t.Error("gateway role should be platform infra")
	}
	if isPlatformInfra(map[string]string{labelApp: "1"}) {
		t.Error("an app is not platform infra")
	}
}
