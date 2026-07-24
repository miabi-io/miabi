// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestAppResourceEmitsMounts(t *testing.T) {
	app := &models.Application{
		Name:  "guestbook",
		Image: "ghcr.io/miabi-io/guestbook",
		Mounts: []models.AppMount{
			{VolumeID: 7, DockerName: "mb-vol-7", Path: "/data"},
			{HostPreset: "docker-sock", Path: "/var/run/docker.sock"}, // VolumeID 0 → skipped
		},
	}
	volNameByID := map[uint]string{7: "guestbook-data"}

	res := appResource(app, map[int]bool{}, map[int]bool{}, volNameByID)
	got := res.Application.Mounts
	if len(got) != 1 {
		t.Fatalf("want 1 mount (host-preset omitted), got %d: %+v", len(got), got)
	}
	if got[0].Volume != "guestbook-data" || got[0].Path != "/data" {
		t.Errorf("mount = %+v, want {Volume: guestbook-data, Path: /data}", got[0])
	}
}
