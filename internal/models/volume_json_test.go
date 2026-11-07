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

package models

import (
	"encoding/json"
	"strings"
	"testing"
)

// The host path is an operator detail and would reveal which disk the workspace landed on.
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
