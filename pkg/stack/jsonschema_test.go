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

package stack

import (
	"encoding/json"
	"strings"
	"testing"
)

func schemaRoot(t *testing.T) map[string]any {
	t.Helper()
	b, err := JSONSchema()
	if err != nil {
		t.Fatalf("JSONSchema: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("the generated schema is not valid JSON: %v", err)
	}
	return root
}

func TestSchemaPinsTheDialect(t *testing.T) {
	props, _ := schemaRoot(t)["properties"].(map[string]any)

	apiVersion, _ := props["apiVersion"].(map[string]any)
	if apiVersion["const"] != APIVersion {
		t.Errorf("apiVersion const = %v, want %q", apiVersion["const"], APIVersion)
	}

	kind, _ := props["kind"].(map[string]any)
	got, _ := kind["enum"].([]any)
	if len(got) != 1 || got[0] != KindControlPlane {
		t.Errorf("kind enum = %v, want [%s]", got, KindControlPlane)
	}
}

func TestSchemaRefusesUnknownFields(t *testing.T) {
	root := schemaRoot(t)
	if root["additionalProperties"] != false {
		t.Error("the document accepts unknown top-level fields, but ParseDocument refuses them")
	}
	defs, _ := root["$defs"].(map[string]any)
	spec, _ := defs["Spec"].(map[string]any)
	if spec == nil {
		t.Fatal("no Spec definition was generated")
	}
	if spec["additionalProperties"] != false {
		t.Error("spec accepts unknown fields, but ParseDocument refuses them")
	}
}

// required is derived from the absence of omitempty, the same signal the parser and the docs use.
func TestSchemaRequiresWhatTheManifestCannotDoWithout(t *testing.T) {
	defs, _ := schemaRoot(t)["$defs"].(map[string]any)
	spec, _ := defs["Spec"].(map[string]any)
	required, _ := spec["required"].([]any)

	var names []string
	for _, r := range required {
		names = append(names, r.(string))
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "domain") {
		t.Errorf("spec.required = %v, want domain — Normalize refuses a manifest without it", names)
	}
	// Sections the install can do without stay optional, or hand-written manifests get rejected.
	for _, optional := range []string{"backup", "license", "registry", "networking"} {
		if strings.Contains(joined, optional) {
			t.Errorf("spec.required includes %q, which an install does not need", optional)
		}
	}
}

// The descriptions are the code's own comments, which is the whole reason this is generated.
func TestSchemaCarriesTheDocComments(t *testing.T) {
	defs, _ := schemaRoot(t)["$defs"].(map[string]any)
	secrets, _ := defs["SecretsSpec"].(map[string]any)
	if secrets == nil {
		t.Fatal("no SecretsSpec definition was generated")
	}
	desc, _ := secrets["description"].(string)
	if !strings.Contains(strings.ToLower(desc), "plaintext") {
		t.Errorf("SecretsSpec description = %q, want the doc comment from document.go", desc)
	}
}

func TestSchemaCarriesTheStorageEnum(t *testing.T) {
	defs, _ := schemaRoot(t)["$defs"].(map[string]any)
	registry, _ := defs["RegistrySpec"].(map[string]any)
	props, _ := registry["properties"].(map[string]any)
	storage, _ := props["storage"].(map[string]any)
	enum, _ := storage["enum"].([]any)

	if len(enum) != 2 {
		t.Fatalf("registry.storage enum = %v, want the drivers Normalize accepts", enum)
	}
	for _, want := range []string{registryStorageFilesystem, registryStorageS3} {
		found := false
		for _, e := range enum {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Errorf("registry.storage enum is missing %q", want)
		}
	}
}

// Derived state is not something an author writes, so it must not appear in the schema at all.
func TestSchemaOmitsFieldsTheDocumentNeverCarries(t *testing.T) {
	if strings.Contains(string(mustSchema(t)), `"Install"`) {
		t.Error("the schema exposes InstallSettings, which is internal and carries no yaml tags")
	}
}

func mustSchema(t *testing.T) []byte {
	t.Helper()
	b, err := JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
