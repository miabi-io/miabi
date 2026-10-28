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
	pg, mysql, mongo := specs[models.DBEnginePostgres], specs[models.DBEngineMySQL], specs[models.DBEngineMongoDB]
	lean := models.DatabaseSize{ID: 4, Name: "lean", MemoryBytes: 2 * gib, NanoCPUs: nanosPerCore / 4}
	tiny := models.DatabaseSize{ID: 5, Name: "tiny", MemoryBytes: gib / 8, NanoCPUs: nanosPerCore / 2}
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
		{"default too small for the engine", SizeOffer{Sizes: []models.DatabaseSize{tiny, small}, DefaultID: tiny.ID, Bound: true}, mysql, Resources{}, false, "small", ""},
		{"too few cores for the engine", SizeOffer{Sizes: []models.DatabaseSize{lean, medium}, DefaultID: lean.ID, Bound: true}, mongo, Resources{}, false, "medium", ""},
		{"fit raises cpu to the engine's minimum", SizeOffer{Sizes: []models.DatabaseSize{lean, medium}, Bound: true}, mongo, Resources{MemoryBytes: gib}, false, "medium", ""},
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
