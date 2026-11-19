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

package pipeline

import (
	"fmt"
	"testing"
)

func TestParseExpose(t *testing.T) {
	cases := map[string]string{
		"FROM node:22\nEXPOSE 3000\n": "[{3000 tcp}]",
		"FROM golang AS build\nEXPOSE 9999\nFROM alpine\nexpose 8080/tcp 53/udp\n": "[{8080 tcp} {53 udp}]",
		"FROM alpine\nEXPOSE ${PORT} 80 80\n":                                      "[{80 tcp}]",
		"FROM alpine\n# EXPOSE 22\nEXPOSE 8080 \\\n  8443\n":                       "[{8080 tcp} {8443 tcp}]",
		"FROM alpine\nCMD [\"sh\"]\n":                                              "[]",
	}
	for in, want := range cases {
		if got := fmt.Sprint(parseExpose(in)); got != want {
			t.Errorf("parseExpose(%q) = %s, want %s", in, got, want)
		}
	}
}
