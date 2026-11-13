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

// ErrNoResources is distinct from fs.ErrNotExist so callers can tell an intentional teardown from an error.
var ErrNoResources = errors.New("no declarative resources found")

// Kept raw so they can be strictly decoded; yaml.Node.Decode cannot enforce KnownFields.
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

// Parse rejects unknown spec fields so typos surface instead of being silently dropped.
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

func parseNode(node *yaml.Node) ([]Resource, error) {
	var head rawDoc
	// Strict-decode the head so misplaced top-level keys surface as errors instead of vanishing.
	if err := decodeSpec(node, &head); err != nil {
		return nil, fmt.Errorf("decode document: %w", err)
	}
	if head.APIVersion != APIVersion {
		return nil, fmt.Errorf("apiVersion must be %q, got %q", APIVersion, head.APIVersion)
	}
	if !knownKinds[head.Kind] {
		return nil, fmt.Errorf("unknown kind %q", head.Kind)
	}
	// Strict-decode metadata so a misplaced custom label is rejected rather than dropped.
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
	case KindDatabaseInstance:
		var s DatabaseInstanceSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.DatabaseInstance = &s
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
	case KindSealedSecret:
		var s SealedSecretSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.SealedSecret = &s
	case KindConfig:
		var s ConfigSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.Config = &s
	case KindMiddleware:
		var s MiddlewareSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.Middleware = &s
	case KindCronJob:
		var s CronJobSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.CronJob = &s
	case KindJob:
		var s JobSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.Job = &s
	case KindDomain:
		var s DomainSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.Domain = &s
	case KindGitRepository:
		var s GitRepositorySpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.GitRepository = &s
	case KindGitSource:
		var s GitSourceSpec
		if err := decodeSpec(&head.Spec, &s); err != nil {
			return nil, specErr(head.Kind, meta.Name, err)
		}
		r.GitSource = &s
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
