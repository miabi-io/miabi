// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

type outDoc struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       Kind   `yaml:"kind"`
	Metadata   Meta   `yaml:"metadata"`
	Spec       any    `yaml:"spec,omitempty"`
}

// Marshal serializes a resource set into a single multi-document YAML bundle
// that Parse round-trips. Project resources are organizational and omitted —
// their children are already first-class members of the set.
func Marshal(set *ResourceSet) ([]byte, error) {
	var buf bytes.Buffer
	for _, r := range set.All() {
		if r.Kind == KindProject {
			continue
		}
		doc := outDoc{APIVersion: r.APIVersion, Kind: r.Kind, Metadata: r.Metadata, Spec: r.spec()}
		if doc.APIVersion == "" {
			doc.APIVersion = APIVersion
		}
		b, err := yaml.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", r.Key(), err)
		}
		buf.WriteString("---\n")
		buf.Write(b)
	}
	return buf.Bytes(), nil
}

func (r Resource) spec() any {
	switch {
	case r.Application != nil:
		return r.Application
	case r.Stack != nil:
		return r.Stack
	case r.Database != nil:
		return r.Database
	case r.Volume != nil:
		return r.Volume
	case r.Route != nil:
		return r.Route
	case r.Secret != nil:
		return r.Secret
	case r.Domain != nil:
		return r.Domain
	case r.Registry != nil:
		return r.Registry
	default:
		return nil
	}
}
