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
package oauth

import (
	"reflect"
	"testing"
)

func TestListClaim(t *testing.T) {
	claims := map[string]any{
		"groups": []any{"miabi:acme:owner", " ops ", "", 42},
		"roles":  "a, b ,,c",
	}
	for _, tt := range []struct {
		name string
		want []string
	}{
		{"groups", []string{"miabi:acme:owner", "ops"}},
		{"roles", []string{"a", "b", "c"}},
		{"missing", nil},
		{"", nil},
	} {
		if got := listClaim(claims, tt.name); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q = %#v, want %#v", tt.name, got, tt.want)
		}
	}
}
