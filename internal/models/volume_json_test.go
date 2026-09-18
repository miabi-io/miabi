// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"encoding/json"
	"strings"
	"testing"
)

// The API contract the volume UI reads: the storage class is present, and the host path the volume
// sits at is not — it is an operator detail a tenant cannot act on, and under a class it would name
// the disk their workspace landed on.
func TestVolumeJSONExposesClassNotMountpoint(t *testing.T) {
	raw, err := json.Marshal(Volume{
		Name:             "app-data",
		StorageClassName: DefaultStorageClassName,
		Mountpoint:       "/mnt/ssd1/miabi/mb-vol-7-app-data",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, `"storage_class":"default"`) {
		t.Fatalf("storage_class missing from the volume payload: %s", body)
	}
	if strings.Contains(body, "mountpoint") || strings.Contains(body, "/mnt/ssd1") {
		t.Fatalf("the host path leaked into the volume payload: %s", body)
	}
}
