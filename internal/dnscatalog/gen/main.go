// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	for _, d := range dnscatalog.All() {
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
