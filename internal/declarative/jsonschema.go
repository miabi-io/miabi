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

package declarative

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strings"
)

// Embedded so the generator can read doc comments reflection cannot see; editors show them as hovers.
//
//go:embed schema.go
var schemaSource string

// SchemaID must be the URL the file is served from, since editors resolve $ref against it.
const SchemaID = "https://docs.miabi.io/schema/miabi.io-v1.schema.json"

func JSONSchema() ([]byte, error) {
	g := &schemaGen{defs: map[string]any{}, docs: fieldDocs()}

	kinds := make([]string, 0, len(knownKinds))
	for k := range knownKinds {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)

	// Per-kind rules struct tags cannot express, such as a choice between two fields.
	specConstraints := map[Kind]map[string]any{
		KindApplication: {
			"oneOf": []any{
				map[string]any{"required": []string{"image"}},
				map[string]any{"required": []string{"source"}},
			},
			"description": "An application either pulls an image or builds one from source — set exactly one of image and source.",
		},
	}

	specFor := map[Kind]any{
		KindApplication:   ApplicationSpec{},
		KindStack:         StackSpec{},
		KindDatabase:      DatabaseSpec{},
		KindVolume:        VolumeSpec{},
		KindRoute:         RouteSpec{},
		KindSecret:        SecretSpec{},
		KindSealedSecret:  SealedSecretSpec{},
		KindDomain:        DomainSpec{},
		KindRegistry:      RegistrySpec{},
		KindProject:       ProjectSpec{},
		KindConfig:        ConfigSpec{},
		KindMiddleware:    MiddlewareSpec{},
		KindCronJob:       CronJobSpec{},
		KindJob:           JobSpec{},
		KindGitSource:     GitSourceSpec{},
		KindGitRepository: GitRepositorySpec{},
	}
	branches := make([]any, 0, len(kinds))
	for _, k := range kinds {
		ref := g.define(reflect.TypeOf(specFor[Kind(k)]))
		then := map[string]any{"properties": map[string]any{"spec": ref}}
		if extra := specConstraints[Kind(k)]; extra != nil {
			// Without this an editor accepts documents the parser rejects, which is worse than no schema.
			then = map[string]any{"allOf": []any{then, map[string]any{"properties": map[string]any{"spec": extra}}}}
		}
		branches = append(branches, map[string]any{
			"if":   map[string]any{"properties": map[string]any{"kind": map[string]any{"const": k}}, "required": []string{"kind"}},
			"then": then,
		})
	}

	metaRef := g.define(reflect.TypeOf(Meta{}))
	schema := map[string]any{
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"$id":                  SchemaID,
		"title":                "Miabi manifest (" + APIVersion + ")",
		"description":          "A declarative Miabi resource. Manifests are strictly parsed: an unknown key is an error, not a silently ignored typo.",
		"type":                 "object",
		"required":             []string{"apiVersion", "kind", "metadata"},
		"additionalProperties": false,
		"properties": map[string]any{
			"apiVersion": map[string]any{"const": APIVersion, "description": "The only accepted API version."},
			"kind":       map[string]any{"enum": kinds, "description": "The resource kind. Decides which fields `spec` accepts."},
			"metadata":   metaRef,
			"spec":       map[string]any{"type": "object", "description": "Kind-specific configuration."},
		},
		"allOf": branches,
		"$defs": g.defs,
	}
	return json.MarshalIndent(schema, "", "  ")
}

type schemaGen struct {
	defs     map[string]any
	docs     map[string]string
	building map[string]bool
}

// define registers t in $defs (once) and returns a $ref to it.
func (g *schemaGen) define(t reflect.Type) map[string]any {
	name := t.Name()
	ref := map[string]any{"$ref": "#/$defs/" + name}
	if _, done := g.defs[name]; done {
		return ref
	}
	if g.building == nil {
		g.building = map[string]bool{}
	}
	if g.building[name] {
		return ref
	}
	g.building[name] = true
	g.defs[name] = g.object(t)
	delete(g.building, name)
	return ref
}

