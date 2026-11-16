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

package apply

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/declarative"
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

	res := appResource(app, map[int]bool{}, map[int]bool{}, volNameByID, nil, nil)
	got := res.Application.Mounts
	if len(got) != 1 {
		t.Fatalf("want 1 mount (host-preset omitted), got %d: %+v", len(got), got)
	}
	if got[0].Volume != "guestbook-data" || got[0].Path != "/data" {
		t.Errorf("mount = %+v, want {Volume: guestbook-data, Path: /data}", got[0])
	}
}

// Create resolves mounts first: an app created before its mount failed was left without it.
func TestCreateRefusesAMissingMountBeforeTheAppExists(t *testing.T) {
	s := &Service{}
	spec := &declarative.ApplicationSpec{Image: "busybox", Tag: "1.36",
		Mounts: []declarative.MountSpec{{Volume: "ceph-data", Path: "/data"}}}
	desired := declarative.Resource{APIVersion: declarative.APIVersion, Kind: declarative.KindApplication,
		Metadata: declarative.Meta{Name: "ceph-probe"}, Application: spec}
	ch := declarative.Change{Action: declarative.ActionCreate, Kind: declarative.KindApplication, Name: "ceph-probe"}

	err := s.applyApplication(context.Background(), 1, ch, desired, &declarative.ResourceSet{})
	if !errors.Is(err, ErrInvalidManifest) || !strings.Contains(err.Error(), `volume "ceph-data" not found`) {
		t.Fatalf("err = %v, want an invalid-manifest error naming the volume", err)
	}
}
