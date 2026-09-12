// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

const gib = int64(1 << 30)

var (
	small  = models.DatabaseSize{ID: 1, Name: "small", MemoryBytes: gib / 2, NanoCPUs: nanosPerCore / 2}
	medium = models.DatabaseSize{ID: 2, Name: "medium", MemoryBytes: 2 * gib, NanoCPUs: nanosPerCore}
	large  = models.DatabaseSize{ID: 3, Name: "large", MemoryBytes: 4 * gib, NanoCPUs: 2 * nanosPerCore}
)

func TestOfferFrom(t *testing.T) {
	all := []models.DatabaseSize{small, medium, large}
	if o := offerFrom(all, []uint{2, 1}, false); o.Bound || len(o.Sizes) != 3 {
		t.Errorf("unenforced offer = %+v, want the whole catalog unbound", o)
	}
	o := offerFrom(all, []uint{2, 9, 1}, true)
	if !o.Bound || o.DefaultID != 2 || len(o.Sizes) != 2 || o.Sizes[0].Name != "medium" || o.Sizes[1].Name != "small" {
		t.Errorf("bound offer = %+v, want medium then small, medium the default", o)
	}
	if o := offerFrom(all, []uint{9}, true); o.Bound || len(o.Sizes) != 3 {
		t.Errorf("a plan whose sizes are all gone must not refuse every database, got %+v", o)
	}
}

func TestPickSize(t *testing.T) {
	bound := SizeOffer{Sizes: []models.DatabaseSize{medium, small, large}, DefaultID: medium.ID, Bound: true}
	unbound := SizeOffer{Sizes: []models.DatabaseSize{small, medium, large}}
	pg, mysql := specs[models.DBEnginePostgres], specs[models.DBEngineMySQL]
	for _, tc := range []struct {
		name   string
		offer  SizeOffer
		spec   engineSpec
		req    Resources
		resize bool
		want   string // the size picked, or "" for the request passing through
		err    string
	}{
		{"named", unbound, pg, Resources{Size: "large"}, false, "large", ""},
		{"named with numbers", unbound, pg, Resources{Size: "large", MemoryBytes: gib}, false, "", "not both"},
		{"named but not offered", SizeOffer{Sizes: []models.DatabaseSize{small}, Bound: true}, pg, Resources{Size: "large"}, false, "", "offered small"},
		{"unbound numbers pass", unbound, pg, Resources{MemoryBytes: gib}, false, "", ""},
		{"bound default", bound, pg, Resources{}, false, "medium", ""},
		{"bound resize must pick", bound, pg, Resources{}, true, "", "pick one"},
		{"fit memory", bound, pg, Resources{MemoryBytes: 3 * gib / 2}, false, "medium", ""},
		{"fit cpu", bound, pg, Resources{NanoCPUs: 3 * nanosPerCore / 2}, false, "large", ""},
		{"fit the smallest", bound, pg, Resources{MemoryBytes: gib / 4}, false, "small", ""},
		{"nothing fits", bound, pg, Resources{MemoryBytes: 8 * gib}, false, "", "8192 MB"},
		{"default too small for the engine", SizeOffer{Sizes: []models.DatabaseSize{small, medium}, DefaultID: small.ID, Bound: true}, mysql, Resources{}, false, "medium", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickSize(tc.offer, tc.spec, tc.req, tc.resize)
			if tc.err != "" {
				if !errors.Is(err, ErrInvalidResources) || !strings.Contains(err.Error(), tc.err) {
					t.Errorf("err = %v, want it to mention %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Size != tc.want {
				t.Errorf("size = %q, want %q (%+v)", got.Size, tc.want, got)
			}
			if tc.want == "" && got != tc.req {
				t.Errorf("request changed: %+v, want %+v", got, tc.req)
			}
		})
	}
}