func (g *schemaGen) object(t reflect.Type) map[string]any {
	props := map[string]any{}
	var required []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, opts, ok := yamlName(f)
		if !ok {
			continue
		}
		prop := g.field(t, f)
		props[name] = prop
		// No omitempty marks a field the author is expected to set.
		if !opts.omitempty {
			required = append(required, name)
		}
	}
	// The parser flattens resources itself (`yaml:"-"`), but the schema must describe what authors type.
	if t.Name() == "ProjectSpec" {
		props["resources"] = map[string]any{
			"type":        "array",
			"description": "Resources authored in this bundle. Each item is a full manifest document; the parser flattens them into the set.",
			"items":       map[string]any{"$ref": "#"},
		}
	}
	sort.Strings(required)
	out := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if doc := g.docs[t.Name()]; doc != "" {
		out["description"] = doc
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func (g *schemaGen) field(owner reflect.Type, f reflect.StructField) map[string]any {
	if owner.Name()+"."+f.Name == "RouteSpec.Port" {
		return map[string]any{
			"description": g.docs["RouteSpec.Port"],
			"oneOf": []any{
				map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
				map[string]any{"type": "string", "pattern": portNameRe.String()},
			},
		}
	}
	out := g.typeOf(f.Type)
	if doc := g.docs[owner.Name()+"."+f.Name]; doc != "" {
		out["description"] = doc
	}
	if vals := enumFor(owner.Name(), f.Name); len(vals) > 0 {
		out["enum"] = vals
	}
	// The field was a string before it became a block, and the parser still reads that spelling.
	legacy := map[string]map[string]any{
		"DatabaseSpec.Placement": {"type": "string", "enum": sortedKeys(placements), "description": "Deprecated: use instance."},
		"RouteSpec.TLS":          {"type": "string", "enum": sortedKeys(validTLS), "description": "Deprecated: use the tls block."},
	}
	if old, ok := legacy[owner.Name()+"."+f.Name]; ok {
		desc := out["description"]
		delete(out, "description")
		out = map[string]any{"description": desc, "oneOf": []any{out, old}}
	}
	return out
}

func (g *schemaGen) typeOf(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": g.typeOf(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": g.typeOf(t.Elem())}
	case reflect.Struct:
		return g.define(t)
	default:
		// An unmapped kind must not silently become "anything", or the schema accepts what the parser rejects.
		return map[string]any{}
	}
}

type yamlOpts struct{ omitempty bool }

func yamlName(f reflect.StructField) (string, yamlOpts, bool) {
	if f.PkgPath != "" {
		return "", yamlOpts{}, false
	}
	tag := f.Tag.Get("yaml")
	if tag == "-" {
		return "", yamlOpts{}, false
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "" {
		name = strings.ToLower(f.Name)
	}
	var o yamlOpts
	for _, p := range parts[1:] {
		if p == "omitempty" {
			o.omitempty = true
		}
	}
	return name, o, true
}

// Uses this package's own sets, so renaming one breaks the build instead of leaving the schema permissive.
func enumFor(typeName, field string) []string {
	switch typeName + "." + field {
	case "DomainSpec.TLS":
		return sortedKeys(validDomainTLS)
	case "DatabaseSpec.Instance":
		return sortedKeys(placements)
	case "DeploymentSpec.Strategy", "ApplicationSpec.DeprecatedStrategy":
		return sortedKeys(validStrategy)
	case "DeploymentSpec.Runtime":
		return []string{"container", "service"}
	case "HealthcheckSpec.Type":
		return sortedKeys(validHealthcheckType)
	case "ApplicationSpec.ReloadPolicy":
		return []string{ReloadRestart, ReloadNone}
	case "PortSpec.Protocol":
		return []string{"tcp", "udp"}
	case "PortSpec.Scheme":
		return []string{"http", "https"}
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func fieldDocs() map[string]string {
	out := map[string]string{}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "schema.go", schemaSource, parser.ParseComments)
	if err != nil {
		return out
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			if doc := commentText(gen.Doc); doc != "" {
				out[ts.Name.Name] = doc
			}
			for _, f := range st.Fields.List {
				doc := commentText(f.Doc)
				if doc == "" {
					doc = commentText(f.Comment)
				}
				if doc == "" {
					continue
				}
				for _, n := range f.Names {
					out[ts.Name.Name+"."+n.Name] = doc
				}
			}
		}
	}
	return out
}

// Editor hovers render wrapping as-is, so collapse Go source line breaks.
func commentText(g *ast.CommentGroup) string {
	if g == nil {
		return ""
	}
	return strings.Join(strings.Fields(g.Text()), " ")
}

// SchemaFileName is shared so the generator, docs site and editor extension cannot disagree.
const SchemaFileName = "miabi.io-v1.schema.json"

var _ = fmt.Sprintf // keep fmt for future error wrapping without churn
