// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"strings"
	"testing"
)

func TestCheckAdminPassword(t *testing.T) {
	for _, tc := range []struct {
		name string
		pw   string
		ok   bool
	}{
		{"too short", "Short!pw1", false},
		{"minimum", "abcdefghijkl", true},
		{"counts characters, not bytes", "éééééééééééé", true},
		{"bcrypt limit", strings.Repeat("a", MaxAdminPasswordBytes), true},
		{"over bcrypt limit", strings.Repeat("a", MaxAdminPasswordBytes+1), false},
		{"multibyte over the byte limit", strings.Repeat("é", 37), false},
		{"newline", "abcdefghijkl\n", false},
		{"symbols and spaces", "correct horse $tapl€!", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckAdminPassword(tc.pw); (err == nil) != tc.ok {
				t.Fatalf("CheckAdminPassword(%q) = %v, want ok=%v", tc.pw, err, tc.ok)
			}
		})
	}
}
