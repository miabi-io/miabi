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

// pkg/ is Apache-2.0 and the rest AGPL; Go's internal/ rule doesn't stop this module importing internal/.
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

// An AGPL-headed file under pkg/ would contradict pkg/LICENSE.
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
		if len(head) > 1024 {
			head = head[:1024]
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
