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
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/declarative"
)

func TestRefuseImmutableStorageClass(t *testing.T) {
	// Converging would recreate the volume and destroy tenant data from a git push.
	ch := declarative.Change{
		Action: declarative.ActionUpdate, Kind: declarative.KindVolume, Name: "pgdata",
		Fields: []declarative.FieldDiff{{Field: "storage.class", From: "ssd-fast", To: "bulk"}},
	}
	err := refuseImmutable(ch)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected ErrInvalidManifest, got %v", err)
	}
	if !strings.Contains(err.Error(), "ssd-fast") || !strings.Contains(err.Error(), "bulk") {
		t.Fatalf("the error should name both classes, got %q", err)
	}
}

func TestRefuseImmutableStillRefusesAMove(t *testing.T) {
	ch := declarative.Change{
		Action: declarative.ActionUpdate, Kind: declarative.KindVolume, Name: "pgdata",
		Fields: []declarative.FieldDiff{{Field: "placement.location", From: "dc1", To: "dc2"}},
	}
	if err := refuseImmutable(ch); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected ErrInvalidManifest, got %v", err)
	}
}

// Size is a quota number, so changing it moves no data.
func TestRefuseImmutableAllowsAResize(t *testing.T) {
	ch := declarative.Change{
		Action: declarative.ActionUpdate, Kind: declarative.KindVolume, Name: "pgdata",
		Fields: []declarative.FieldDiff{{Field: "size", From: "5368709120", To: "10737418240"}},
	}
	if err := refuseImmutable(ch); err != nil {
		t.Fatalf("a resize must be allowed, got %v", err)
	}
}
