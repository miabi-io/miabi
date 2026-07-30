// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package registryserver

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// stubCatalog reports a fixed protected-digest set, or an error when digests is
// nil. Provenance is always empty — the delete path doesn't consult it.
type stubCatalog struct {
	digests map[string]bool
	rows    map[string]models.Image
}

func (s stubCatalog) ProtectedDigests() (map[string]bool, error) {
	if s.digests == nil {
		return nil, errors.New("catalog unavailable")
	}
	return s.digests, nil
}

func (s stubCatalog) ByDigests(uint, []string) (map[string]models.Image, error) {
	return s.rows, nil
}

// Deleting the tag a live release is pinned to must be refused: the registry
// would delete it happily and the breakage would only appear later, as a failed
// pull on a node.
func TestDeleteTagRefusesImageInUse(t *testing.T) {
	svc := listSvc(t)
	svc.SetCatalog(stubCatalog{digests: map[string]bool{"sha256:abc": true}}) // web:1.0

	err := svc.DeleteTag(context.Background(), 7, "web", "1.0")
	if !errors.Is(err, ErrTagInUse) {
		t.Fatalf("DeleteTag = %v, want ErrTagInUse", err)
	}
}

func TestDeleteTagAllowsUnusedImage(t *testing.T) {
	svc := listSvc(t)
	svc.SetCatalog(stubCatalog{digests: map[string]bool{"sha256:somethingelse": true}})

	if err := svc.DeleteTag(context.Background(), 7, "web", "1.0"); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}
}

// If the protected set can't be resolved we can't prove the image is free, so
// the delete must fail rather than proceed and risk removing a live image.
func TestDeleteTagFailsClosedWhenProtectionUnknown(t *testing.T) {
	svc := listSvc(t)
	svc.SetCatalog(stubCatalog{digests: nil})

	if err := svc.DeleteTag(context.Background(), 7, "web", "1.0"); err == nil {
		t.Fatal("want an error when the protected set can't be read")
	}
}

func TestSortTags(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want []string
	}{
		{
			// The case plain lexicographic order gets wrong: v10 sorts before v9,
			// and "latest" lands in the middle.
			name: "version tags, newest first",
			in:   []string{"v1.9.0", "latest", "v1.10.0", "v1.2.0", "v2.0.0"},
			want: []string{"latest", "v2.0.0", "v1.10.0", "v1.9.0", "v1.2.0"},
		},
		{
			name: "bare numeric build tags",
			in:   []string{"2", "10", "1", "9", "100"},
			want: []string{"100", "10", "9", "2", "1"},
		},
		{
			name: "latest is pinned first even when alphabetically last",
			in:   []string{"alpha", "latest", "zulu"},
			want: []string{"latest", "zulu", "alpha"},
		},
		{
			name: "no latest tag",
			in:   []string{"v1", "v3", "v2"},
			want: []string{"v3", "v2", "v1"},
		},
		{
			name: "zero-padded variants stay adjacent and stable",
			in:   []string{"v1", "v01", "v2"},
			want: []string{"v2", "v01", "v1"},
		},
		{
			name: "commit-sha tags are ordered arbitrarily but deterministically",
			in:   []string{"7596d3b", "0a419f1", "cdbd256"},
			want: []string{"cdbd256", "7596d3b", "0a419f1"},
		},
		{name: "empty", in: []string{}, want: []string{}},
		{name: "single", in: []string{"latest"}, want: []string{"latest"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := slices.Clone(tc.in)
			SortTags(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("SortTags(%v)\n got: %v\nwant: %v", tc.in, got, tc.want)
			}
		})
	}
}

// SortTags must impose a total order — an inconsistent comparator makes
// sort.Slice produce different results for different input permutations.
func TestSortTagsIsStableAcrossPermutations(t *testing.T) {
	base := []string{"latest", "v2.0.0", "v1.10.0", "v1.9.0", "main", "3", "20"}
	first := slices.Clone(base)
	SortTags(first)

	for i := range base {
		perm := slices.Clone(base)
		perm[0], perm[i] = perm[i], perm[0]
		SortTags(perm)
		if !slices.Equal(perm, first) {
			t.Fatalf("permutation %d sorted differently\n got: %v\nwant: %v", i, perm, first)
		}
	}
}

func TestNaturalLess(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v9", "v10", true}, // the whole point
		{"v10", "v9", false},
		{"v1.2", "v1.10", true},
		{"a", "b", true},
		{"b", "a", false},
		{"v1", "v1", false},  // equal is not less
		{"v1", "v1.1", true}, // prefix sorts first
		{"", "a", true},
		{"a", "", false},
		{"1", "a", true}, // digits before letters
	} {
		if got := naturalLess(tc.a, tc.b); got != tc.want {
			t.Errorf("naturalLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
