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

package stack

import (
	"fmt"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/dotenv"
	"gopkg.in/yaml.v3"
)

// composeVars holds the .env values a Compose file is interpolated with, following the Compose spec.
type composeVars struct {
	values map[string]string
	unset  map[string]bool
}

func newComposeVars(dotEnv string) *composeVars {
	v := &composeVars{values: map[string]string{}, unset: map[string]bool{}}
	for _, kv := range dotenv.Parse(dotEnv) {
		v.values[kv.Key] = kv.Value
	}
	return v
}

func (v *composeVars) pairs() []dotenv.KeyValue {
	keys := make([]string, 0, len(v.values))
	for k := range v.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]dotenv.KeyValue, 0, len(keys))
	for _, k := range keys {
		out = append(out, dotenv.KeyValue{Key: k, Value: v.values[k]})
	}
	return out
}

func (v *composeVars) warnings() []string {
	names := make([]string, 0, len(v.unset))
	for n := range v.unset {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, fmt.Sprintf("variable %s is not set; an empty string was used", n))
	}
	return out
}

// interpolateNode expands values only: substituting on the parsed tree means a value can't inject YAML structure.
func (v *composeVars) interpolateNode(n *yaml.Node) error {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			if err := v.interpolateNode(c); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			if err := v.interpolateNode(n.Content[i]); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if !strings.Contains(n.Value, "$") {
			return nil
		}
		out, err := v.expand(n.Value)
		if err != nil {
			return err
		}
		n.Value = out
	}
	return nil
}

func (v *composeVars) expand(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch next := s[i+1]; {
		case next == '$':
			b.WriteByte('$')
			i++
		case next == '{':
			end := closingBrace(s, i+2)
			if end < 0 {
				return "", fmt.Errorf("unclosed ${ in %q", s)
			}
			out, err := v.substitute(s[i+2 : end])
			if err != nil {
				return "", err
			}
			b.WriteString(out)
			i = end
		case isNameStart(next):
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			b.WriteString(v.lookup(s[i+1 : j]))
			i = j - 1
		default:
			b.WriteByte('$')
		}
	}
	return b.String(), nil
}

// substitute handles one ${...} body: NAME, then optionally :- - :? ? :+ + and an argument that may nest.
func (v *composeVars) substitute(body string) (string, error) {
	j := 0
	for j < len(body) && isNameChar(body[j]) {
		j++
	}
	name, rest := body[:j], body[j:]
	if name == "" || !isNameStart(name[0]) {
		return "", fmt.Errorf("invalid interpolation ${%s}", body)
	}
	value, set := v.values[name]
	if rest == "" {
		return v.lookup(name), nil
	}
	op, arg := rest[:1], rest[1:]
	if op == ":" && len(rest) >= 2 {
		op, arg = rest[:2], rest[2:]
	}
	nonEmpty := set && value != ""
	switch op {
	case ":-", "-":
		if (op == ":-" && nonEmpty) || (op == "-" && set) {
			return value, nil
		}
		return v.expand(arg)
	case ":?", "?":
		if (op == ":?" && nonEmpty) || (op == "?" && set) {
			return value, nil
		}
		msg, err := v.expand(arg)
		if err != nil {
			return "", err
		}
		if msg == "" {
			msg = "required variable is missing a value"
		}
		return "", fmt.Errorf("%s: %s", name, msg)
	case ":+", "+":
		if (op == ":+" && nonEmpty) || (op == "+" && set) {
			return v.expand(arg)
		}
		return "", nil
	}
	return "", fmt.Errorf("invalid interpolation ${%s}", body)
}

func (v *composeVars) lookup(name string) string {
	value, ok := v.values[name]
	if !ok {
		v.unset[name] = true
	}
	return value
}

// closingBrace finds the } that closes a ${ opened before from, skipping nested ${...}.
func closingBrace(s string, from int) int {
	depth := 1
	for i := from; i < len(s); i++ {
		switch {
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '{':
			depth++
			i++
		case s[i] == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameChar(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9')
}

// composeEnvFile reads env_file as a string, a list of paths, or a list of {path, required}.
type composeEnvFile []composeEnvFileRef

type composeEnvFileRef struct {
	Path     string
	Required bool
}

func (f *composeEnvFile) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*f = append(*f, composeEnvFileRef{Path: node.Value, Required: true})
	case yaml.SequenceNode:
		for _, n := range node.Content {
			if n.Kind == yaml.ScalarNode {
				*f = append(*f, composeEnvFileRef{Path: n.Value, Required: true})
				continue
			}
			ref := struct {
				Path     string `yaml:"path"`
				Required *bool  `yaml:"required"`
			}{}
			if err := n.Decode(&ref); err != nil {
				return err
			}
			*f = append(*f, composeEnvFileRef{Path: ref.Path, Required: ref.Required == nil || *ref.Required})
		}
	default:
		return fmt.Errorf("env_file must be a path or a list")
	}
	return nil
}

func isDotEnvPath(p string) bool {
	p = strings.TrimSpace(p)
	return p == ".env" || p == "./.env"
}
