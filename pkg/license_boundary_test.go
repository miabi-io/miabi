// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package pkg_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Everything under pkg/ is Apache-2.0 (pkg/LICENSE); the rest of the repository is AGPL. An import
// of internal/ from here would put Apache-licensed source on top of AGPL code and make pkg/LICENSE a
// false claim — so the boundary is a test, not a convention. Go's own internal/ rule does not help:
// it stops OTHER modules importing internal/, not this one.
func TestPkgDoesNotImportInternal(t *testing.T) {
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(p, "miabi-io/miabi/internal") {
				t.Errorf("%s imports %s — pkg/ is Apache-2.0 and must not link the AGPL tree", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A file added under pkg/ with the repository's default AGPL header would be distributed under
// pkg/LICENSE regardless, which is the kind of contradiction a license scanner reports and a user
// has to guess at.
func TestEveryFileUnderPkgIsMarkedApache(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		head := string(b)
		if len(head) > 400 {
			head = head[:400]
		}
		if !strings.Contains(head, "SPDX-License-Identifier: Apache-2.0") {
			t.Errorf("%s does not carry an Apache-2.0 SPDX header", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
