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

// Command gen prints the supported-providers table for the docs, so the page cannot
// drift from the catalog. Run: go run ./internal/dnscatalog/gen
package main

import (
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/dnscatalog"
)

func main() {
	fmt.Println("| Type | Host | Credential fields | Where to create them |")
	fmt.Println("|---|---|---|---|")
	for _, d := range dnscatalog.Selectable() {
		fields := make([]string, 0, len(d.Fields))
		for _, f := range d.Fields {
			s := "`" + f.Key + "`"
			if !f.Required {
				s += " *"
			}
			fields = append(fields, s)
		}
		note := ""
		if d.ChallengeOnly {
			note = " **(DNS-01 only)**"
		}
		docs := "—"
		if d.DocsURL != "" {
			docs = "[Console](" + d.DocsURL + ")"
		}
		fmt.Printf("| `%s` | %s%s | %s | %s |\n", d.Type, d.Label, note, strings.Join(fields, ", "), docs)
	}
}
