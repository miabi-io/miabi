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

package application

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateContainerLabels_Reserved(t *testing.T) {
	for _, k := range []string{"io.miabi.app", "io.miabi.managed", "com.docker.compose.project"} {
		if _, err := validateContainerLabels(map[string]string{k: "x"}); !errors.Is(err, ErrLabelReserved) {
			t.Errorf("key %q should be rejected as reserved, got %v", k, err)
		}
	}
}

func TestValidateContainerLabels_Valid(t *testing.T) {
	in := map[string]string{
		"traefik.enable":              "true",
		"traefik.http.routers.x.rule": "Host(`x`)",
		"  spaced.key  ":              "v", // trimmed
	}
	out, err := validateContainerLabels(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["traefik.enable"] != "true" || out["spaced.key"] != "v" {
		t.Errorf("unexpected cleaned labels: %v", out)
	}
	if _, ok := out["  spaced.key  "]; ok {
		t.Error("key should have been trimmed")
	}
}

func TestValidateContainerLabels_Empty(t *testing.T) {
	out, err := validateContainerLabels(map[string]string{})
	if err != nil || out != nil {
		t.Errorf("empty input should give (nil, nil), got (%v, %v)", out, err)
	}
	if _, err := validateContainerLabels(map[string]string{"": "v"}); !errors.Is(err, ErrLabelInvalid) {
		t.Errorf("empty key should be invalid, got %v", err)
	}
}

func TestValidateContainerLabels_Limits(t *testing.T) {
	many := make(map[string]string, maxContainerLabels+1)
	for i := 0; i <= maxContainerLabels; i++ {
		many["k"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+string(rune('A'+i/26))] = "v"
	}
	// Ensure we actually exceed the cap regardless of key collisions above.
	for i := 0; len(many) <= maxContainerLabels; i++ {
		many["extra-"+string(rune('a'+i%26))+string(rune('0'+i/26))] = "v"
	}
	if _, err := validateContainerLabels(many); !errors.Is(err, ErrTooManyLabels) {
		t.Errorf("exceeding the cap should be rejected, got %v (n=%d)", err, len(many))
	}

	long := map[string]string{"k": strings.Repeat("v", maxLabelValueLength+1)}
	if _, err := validateContainerLabels(long); !errors.Is(err, ErrLabelInvalid) {
		t.Errorf("over-long value should be rejected, got %v", err)
	}
}

func TestCustomLabelsAllowed_NoDepsIsPermissive(t *testing.T) {
	// With neither settings nor quota wired, the gate is a no-op.
	s := &Service{}
	if err := s.customLabelsAllowed(1); err != nil {
		t.Errorf("expected no error with nil deps, got %v", err)
	}
}
