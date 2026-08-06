// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/registryserver"
)

func str(s string) *string { return &s }

func withRepos(n int, err error) *AdminRegistryHandler {
	return &AdminRegistryHandler{
		repoCount: func(context.Context) (int, error) { return n, err },
	}
}

// Changing the hostname is gated whenever there is an old name to strand
// references against, whether or not anything has been pushed yet: the
// references live in Miabi's own records, not in the registry.
func TestHostChangeNeedsConfirmation(t *testing.T) {
	h := withRepos(0, nil)
	current := &models.RegistrySettings{Host: "registry.old.test"}

	msg := h.destructiveChange(context.Background(), current, registryserver.Locks{},
		RegistrySettingsBody{Host: str("registry.new.test")})
	if msg == "" {
		t.Fatal("moving the hostname must require confirmation")
	}
	for _, want := range []string{"registry.old.test", "registry.new.test"} {
		if !strings.Contains(msg, want) {
			t.Errorf("prompt %q should name %q", msg, want)
		}
	}

	// The same value, differently spelled, is not a change.
	if msg := h.destructiveChange(context.Background(), current, registryserver.Locks{},
		RegistrySettingsBody{Host: str("REGISTRY.OLD.TEST")}); msg != "" {
		t.Errorf("re-sending the same host must not prompt, got %q", msg)
	}
	// Naming a host for the first time strands nothing.
	if msg := h.destructiveChange(context.Background(), &models.RegistrySettings{}, registryserver.Locks{},
		RegistrySettingsBody{Host: str("registry.new.test")}); msg != "" {
		t.Errorf("setting the first host must not prompt, got %q", msg)
	}
	// An env-pinned host is never written, so it can never be a change.
	if msg := h.destructiveChange(context.Background(), current, registryserver.Locks{Host: true},
		RegistrySettingsBody{Host: str("registry.new.test")}); msg != "" {
		t.Errorf("a pinned host must not prompt, got %q", msg)
	}
}

// Switching storage only matters once there are blobs to strand.
func TestStorageChangeNeedsConfirmationOnlyWhenImagesExist(t *testing.T) {
	current := &models.RegistrySettings{StorageType: models.RegistryStorageFilesystem}
	body := RegistrySettingsBody{StorageType: str(models.RegistryStorageS3)}

	if msg := withRepos(0, nil).destructiveChange(context.Background(), current, registryserver.Locks{}, body); msg != "" {
		t.Errorf("an empty registry must switch freely, got %q", msg)
	}

	msg := withRepos(3, nil).destructiveChange(context.Background(), current, registryserver.Locks{}, body)
	if msg == "" {
		t.Fatal("switching storage with images stored must require confirmation")
	}
	if !strings.Contains(msg, "3 repositories") {
		t.Errorf("prompt %q should say how much is at stake", msg)
	}

	// Re-sending the current driver is not a change.
	if msg := withRepos(3, nil).destructiveChange(context.Background(), current, registryserver.Locks{},
		RegistrySettingsBody{StorageType: str(models.RegistryStorageFilesystem)}); msg != "" {
		t.Errorf("re-sending the same driver must not prompt, got %q", msg)
	}
}

func TestBucketChangeNeedsConfirmation(t *testing.T) {
	current := &models.RegistrySettings{StorageType: models.RegistryStorageS3, S3Bucket: "old-bucket"}

	msg := withRepos(1, nil).destructiveChange(context.Background(), current, registryserver.Locks{},
		RegistrySettingsBody{S3Bucket: str("new-bucket")})
	if !strings.Contains(msg, "old-bucket") || !strings.Contains(msg, "new-bucket") {
		t.Errorf("prompt %q should name both buckets", msg)
	}
	// Singular reads as a sentence, not "1 repositories".
	if !strings.Contains(msg, "1 repository") {
		t.Errorf("prompt %q should count one repository in the singular", msg)
	}
}

// A registry that can't be reached must not wedge the settings form behind a
// confirmation the admin has no way to satisfy — the probe failing is not
// evidence that images exist.
func TestUnreachableRegistryDoesNotBlockSaving(t *testing.T) {
	h := withRepos(0, errors.New("connection refused"))
	msg := h.destructiveChange(context.Background(), &models.RegistrySettings{StorageType: models.RegistryStorageFilesystem},
		registryserver.Locks{}, RegistrySettingsBody{StorageType: str(models.RegistryStorageS3)})
	if msg != "" {
		t.Errorf("an unreachable registry must not block a storage change, got %q", msg)
	}
}

// Saving the operational knobs alone is never a destructive change.
func TestRoutineSaveIsNotGated(t *testing.T) {
	h := withRepos(9, nil)
	msg := h.destructiveChange(context.Background(),
		&models.RegistrySettings{Host: "registry.old.test", StorageType: models.RegistryStorageS3, S3Bucket: "b"},
		registryserver.Locks{},
		RegistrySettingsBody{DeleteEnabled: true, PerWorkspaceQuotaMB: 500})
	if msg != "" {
		t.Errorf("changing quota/deletes must not prompt, got %q", msg)
	}
}
