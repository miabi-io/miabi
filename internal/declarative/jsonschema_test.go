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

package declarative_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// A stale committed schema means no completion for fields the parser already accepts.
const committedSchema = "../../schema/miabi.io-v1.schema.json"

func TestCommittedSchemaIsCurrent(t *testing.T) {
	want, err := d.JSONSchema()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	got, err := os.ReadFile(committedSchema)
	if err != nil {
		t.Fatalf("read %s: %v — run `make schema`", committedSchema, err)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
		t.Fatalf("%s is stale — run `make schema` and commit the result", committedSchema)
	}
}

func compiled(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := d.JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(d.SchemaID, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(d.SchemaID)
	if err != nil {
		t.Fatalf("the generated schema does not compile: %v", err)
	}
	return s
}

func docsOf(t *testing.T, path string) []any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var out []any
	dec := yaml.NewDecoder(f)
	for {
		var node any
		err := dec.Decode(&node)
		if err != nil {
			break
		}
		if node == nil {
			continue
		}
		// Round-trip through JSON so map keys are strings, as JSON Schema expects.
		b, merr := json.Marshal(node)
		if merr != nil {
			t.Fatalf("%s: %v", path, merr)
		}
		v, uerr := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if uerr != nil {
			t.Fatalf("%s: %v", path, uerr)
		}
		out = append(out, v)
	}
	return out
}

// A schema stricter than the parser would flag correct example files.
func TestExamplesValidateAgainstSchema(t *testing.T) {
	files, err := filepath.Glob("../../examples/apply/*.yaml")
	if err != nil || len(files) == 0 {
		t.Skip("no example manifests")
	}
	schema := compiled(t)
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			// Projects inline their children, so a document count of one is normal.
			for i, doc := range docsOf(t, path) {
				if err := schema.Validate(doc); err != nil {
					t.Errorf("document %d does not validate:\n%v", i, err)
				}
			}
		})
	}
}

// The gitops examples are the same schema seen through a directory source.
func TestGitOpsExamplesValidate(t *testing.T) {
	files, _ := filepath.Glob("../../examples/gitops/*/*/stack.yaml")
	more, _ := filepath.Glob("../../examples/gitops/*/stack.yaml")
	files = append(files, more...)
	if len(files) == 0 {
		t.Skip("no gitops examples")
	}
	schema := compiled(t)
	for _, path := range files {
		t.Run(strings.TrimPrefix(path, "../../examples/"), func(t *testing.T) {
			for i, doc := range docsOf(t, path) {
				if err := schema.Validate(doc); err != nil {
					t.Errorf("document %d does not validate:\n%v", i, err)
				}
			}
		})
	}
}

func TestSchemaRejectsWhatTheParserRejects(t *testing.T) {
	schema := compiled(t)
	cases := []struct {
		name, yaml string
	}{
		{"unknown top-level key", "apiVersion: miabi.io/v1\nkind: Volume\nmetadata: {name: v}\nspce: {}\n"},
		{"unknown spec field", "apiVersion: miabi.io/v1\nkind: Application\nmetadata: {name: a}\nspec: {image: nginx, imagee: x}\n"},
		{"wrong apiVersion", "apiVersion: miabi.io/v2\nkind: Volume\nmetadata: {name: v}\n"},
		{"unknown kind", "apiVersion: miabi.io/v1\nkind: Sercet\nmetadata: {name: s}\n"},
		{"missing metadata", "apiVersion: miabi.io/v1\nkind: Volume\n"},
		{"application without image", "apiVersion: miabi.io/v1\nkind: Application\nmetadata: {name: a}\nspec: {tag: '1'}\n"},
		{"route tls out of range", "apiVersion: miabi.io/v1\nkind: Route\nmetadata: {name: r}\nspec: {hosts: [a.example.com], app: web, tls: maybe}\n"},
		{"port is not a number", "apiVersion: miabi.io/v1\nkind: Application\nmetadata: {name: a}\nspec: {image: nginx, ports: [{container: eighty}]}\n"},
		{"field from another kind", "apiVersion: miabi.io/v1\nkind: Volume\nmetadata: {name: v}\nspec: {image: nginx}\n"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var node any
			if err := yaml.Unmarshal([]byte(tt.yaml), &node); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(node)
			v, _ := jsonschema.UnmarshalJSON(bytes.NewReader(b))
			if err := schema.Validate(v); err == nil {
				t.Error("accepted by the schema; the parser would reject it")
			}
		})
	}
}

func TestSchemaCarriesDescriptions(t *testing.T) {
	raw, err := d.JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Defs map[string]struct {
			Description string                    `json:"description"`
			Properties  map[string]map[string]any `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	described, total := 0, 0
	for _, def := range doc.Defs {
		for _, p := range def.Properties {
			total++
			if s, _ := p["description"].(string); s != "" {
				described++
			}
		}
	}
	if described == 0 {
		t.Fatal("no field carries a description — the doc-comment pass is broken")
	}
	t.Logf("%d/%d fields documented", described, total)
	if doc.Defs["ConfigSpec"].Properties["delimiters"]["description"] == "" {
		t.Error("ConfigSpec.delimiters lost its comment")
	}
}
