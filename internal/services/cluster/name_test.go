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

package cluster

import (
	"errors"
	"strings"
	"testing"
)

func TestClusterNameRoundTrips(t *testing.T) {
	s := &Service{store: &memStore{}}

	if got := s.Name(); got != "" {
		t.Fatalf("an unnamed cluster should report %q, got %q", "", got)
	}
	if err := s.SetName("  prod-eu-west-1  "); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if got := s.Name(); got != "prod-eu-west-1" {
		t.Fatalf("name = %q, want it trimmed to %q", got, "prod-eu-west-1")
	}
}

// An empty name clears the label rather than being rejected.
func TestClusterNameCanBeCleared(t *testing.T) {
	s := &Service{store: &memStore{}}
	_ = s.SetName("staging")

	if err := s.SetName(""); err != nil {
		t.Fatalf("clearing the name should be allowed, got %v", err)
	}
	if got := s.Name(); got != "" {
		t.Fatalf("name = %q, want it cleared", got)
	}
}

func TestClusterNameTooLongIsRejected(t *testing.T) {
	s := &Service{store: &memStore{}}
	err := s.SetName(strings.Repeat("x", maxClusterNameLen+1))
	if !errors.Is(err, ErrNameTooLong) {
		t.Fatalf("want ErrNameTooLong, got %v", err)
	}
}

// Reading a name must never be the thing that panics a status call.
func TestClusterNameIsNilSafe(t *testing.T) {
	s := &Service{}
	if got := s.Name(); got != "" {
		t.Fatalf("name = %q, want empty with no store wired", got)
	}
	if err := s.SetName("x"); err == nil {
		t.Fatal("SetName with no store should error, not silently succeed")
	}
}
