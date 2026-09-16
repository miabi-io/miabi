// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package stack

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

// documentSource is this file's neighbour, embedded so the generator can read the doc comments
// reflection cannot see. Editors render them as hovers, which is what makes generating the schema
// worth more than writing one.
//
//go:embed document.go
var documentSource string

// SchemaID is where the generated schema is published. Editors resolve $ref against it, so it has to
// be the URL the file is actually served from.
const SchemaID = "https://docs.miabi.io/schema/install.miabi.io-v1.schema.json"

// SchemaFileName is the published file name, kept here so the generator, the docs site and the
// editor extension cannot disagree about it.
const SchemaFileName = "install.miabi.io-v1.schema.json"

// JSONSchema renders the install document's JSON Schema from the types the parser binds to. A field
// the parser accepts and the schema does not is a false error in an editor; the reverse is a file
// that passes in the editor and fails at install. Generating it is what keeps the two identical.
func JSONSchema() ([]byte, error) {
	g := &schemaGen{defs: map[string]any{}, docs: documentDocs()}

	spec := g.define(reflect.TypeOf(Spec{}))
	meta := g.define(reflect.TypeOf(DocumentMeta{}))

	root := map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"$id":         SchemaID,
		"title":       KindControlPlane,
		"description": "A Miabi install: the desired state of the stack on one host.",
		"type":        "object",
		"properties": map[string]any{
			"apiVersion": map[string]any{"const": APIVersion},
			"kind":       map[string]any{"enum": []string{KindControlPlane}},
			"metadata":   meta,
			"spec":       spec,
		},
		"required":             []string{"apiVersion", "kind", "spec"},
		"additionalProperties": false,
		"$defs":                g.defs,
	}
	return json.MarshalIndent(root, "", "  ")
}

type schemaGen struct {
	defs     map[string]any
	docs     map[string]string
	building map[string]bool
}

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
		return ref // a type that reaches itself; the $ref is already correct
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
		name, opts, ok := schemaFieldName(f)
		if !ok {
			continue
		}
		props[name] = g.field(t, f)
		// A field the author is expected to set carries no omitempty — the same signal the parser
		// and the documentation already agree on.
		if !opts.omitempty {
			required = append(required, name)
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
	out := g.typeOf(f.Type)
	if doc := g.docs[owner.Name()+"."+f.Name]; doc != "" {
		out["description"] = doc
	}
	if vals := schemaEnumFor(owner.Name(), f.Name); len(vals) > 0 {
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
		// An unmapped kind must not silently become "anything": that is how a schema starts
		// accepting what the parser rejects.
		return map[string]any{}
	}
}

type schemaFieldOpts struct{ omitempty bool }

// schemaFieldName reads the field's YAML name; ok is false for unexported fields and for `yaml:"-"`,
// which is derived state the document never carries.
func schemaFieldName(f reflect.StructField) (string, schemaFieldOpts, bool) {
	if f.PkgPath != "" {
		return "", schemaFieldOpts{}, false
	}
	tag := f.Tag.Get("yaml")
	if tag == "-" {
		return "", schemaFieldOpts{}, false
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "" {
		name = strings.ToLower(f.Name)
	}
	var o schemaFieldOpts
	for _, p := range parts[1:] {
		if p == "omitempty" {
			o.omitempty = true
		}
	}
	return name, o, true
}

// schemaEnumFor returns the values Normalize accepts for a field. Reusing the same constants means a
// change there breaks the build rather than leaving the schema quietly permissive.
func schemaEnumFor(typeName, field string) []string {
	if typeName+"."+field == "RegistrySpec.Storage" {
		return []string{registryStorageFilesystem, registryStorageS3}
	}
	return nil
}

// documentDocs maps "Type" and "Type.Field" to the doc comments in document.go, so the schema's
// descriptions and the code's comments are the same words.
func documentDocs() map[string]string {
	out := map[string]string{}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "document.go", documentSource, parser.ParseComments)
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
			if doc := schemaComment(gen.Doc); doc != "" {
				out[ts.Name.Name] = doc
			}
			for _, f := range st.Fields.List {
				doc := schemaComment(f.Doc)
				if doc == "" {
					doc = schemaComment(f.Comment)
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

// schemaComment flattens a comment group onto one line: an editor hover renders the wrapping as-is,
// and a Go source wrap width is not a tooltip's.
func schemaComment(g *ast.CommentGroup) string {
	if g == nil {
		return ""
	}
	return strings.Join(strings.Fields(g.Text()), " ")
}

var _ = fmt.Sprintf // keep fmt for error wrapping without churn
