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
	"errors"
	"slices"
	"testing"
)

func TestNormalizeGPUCapabilities(t *testing.T) {
	got, err := NormalizeGPUCapabilities([]string{" Video", "compute", "video", ""})
	if err != nil || !slices.Equal(got, []string{"compute", "video"}) {
		t.Errorf("got %v, %v", got, err)
	}
	if got, err := NormalizeGPUCapabilities(nil); got != nil || err != nil {
		t.Errorf("empty should stay nil, got %v, %v", got, err)
	}
	if _, err := NormalizeGPUCapabilities([]string{"gpu"}); !errors.Is(err, ErrGPUCapabilityInvalid) {
		t.Errorf("gpu is implied, not a driver capability; got %v", err)
	}
}

func TestGPUDeviceMatchesKind(t *testing.T) {
	d := GPUDevice{Vendor: GPUVendorNvidia, Model: "NVIDIA A100-SXM4-40GB"}
	for kind, want := range map[string]bool{"": true, "nvidia": true, "a100": true, "NVIDIA A100-SXM4-40GB": true, "h100": false, "amd": false} {
		if got := d.MatchesKind(kind); got != want {
			t.Errorf("MatchesKind(%q) = %v, want %v", kind, got, want)
		}
	}
}
