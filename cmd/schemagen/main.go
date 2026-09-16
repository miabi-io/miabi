// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command schemagen renders the miabi.io/v1 JSON Schema from the types the
// manifest parser binds to. Editors consume it for completion and validation, so
// it is generated rather than written: a field the parser accepts and the schema
// does not is a false error, and the reverse is a file that fails at apply.
//
//	go run ./cmd/schemagen -o schema/miabi.io-v1.schema.json
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

	// The install document is a separate dialect with its own parser and its own licence tree, so it
	// has its own schema — see pkg/stack. Emitted from the same command because it is published
	// alongside, and two commands would drift.
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
