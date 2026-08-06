// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrNoResources reports that a manifest source parsed with zero resources (the
// path exists but holds no miabi.io/v1 documents). Callers distinguish it
// from a missing path (fs.ErrNotExist) to decide whether an empty result is an
// intentional teardown or an error.
var ErrNoResources = errors.New("no declarative resources found")

// rawDoc is the kind-agnostic head of a declarative document: identity plus
// undecoded metadata and spec nodes that Parse routes into typed structs. Both
// are kept as raw nodes so they can be strictly decoded (rejecting unknown
// fields) — yaml.Node.Decode cannot enforce KnownFields, but decodeSpec can.
type rawDoc struct {
	APIVersion string    `yaml:"apiVersion"`
	Kind       Kind      `yaml:"kind"`
	Metadata   yaml.Node `yaml:"metadata"`
	Spec       yaml.Node `yaml:"spec"`
}

type rawProjectSpec struct {
	Description string      `yaml:"description,omitempty"`
	Resources   []yaml.Node `yaml:"resources,omitempty"`
}

// Parse decodes a single- or multi-document YAML stream into a normalized,
// validated set of resources. Unknown spec fields are rejected so typos surface
// rather than being silently dropped. A Project's inline resources are flattened
// into the set.
func Parse(data []byte) (*ResourceSet, error) {
	resources, err := parseStream(data, "")
	if err != nil {
		return nil, err
	}
	set := NewResourceSet()
	for _, r := range resources {
		r.normalize()
		if err := r.validate(); err != nil {
			return nil, err
		}
		if existing, dup := set.Get(r.Key()); dup {
			_ = existing
			return nil, fmt.Errorf("duplicate resource %s", r.Key())
		}
		set.Add(r)
	}
	if err := set.validateReferences(); err != nil {
		return nil, err
	}
	return set, nil
}

// ParseFS walks an fs.FS rooted at dir, parsing every .yaml/.yml file in lexical
// order into one combined set. It is the entry point GitOps uses over a cloned
// repo's manifest path.
func ParseFS(fsys fs.FS, dir string) (*ResourceSet, error) {
	if dir == "" {
		dir = "."
	}
	var files []string
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(path.Ext(p)) {
		case ".yaml", ".yml":
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk manifests: %w", err)
	}
	sort.Strings(files)

	set := NewResourceSet()
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
		parsed, err := parseStream(data, f)
		if err != nil {
			return nil, err
		}
		for _, r := range parsed {
			r.normalize()
			if err := r.validate(); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			if _, dup := set.Get(r.Key()); dup {
				return nil, fmt.Errorf("%s: duplicate resource %s", f, r.Key())
			}
			set.Add(r)
		}
	}
	if len(set.list) == 0 {
		return nil, ErrNoResources
	}
	if err := set.validateReferences(); err != nil {
		return nil, err
	}
	return set, nil
}

// parseStream decodes every YAML document in data. src is an optional filename
// used for error context.
func parseStream(data []byte, src string) ([]Resource, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var out []Resource
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, wrap(src, fmt.Errorf("parse yaml: %w", err))
		}
		if node.Kind == 0 || (node.Kind == yaml.DocumentNode && len(node.Content) == 0) {
			continue // empty document (e.g. trailing ---)
		}
		rs, err := parseNode(&node)
		if err != nil {
			return nil, wrap(src, err)
		}
		out = append(out, rs...)
	}
	return out, nil
}

// parseNode decodes one document node into one or more resources (a Project
// fans out into its children plus the Project itself).
func parseNode(node *yaml.Node) ([]Resource, error) {
	var head rawDoc
	// Strict-decode the document head so misplaced top-level keys (anything other
	// than apiVersion/kind/metadata/spec) surface as errors instead of vanishing.
	if err := decodeSpec(node, &head); err != nil {
		return nil, fmt.Errorf("decode document: %w", err)
	}
	if head.APIVersion != APIVersion {
		return nil, fmt.Errorf("apiVersion must be %q, got %q", APIVersion, head.APIVersion)
	}
	if !knownKinds[head.Kind] {
		return nil, fmt.Errorf("unknown kind %q", head.Kind)
	}
	// Strict-decode metadata too, so a typo'd key under metadata (e.g. a custom
	// label written as a top-level metadata key) is rejected rather than dropped.
	var meta Meta
	if err := decodeSpec(&head.Metadata, &meta); err != nil {
		return nil, fmt.Errorf("%s: decode metadata: %w", head.Kind, err)
	}

	r := Resource{APIVersion: head.APIVersion, Kind: head.Kind, Metadata: meta}
	switch head.Kind {
	case KindApplication:
		var s ApplicationSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.Application = &s
	case KindStack:
		var s StackSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.Stack = &s
	case KindDatabase:
		var s DatabaseSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.Database = &s
	case KindVolume:
		var s VolumeSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.Volume = &s
	case KindRoute:
		var s RouteSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.Route = &s
	case KindSecret:
		var s SecretSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.Secret = &s
	case KindDomain:
		var s DomainSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.Domain = &s
	case KindRegistry:
		var s RegistrySpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.Registry = &s
	case KindProject:
		var raw rawProjectSpec
		if err := decodeSpec(&head.Spec, &raw); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.Project = &ProjectSpec{Description: raw.Description}
		out := []Resource{r}
		for i := range raw.Resources {
			children, err := parseNode(&raw.Resources[i])
			if err != nil {
				return nil, fmt.Errorf("project %q: %w", meta.Name, err)
			}
			out = append(out, children...)
		}
		return out, nil
	}
	return []Resource{r}, nil
}

// decodeSpec strictly decodes a spec node into out, rejecting unknown fields. A
// nil/empty node decodes to the zero value.
func decodeSpec(n *yaml.Node, out any) error {
	if n == nil || n.Kind == 0 {
		return nil
	}
	b, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func specErr(kind Kind, name string, err error) error {
	return fmt.Errorf("%s %q: %w", kind, name, err)
}

func wrap(src string, err error) error {
	if src == "" {
		return err
	}
	return fmt.Errorf("%s: %w", src, err)
}
