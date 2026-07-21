// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"errors"
	"strings"
	"testing"
)

// memTokens is an in-memory TokenStore (the same store backs the cluster name).
type memTokens struct{ m map[string]string }

func newMemTokens() *memTokens { return &memTokens{m: map[string]string{}} }

func (t *memTokens) Get(k string) (string, error) { return t.m[k], nil }
func (t *memTokens) Set(k, v string) error        { t.m[k] = v; return nil }

func TestClusterNameRoundTrips(t *testing.T) {
	s := &Service{tokens: newMemTokens()}

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

// An operator who no longer wants a label should be able to drop it, not be stuck with
// one — so an empty name clears rather than being rejected.
func TestClusterNameCanBeCleared(t *testing.T) {
	s := &Service{tokens: newMemTokens()}
	_ = s.SetName("staging")

	if err := s.SetName(""); err != nil {
		t.Fatalf("clearing the name should be allowed, got %v", err)
	}
	if got := s.Name(); got != "" {
		t.Fatalf("name = %q, want it cleared", got)
	}
}

func TestClusterNameTooLongIsRejected(t *testing.T) {
	s := &Service{tokens: newMemTokens()}
	err := s.SetName(strings.Repeat("x", maxClusterNameLen+1))
	if !errors.Is(err, ErrNameTooLong) {
		t.Fatalf("want ErrNameTooLong, got %v", err)
	}
}

// The name store is optional (nil on a build that never wires it), and reading a name
// must never be the thing that panics a status call.
func TestClusterNameIsNilSafe(t *testing.T) {
	s := &Service{}
	if got := s.Name(); got != "" {
		t.Fatalf("name = %q, want empty with no store wired", got)
	}
	if err := s.SetName("x"); err == nil {
		t.Fatal("SetName with no store should error, not silently succeed")
	}
}
