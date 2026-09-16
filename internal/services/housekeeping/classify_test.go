// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package housekeeping

import "testing"

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
