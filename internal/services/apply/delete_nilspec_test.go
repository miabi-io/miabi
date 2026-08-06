// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import "testing"

// A prune-delete (and DeleteResource) drives execute with a zero Resource: the
// pruned resource is, by definition, absent from the desired manifest. Anything
// reading the desired spec on that path has to tolerate a nil.
func TestResolveRegistryToleratesAbsentSpec(t *testing.T) {
	s := &Service{} // registries deliberately unwired: a nil spec must not reach it
	id, err := s.resolveRegistry(1, "api", nil)
	if err != nil || id != nil {
		t.Fatalf("resolveRegistry(nil spec) = (%v, %v), want (nil, nil)", id, err)
	}
}
