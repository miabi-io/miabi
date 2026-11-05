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

import "testing"

// Only "bytes=N-" resumes; anything else is served whole, and a start past the end is refused.
func TestRangeStart(t *testing.T) {
	cases := []struct {
		h       string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"bytes=100-", 100, false},
		{"bytes=0-", 0, false},
		{"bytes=100-200", 100, false},
		{"bytes=1-2,5-6", 0, false},
		{"items=3-", 0, false},
		{"bytes=-50", 0, false},
		{"bytes=1000-", 0, true},
	}
	for _, c := range cases {
		got, err := rangeStart(c.h, 1000)
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("rangeStart(%q) = %d, %v; want %d, err %v", c.h, got, err, c.want, c.wantErr)
		}
	}
}
