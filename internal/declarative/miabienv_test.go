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

func TestMiabiEnvComparedOnlyWhenStated(t *testing.T) {
	off, on := false, true
	actual := Resource{Kind: KindApplication, Application: &ApplicationSpec{Image: "nginx", MiabiEnv: &off}}
	desired := Resource{Kind: KindApplication, Application: &ApplicationSpec{Image: "nginx"}}
	if d := diffFields(actual, desired); len(d) != 0 {
		t.Errorf("omitted miabiEnv diffed: %+v", d)
	}
	desired.Application.MiabiEnv = &on
	if d := diffFields(actual, desired); len(d) != 1 || d[0].Field != "miabiEnv" || d[0].To != "true" {
		t.Errorf("stated miabiEnv must diff, got %+v", d)
	}
	desired.Application.MiabiEnv = &off
	if d := diffFields(actual, desired); len(d) != 0 {
		t.Errorf("same miabiEnv diffed: %+v", d)
	}
}

func TestMiabiEnvParses(t *testing.T) {
	set, err := Parse([]byte("apiVersion: miabi.io/v1\nkind: Application\nmetadata: {name: api}\nspec:\n  image: nginx\n  miabiEnv: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := set.Get("Application/api")
	if r.Application.MiabiEnv == nil || *r.Application.MiabiEnv {
		t.Fatalf("miabiEnv = %v", r.Application.MiabiEnv)
	}
}
