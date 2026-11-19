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

package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJobEnvMasksSecretsInJSONButStoresThem(t *testing.T) {
	env := JobEnv{{Key: "MODE", Value: "nightly"}, {Key: "TOKEN", Value: "enc:abc", IsSecret: true}}
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "enc:abc") || !strings.Contains(string(out), JobEnvMask) {
		t.Fatalf("API JSON leaks a secret: %s", out)
	}
	stored, err := env.Value()
	if err != nil {
		t.Fatal(err)
	}
	var back JobEnv
	if err := back.Scan(stored); err != nil {
		t.Fatal(err)
	}
	if v, _ := back.Lookup("TOKEN"); v.Value != "enc:abc" || !v.IsSecret {
		t.Fatalf("the column must keep the stored value: %+v", back)
	}
}

func TestInheritsEnvDefaultsToTrue(t *testing.T) {
	off := false
	if !InheritsEnv(nil) || InheritsEnv(&off) {
		t.Fatal("nil must mean true, false must mean false")
	}
}
