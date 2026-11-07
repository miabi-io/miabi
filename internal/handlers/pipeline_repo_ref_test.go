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

package handlers

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/services/pipeline"
)

func classify(raw string) string {
	if raw == "" {
		return "omitted"
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "null" {
		return "unbind"
	}
	if strings.HasPrefix(trimmed, `"`) {
		var name string
		if err := json.Unmarshal([]byte(raw), &name); err != nil {
			return "invalid"
		}
		if strings.TrimSpace(name) == "" {
			return "unbind"
		}
		return "name"
	}
	var n uint64
	if err := json.Unmarshal([]byte(raw), &n); err != nil || n == 0 {
		return "invalid"
	}
	return "id"
}

func TestGitRepositoryRefUsesTheJSONType(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"omitted keeps the binding", "", "omitted"},
		{"null unbinds", "null", "unbind"},
		{"an empty string unbinds", `""`, "unbind"},
		{"a string is a name", `"acme-api"`, "name"},
		{"a number is an id", "3", "id"},
		// The case that makes a precedence rule unsafe.
		{"a quoted number is a NAME", `"123"`, "name"},
		{"a bare number is an ID", "123", "id"},
		{"zero is not an id", "0", "invalid"},
		{"an object is neither", `{"id":3}`, "invalid"},
		{"a negative number is not an id", "-1", "invalid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classify(c.raw); got != c.want {
				t.Errorf("classify(%s) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// Must match the handler, or the test pins a copy rather than the code.
func TestClassifyMatchesTheHandler(t *testing.T) {
	h := &PipelineHandler{}
	for _, raw := range []string{"", "null", `""`, "3", "123"} {
		present, id, err := h.resolveRepositoryRef(1, json.RawMessage(raw))
		if err != nil {
			t.Fatalf("resolveRepositoryRef(%q) errored: %v", raw, err)
		}
		switch classify(raw) {
		case "omitted":
			if present {
				t.Errorf("%q: present=true, want false", raw)
			}
		case "unbind":
			if !present || id != nil {
				t.Errorf("%q: present=%v id=%v, want present with a nil id", raw, present, id)
			}
		case "id":
			if !present || id == nil {
				t.Errorf("%q: present=%v id=%v, want present with an id", raw, present, id)
			}
		}
	}

	unwired := &PipelineHandler{svc: pipeline.NewService(nil, nil)}
	_, _, err := unwired.resolveRepositoryRef(1, json.RawMessage(`"acme-api"`))
	if !errors.Is(err, pipeline.ErrRepositoriesUnavailable) {
		t.Errorf("got %v, want ErrRepositoriesUnavailable", err)
	}
}
