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

// Command schemagen generates the miabi.io/v1 JSON Schema from the manifest parser's types so they cannot drift.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/pkg/stack"
)

func main() {
	out := flag.String("o", "", "write the miabi.io/v1 schema to this file instead of stdout")
	installOut := flag.String("install-o", "", "write the install.miabi.io/v1 schema to this file")
	flag.Parse()

	// Emitted by the same command because it is published alongside, and two commands would drift.
	if *installOut != "" {
		render(stack.JSONSchema, *installOut)
	}
	if *installOut != "" && *out == "" {
		return
	}
	render(declarative.JSONSchema, *out)
}

func render(schema func() ([]byte, error), out string) {
	b, err := schema()
	if err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
	b = append(b, '\n')

	if out == "" {
		_, _ = os.Stdout.Write(b)
		return
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "schemagen: wrote %s (%d bytes)\n", out, len(b))
}
