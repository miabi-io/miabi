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

package registryserver

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type volDocker struct {
	docker.Client
	existing map[string]bool
	created  []docker.VolumeSpec
}

func (v *volDocker) InspectVolume(_ context.Context, name string) (docker.Volume, error) {
	if v.existing[name] {
		return docker.Volume{Name: name}, nil
	}
	return docker.Volume{}, docker.ErrNotFound
}

func (v *volDocker) CreateVolumeWith(_ context.Context, spec docker.VolumeSpec) (docker.Volume, error) {
	v.created = append(v.created, spec)
	return docker.Volume{Name: spec.Name}, nil
}

type fakeClasses struct {
	classes map[string]*models.StorageClass
	dirs    []string
}

func (f *fakeClasses) Resolve(_ uint, name string) (*models.StorageClass, error) {
	if c, ok := f.classes[name]; ok {
		return c, nil
	}
	return nil, errors.New("storage class not found")
}

func (f *fakeClasses) EnsureDir(_ context.Context, _ *models.StorageClass, dockerName string) error {
	f.dirs = append(f.dirs, dockerName)
	return nil
}

func (f *fakeClasses) LocalID() uint { return 1 }

func hddClasses() *fakeClasses {
	return &fakeClasses{classes: map[string]*models.StorageClass{
		"hdd": {Name: "hdd", Path: "/mnt/hdd/miabi", Enabled: true},
	}}
}

func TestDataVolumeFollowsStorageClass(t *testing.T) {
	cases := map[string]string{
		"":        models.DefaultRegistryVolume,
		"default": models.DefaultRegistryVolume,
		"hdd":     models.DefaultRegistryVolume + "-hdd",
	}
	for class, want := range cases {
		st := &models.RegistrySettings{StorageClass: class}
		if got := st.DataVolume(); got != want {
			t.Errorf("DataVolume(%q) = %q, want %q", class, got, want)
		}
		if !models.IsRegistryDataVolume(want) {
			t.Errorf("IsRegistryDataVolume(%q) = false, want true so platform backups skip it", want)
		}
	}
}

func TestStorageClassEnvPinsTheStoredValue(t *testing.T) {
	svc := &Service{cfg: config.RegistryConfig{StorageClass: "hdd"}}
	st := &models.RegistrySettings{StorageClass: "ssd"}
	svc.applyEnvConfig(st)
	if st.StorageClass != "hdd" || st.VolumeName != models.DefaultRegistryVolume+"-hdd" {
		t.Errorf("class = %q volume = %q, want hdd on its own volume", st.StorageClass, st.VolumeName)
	}
	if !svc.Locks().StorageClass {
		t.Error("Locks().StorageClass = false, want the env-set class locked")
	}
}

func TestDataVolumeIsCreatedOnTheClassPath(t *testing.T) {
	classes := hddClasses()
	svc := &Service{classes: classes}
	dc := &volDocker{}
	st := &models.RegistrySettings{StorageClass: "hdd"}

	if err := svc.ensureDataVolume(context.Background(), dc, st); err != nil {
		t.Fatal(err)
	}
	if len(dc.created) != 1 {
		t.Fatalf("created %d volumes, want 1", len(dc.created))
	}
	got := dc.created[0]
	if got.Name != "mb-registry-data-hdd" || got.DevicePath != "/mnt/hdd/miabi/mb-registry-data-hdd" {
		t.Errorf("volume = %q on %q, want mb-registry-data-hdd under the class path", got.Name, got.DevicePath)
	}
	if len(classes.dirs) != 1 || classes.dirs[0] != "mb-registry-data-hdd" {
		t.Errorf("prepared dirs = %v, want the volume directory created before the volume", classes.dirs)
	}
}

func TestExistingDataVolumeIsReused(t *testing.T) {
	classes := &fakeClasses{}
	svc := &Service{classes: classes}
	dc := &volDocker{existing: map[string]bool{"mb-registry-data-hdd": true}}

	if err := svc.ensureDataVolume(context.Background(), dc, &models.RegistrySettings{StorageClass: "hdd"}); err != nil {
		t.Fatalf("an existing volume must not need its class to resolve: %v", err)
	}
	if len(dc.created) != 0 {
		t.Errorf("created %v, want the existing volume reused", dc.created)
	}
}

// Falling back to the data root would put blobs on the boot disk that vanish once the class returns.
func TestUnavailableClassRefusesToStart(t *testing.T) {
	svc := &Service{classes: &fakeClasses{}}
	dc := &volDocker{}

	if err := svc.ensureDataVolume(context.Background(), dc, &models.RegistrySettings{StorageClass: "hdd"}); err == nil {
		t.Fatal("ensureDataVolume succeeded, want an error for an unknown class")
	}
	if len(dc.created) != 0 {
		t.Errorf("created %v, want nothing in the data root", dc.created)
	}
}

func TestStorageClassUserGuardsDeletion(t *testing.T) {
	svc := &Service{cfg: config.RegistryConfig{StorageClass: "hdd"}}
	if svc.StorageClassUser("hdd") == "" {
		t.Error("StorageClassUser(hdd) = \"\", want the registry named as a user")
	}
	if got := svc.StorageClassUser("ssd"); got != "" {
		t.Errorf("StorageClassUser(ssd) = %q, want \"\"", got)
	}
	s3 := &Service{cfg: config.RegistryConfig{StorageClass: "hdd", StorageType: "s3"}}
	if got := s3.StorageClassUser("hdd"); got != "" {
		t.Errorf("StorageClassUser on S3 = %q, want \"\": no local volume uses the class", got)
	}
}
