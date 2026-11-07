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

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// fakePlacer records what was asked of it and answers with a fixed device path.
type fakePlacer struct {
	devicePath string
	err        error
	placed     []string // dockerNames it was asked to place
	reclaimed  []string
}

func (f *fakePlacer) PlaceVolume(_ context.Context, _, _ uint, requested, dockerName string) (string, string, error) {
	if f.err != nil {
		return "", "", f.err
	}
	f.placed = append(f.placed, dockerName)
	if requested == "" {
		return models.DefaultStorageClassName, "", nil
	}
	return requested, f.devicePath, nil
}

func (f *fakePlacer) ReclaimVolumeDir(_ context.Context, _ uint, className, dockerName string) {
	f.reclaimed = append(f.reclaimed, className+":"+dockerName)
}

func instOn(class string) *models.DatabaseInstance {
	return &models.DatabaseInstance{
		ID: 7, WorkspaceID: 3, ServerID: 1,
		VolumeName: "mb-db-abc-7-data", VolumeSizeBytes: 1024,
		StorageClassName: class,
	}
}

func TestDataVolumeSpec_BindsTheClassDirectory(t *testing.T) {
	placer := &fakePlacer{devicePath: "/mnt/ssd1/miabi/mb-db-abc-7-data"}
	s := &Service{storage: placer}

	spec := s.dataVolumeSpec(instOn("ssd-fast"))

	if spec.DevicePath != placer.devicePath {
		t.Fatalf("device path = %q, want %q", spec.DevicePath, placer.devicePath)
	}
	if spec.Name != "mb-db-abc-7-data" || spec.SizeBytes != 1024 {
		t.Errorf("spec lost the volume's identity: %+v", spec)
	}
	if spec.Labels[docker.LabelDatabase] != "7" || spec.Labels[docker.LabelWorkspace] != "3" {
		t.Errorf("labels = %v, want the database and workspace ids", spec.Labels)
	}
}

// The default class is Docker's own data root, so there is no directory to bind.
func TestDataVolumeSpec_DefaultClassBindsNothing(t *testing.T) {
	for _, class := range []string{models.DefaultStorageClassName, ""} {
		placer := &fakePlacer{devicePath: "/mnt/ssd1/miabi/x"}
		s := &Service{storage: placer}

		spec := s.dataVolumeSpec(instOn(class))

		if spec.DevicePath != "" {
			t.Errorf("class %q: device path = %q, want none", class, spec.DevicePath)
		}
		if len(placer.placed) != 0 {
			t.Errorf("class %q: asked the class service to place %v, want nothing", class, placer.placed)
		}
	}
}

// A vanished class must not stop a database starting: falling back to the data root keeps it up.
func TestDataVolumeSpec_UnavailableClassFallsBack(t *testing.T) {
	s := &Service{storage: &fakePlacer{err: errors.New("no such class on this node")}}

	spec := s.dataVolumeSpec(instOn("ssd-gone"))

	if spec.DevicePath != "" {
		t.Fatalf("device path = %q, want the data root", spec.DevicePath)
	}
	if spec.Name != "mb-db-abc-7-data" {
		t.Fatalf("spec = %+v, want the volume still named", spec)
	}
}

// Without placement wired at all, every database lands where it always did.
func TestDataVolumeSpec_Unwired(t *testing.T) {
	s := &Service{}

	spec := s.dataVolumeSpec(instOn("ssd-fast"))

	if spec.DevicePath != "" {
		t.Fatalf("device path = %q, want none when placement is not wired", spec.DevicePath)
	}
}
