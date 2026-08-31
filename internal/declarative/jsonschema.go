// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// schemaSource is this file's neighbour, embedded so the generator can read the
// doc comments reflection cannot see. Editors show them as hovers, which is what
// makes a generated schema worth more than a hand-written one.
//
//go:embed schema.go
var schemaSource string

// SchemaID is where the generated schema is published. Editors resolve $ref
// against it, so it must be the URL the file is actually served from.
const SchemaID = "https://docs.miabi.io/schema/miabi.io-v1.schema.json"

func JSONSchema() ([]byte, error) {
	g := &schemaGen{defs: map[string]any{}, docs: fieldDocs()}

	kinds := make([]string, 0, len(knownKinds))
	for k := range knownKinds {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)

	specFor := map[Kind]any{
		KindApplication: ApplicationSpec{},
		KindStack:       StackSpec{},
		KindDatabase:    DatabaseSpec{},
		KindVolume:      VolumeSpec{},
		KindRoute:       RouteSpec{},
		KindSecret:      SecretSpec{},
		KindDomain:      DomainSpec{},
		KindRegistry:    RegistrySpec{},
		KindProject:     ProjectSpec{},
		KindConfig:      ConfigSpec{},
		KindMiddleware:  MiddlewareSpec{},
	}
	branches := make([]any, 0, len(kinds))
	for _, k := range kinds {
		ref := g.define(reflect.TypeOf(specFor[Kind(k)]))
		branches = append(branches, map[string]any{
			"if":   map[string]any{"properties": map[string]any{"kind": map[string]any{"const": k}}, "required": []string{"kind"}},
			"then": map[string]any{"properties": map[string]any{"spec": ref}},
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
		// A field the author is expected to set carries no omitempty — the same
		// signal the parser and the docs already agree on.
		if !opts.omitempty {
			required = append(required, name)
		}
	}
	// A Project's children are written under spec.resources, but the field carries
	// `yaml:"-"` because the parser flattens them itself before binding. The
	// schema has to describe what authors type, not what the struct binds.
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

// field renders one struct field, attaching its doc comment and any enum the
// validator enforces for it.
func (g *schemaGen) field(owner reflect.Type, f reflect.StructField) map[string]any {
	out := g.typeOf(f.Type)
	if doc := g.docs[owner.Name()+"."+f.Name]; doc != "" {
		out["description"] = doc
	}
	if vals := enumFor(owner.Name(), f.Name); len(vals) > 0 {
		out["enum"] = vals
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
		// An unmapped kind must not silently become "anything": that is how a
		// schema starts accepting what the parser rejects.
		return map[string]any{}
	}
}

type yamlOpts struct{ omitempty bool }

// yamlName reads the field's YAML name; ok is false for unexported fields and
// for `yaml:"-"` (the typed spec pointers, which the parser fills itself).
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

// enumFor returns the values the validator accepts for a field. The sets are
// this package's own, so renaming one breaks the build instead of leaving the
// schema quietly permissive.
func enumFor(typeName, field string) []string {
	switch typeName + "." + field {
	case "RouteSpec.TLS":
		return sortedKeys(validTLS)
	case "DomainSpec.TLS":
		return sortedKeys(validDomainTLS)
	case "DatabaseSpec.Placement":
		return sortedKeys(placements)
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

// fieldDocs maps "Type" and "Type.Field" to the doc comments in schema.go, so
// the schema's descriptions and the code's comments are the same words.
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

// commentText flattens a comment group into one line: an editor hover renders
// the wrapping as-is, and a Go source wrap width is not a tooltip's.
func commentText(g *ast.CommentGroup) string {
	if g == nil {
		return ""
	}
	return strings.Join(strings.Fields(g.Text()), " ")
}

// SchemaFileName is the published file name, kept here so the generator, the
// docs site and the editor extension cannot disagree about it.
const SchemaFileName = "miabi.io-v1.schema.json"

var _ = fmt.Sprintf // keep fmt for future error wrapping without churn
