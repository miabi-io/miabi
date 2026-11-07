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
	"testing"
)

// A spec-only PATCH must leave a pipeline's app binding untouched; null unbinds, a number rebinds.
func TestOptionalUintRef(t *testing.T) {
	cases := []struct {
		name        string
		raw         json.RawMessage
		wantPresent bool
		wantVal     *uint
		wantErr     bool
	}{
		{"absent (nil)", nil, false, nil, false},
		{"absent (empty)", json.RawMessage(""), false, nil, false},
		{"absent (whitespace)", json.RawMessage("  "), false, nil, false},
		{"null clears", json.RawMessage("null"), true, nil, false},
		{"number binds", json.RawMessage("42"), true, uptr(42), false},
		{"number with spaces", json.RawMessage(" 7 "), true, uptr(7), false},
		{"zero is invalid", json.RawMessage("0"), true, nil, true},
		{"garbage is invalid", json.RawMessage(`"x"`), true, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			present, val, err := optionalUintRef(tc.raw)
			if present != tc.wantPresent {
				t.Errorf("present = %v, want %v", present, tc.wantPresent)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
			switch {
			case tc.wantVal == nil && val != nil:
				t.Errorf("value = %d, want nil", *val)
			case tc.wantVal != nil && (val == nil || *val != *tc.wantVal):
				t.Errorf("value = %v, want %d", val, *tc.wantVal)
			}
		})
	}
}

func uptr(n uint) *uint { return &n }
