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
	"fmt"

	"gopkg.in/yaml.v3"
)

type outDoc struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       Kind   `yaml:"kind"`
	Metadata   Meta   `yaml:"metadata"`
	Spec       any    `yaml:"spec,omitempty"`
}

// Marshal omits Project resources; their children are already first-class members of the set.
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
	case r.SealedSecret != nil:
		return r.SealedSecret
	case r.Config != nil:
		return r.Config
	case r.Middleware != nil:
		return r.Middleware
	case r.CronJob != nil:
		return r.CronJob
	case r.Job != nil:
		return r.Job
	case r.Domain != nil:
		return r.Domain
	case r.Registry != nil:
		return r.Registry
	case r.GitSource != nil:
		return r.GitSource
	case r.GitRepository != nil:
		return r.GitRepository
	default:
		return nil
	}
}
