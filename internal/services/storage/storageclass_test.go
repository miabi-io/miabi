// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// fakeClasses is a storage-class catalog backed by a map, recording the directory calls the
// service makes so a test can assert that a create provisions one and a delete reclaims it.
type fakeClasses struct {
	byName      map[string]*models.StorageClass
	nodeDefault string
	ensured     []string
	removed     []string
	resolveEr   error
}

func (f *fakeClasses) Resolve(serverID uint, name string) (*models.StorageClass, error) {
	if f.resolveEr != nil {
		return nil, f.resolveEr
	}
	if name == "" {
		name = f.nodeDefault
	}
	c, ok := f.byName[name]
	if !ok {
		return nil, errors.New("storage class not found: " + name)
	}
	return c, nil
}

func (f *fakeClasses) EnsureDir(_ context.Context, c *models.StorageClass, dockerName string) error {
	f.ensured = append(f.ensured, c.Name+":"+dockerName)
	return nil
}

func (f *fakeClasses) RemoveDir(_ context.Context, c *models.StorageClass, dockerName string) error {
	f.removed = append(f.removed, c.Name+":"+dockerName)
	return nil
}

func (f *fakeClasses) DiskUsageAll(context.Context, *models.StorageClass) (map[string]int64, error) {
	return nil, nil
}

func (f *fakeClasses) ListManaged() ([]models.StorageClass, error) { return nil, nil }

func catalog() *fakeClasses {
	return &fakeClasses{
		byName: map[string]*models.StorageClass{
			models.DefaultStorageClassName: {Name: models.DefaultStorageClassName, Enabled: true, IsDefault: true},
			"ssd-fast":                     {Name: "ssd-fast", Path: "/mnt/ssd1/miabi", Enabled: true},
			"bulk":                         {Name: "bulk", Path: "/mnt/ssd3/miabi", Enabled: true},
		},
		nodeDefault: models.DefaultStorageClassName,
	}
}

// With no class service wired at all, nothing resolves — the pre-storage-class behaviour, where
// every volume lands wherever Docker puts it.
func TestResolveClassWithoutCatalog(t *testing.T) {
	s := &Service{}
	c, err := s.resolveClass(1, 0, "ssd-fast")
	if err != nil || c != nil {
		t.Fatalf("expected no class and no error, got %+v / %v", c, err)
	}
}

func TestResolveClassNamedAndDefaulted(t *testing.T) {
	f := catalog()
	s := &Service{classes: f}

	named, err := s.resolveClass(1, 0, "ssd-fast")
	if err != nil {
		t.Fatalf("named class: %v", err)
	}
	if named.Name != "ssd-fast" {
		t.Fatalf("named class = %q, want ssd-fast", named.Name)
	}

	// Unstated: the node's default decides, so a manifest that names no class stays portable.
	implicit, err := s.resolveClass(1, 0, "")
	if err != nil {
		t.Fatalf("implicit class: %v", err)
	}
	if implicit.Name != models.DefaultStorageClassName {
		t.Fatalf("implicit class = %q, want the built-in default", implicit.Name)
	}
}

func TestReclaimSkipsTheBuiltinClass(t *testing.T) {
	f := catalog()
	s := &Service{classes: f}
	// The built-in class owns no directory of its own: `docker volume rm` already removed
	// everything, so there is nothing to reclaim and nothing to look up.
	s.reclaim(context.Background(), &models.Volume{
		StorageClassName: models.DefaultStorageClassName, DockerName: "mb-vol-1-data",
	})
	if len(f.removed) != 0 {
		t.Fatalf("expected no reclaim for the built-in class, got %v", f.removed)
	}
}

func TestReclaimRemovesAManagedClassDirectory(t *testing.T) {
	f := catalog()
	s := &Service{classes: f}
	// Without this, `docker volume rm` would drop the volume record and leave every byte of its
	// data sitting on the operator's SSD.
	s.reclaim(context.Background(), &models.Volume{
		StorageClassName: "ssd-fast", DockerName: "mb-vol-1-data",
	})
	if len(f.removed) != 1 || f.removed[0] != "ssd-fast:mb-vol-1-data" {
		t.Fatalf("expected the volume directory to be reclaimed, got %v", f.removed)
	}
}
