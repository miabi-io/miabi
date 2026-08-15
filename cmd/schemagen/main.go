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
)

func main() {
	out := flag.String("o", "", "write to this file instead of stdout")
	flag.Parse()

	b, err := declarative.JSONSchema()
	if err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
	b = append(b, '\n')

	if *out == "" {
		_, _ = os.Stdout.Write(b)
		return
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "schemagen: wrote %s (%d bytes)\n", *out, len(b))
}
