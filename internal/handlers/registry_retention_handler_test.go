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
	"slices"
	"testing"
)

func TestCleanPatterns(t *testing.T) {
	got, err := cleanPatterns([]string{" latest ", "", "v*.*.*", "release-[0-9]*"})
	if err != nil || !slices.Equal(got, []string{"latest", "v*.*.*", "release-[0-9]*"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := cleanPatterns([]string{"v[1"}); err == nil {
		t.Error("an unclosed bracket is not a glob")
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = "t"
	}
	if _, err := cleanPatterns(many); err == nil {
		t.Error("more than 20 patterns were accepted")
	}
}
