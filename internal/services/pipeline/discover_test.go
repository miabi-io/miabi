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
	"strings"
	"testing"
	"testing/fstest"
)

// The guestbook example's .miabi/pipeline.yaml, the shape adoption must handle end to end.
const guestbookPipeline = `apiVersion: miabi.io/v1
kind: Pipeline
metadata: { name: guestbook }
on:
  push: { branches: [main] }
steps:
  - name: build
    uses: build
    dockerfile: Dockerfile
  - name: scan
    image: aquasec/trivy:latest
    continue-on-error: true
    run: "trivy image --exit-code 1 --severity HIGH,CRITICAL $MIABI_IMAGE"
  - name: deploy
    uses: deploy
`

func TestDiscoverFSFindsPipeline(t *testing.T) {
	fsys := fstest.MapFS{
		"Dockerfile":           {Data: []byte("FROM scratch\n")},
		".miabi/pipeline.yaml": {Data: []byte(guestbookPipeline)},
		"README.md":            {Data: []byte("hi")},
	}
	path, raw, spec, err := DiscoverFS(fsys)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if path != ".miabi/pipeline.yaml" {
		t.Fatalf("path = %q", path)
	}
	if string(raw) != guestbookPipeline {
		t.Error("raw document was not returned verbatim")
	}
	if spec == nil || len(spec.Steps) != 3 {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.Metadata.Name != "guestbook" {
		t.Errorf("metadata.name = %q", spec.Metadata.Name)
	}
	if !spec.Steps[1].ContinueOnError {
		t.Error("continue-on-error was dropped")
	}
	if !spec.On.FiresOnBranch("main") || spec.On.FiresOnBranch("dev") {
		t.Error("push trigger did not survive discovery")
	}
}

func TestDiscoverFSNoPipelineIsNotAnError(t *testing.T) {
	fsys := fstest.MapFS{"Dockerfile": {Data: []byte("FROM scratch\n")}}
	path, raw, spec, err := DiscoverFS(fsys)
	if err != nil {
		t.Fatalf("a repo without a pipeline must not error, got %v", err)
	}
	if path != "" || raw != nil || spec != nil {
		t.Fatalf("want zero values, got path=%q raw=%q spec=%+v", path, raw, spec)
	}
}

func TestDiscoverFSPathPriority(t *testing.T) {
	// Every accepted path holds a valid document; the highest-priority one wins.
	for i, want := range SourcePaths {
		fsys := fstest.MapFS{}
		for _, p := range SourcePaths[i:] {
			fsys[p] = &fstest.MapFile{Data: []byte(guestbookPipeline)}
		}
		path, _, spec, err := DiscoverFS(fsys)
		if err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		if path != want || spec == nil {
			t.Errorf("with %v present, picked %q, want %q", SourcePaths[i:], path, want)
		}
	}
}

func TestDiscoverFSMalformedReportsTheFile(t *testing.T) {
	fsys := fstest.MapFS{
		".miabi/pipeline.yaml": {Data: []byte("apiVersion: miabi.io/v1\nkind: Pipeline\nsteps: []\n")},
	}
	path, raw, spec, err := DiscoverFS(fsys)
	if err == nil {
		t.Fatal("want a parse error")
	}
	if spec != nil {
		t.Error("spec must be nil when parsing failed")
	}
	// Path and bytes still come back so the caller can name the broken file.
	if path != ".miabi/pipeline.yaml" || len(raw) == 0 {
		t.Errorf("path = %q, raw len = %d", path, len(raw))
	}
	if !strings.Contains(err.Error(), ".miabi/pipeline.yaml") {
		t.Errorf("error does not name the file: %v", err)
	}
}
