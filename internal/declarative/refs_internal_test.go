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

import "testing"

// Must match what the renderer resolves, or a dry run disagrees with apply.
func TestRefFieldsMatchRenderer(t *testing.T) {
	r := NewRenderer(RenderContext{
		Databases: map[string]ConnView{"db": {Host: "h"}},
		Apps:      map[string]AppView{"web": {Host: "web"}},
	})
	for _, f := range databaseRefFields {
		if _, err := r.ref("databases", "db", f); err != nil {
			t.Errorf("database field %q: the check allows it but the renderer refuses: %v", f, err)
		}
	}
	for _, f := range applicationRefFields {
		if _, err := r.ref("applications", "web", f); err != nil {
			t.Errorf("application field %q: the check allows it but the renderer refuses: %v", f, err)
		}
	}
	if _, err := r.ref("databases", "db", "uri2"); err == nil {
		t.Error("the renderer must refuse a field the check would reject")
	}
}
