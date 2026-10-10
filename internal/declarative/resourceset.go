// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import "sort"

// ResourceSet is an ordered, key-indexed collection of resources. It is the
// shape both desired state (parsed manifests) and actual state (a workspace
// snapshot) take so the plan engine can diff them generically.
type ResourceSet struct {
	list  []Resource
	index map[string]int // Key() -> position in list
}

// NewResourceSet returns an empty set.
func NewResourceSet() *ResourceSet {
	return &ResourceSet{index: map[string]int{}}
}

// Add appends r. A later Add with the same Key replaces the earlier entry.
func (s *ResourceSet) Add(r Resource) {
	if pos, ok := s.index[r.Key()]; ok {
		s.list[pos] = r
		return
	}
	s.index[r.Key()] = len(s.list)
	s.list = append(s.list, r)
}

// Get returns the resource for key and whether it exists.
func (s *ResourceSet) Get(key string) (Resource, bool) {
	pos, ok := s.index[key]
	if !ok {
		return Resource{}, false
	}
	return s.list[pos], true
}

// All returns the resources in insertion order.
func (s *ResourceSet) All() []Resource { return s.list }

// Len reports the number of resources.
func (s *ResourceSet) Len() int { return len(s.list) }

// ByKind returns the resources of a given kind, in insertion order.
func (s *ResourceSet) ByKind(k Kind) []Resource {
	var out []Resource
	for _, r := range s.list {
		if r.Kind == k {
			out = append(out, r)
		}
	}
	return out
}

// Has reports whether a resource of the given kind+name is present.
func (s *ResourceSet) Has(k Kind, name string) bool {
	_, ok := s.index[string(k)+"/"+name]
	return ok
}

// PlainSecrets lists the Secrets in a set that carry a plaintext value. A generated Secret has none.
func PlainSecrets(set *ResourceSet) []string {
	var names []string
	for _, r := range set.All() {
		if r.Kind == KindSecret && r.Secret != nil && r.Secret.Value != "" {
			names = append(names, r.Metadata.Name)
		}
	}
	sort.Strings(names)
	return names
}
