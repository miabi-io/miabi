// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/declarative"
)

func TestRefuseImmutableStorageClass(t *testing.T) {
	// Converging this would mean deleting and recreating the volume — destroying tenant data from
	// a git push — so the apply has to fail instead.
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

// Size is the one volume field that converges: it is a declared number for quota accounting, so
// changing it moves no data.
func TestRefuseImmutableAllowsAResize(t *testing.T) {
	ch := declarative.Change{
		Action: declarative.ActionUpdate, Kind: declarative.KindVolume, Name: "pgdata",
		Fields: []declarative.FieldDiff{{Field: "size", From: "5368709120", To: "10737418240"}},
	}
	if err := refuseImmutable(ch); err != nil {
		t.Fatalf("a resize must be allowed, got %v", err)
	}
}
