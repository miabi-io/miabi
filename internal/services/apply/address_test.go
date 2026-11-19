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

package apply

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestStaleConsumers(t *testing.T) {
	app := func(id uint) models.Application {
		a := models.Application{}
		a.ID = id
		return a
	}
	consumers := []models.Application{app(1), app(2), app(3), app(4)}
	// 1 deployed before the change, 2 after it, 3 not in this run, 4 already redeployed for another reason.
	deploySeq := map[uint]int{1: 1, 2: 3, 4: 1}
	seen := map[uint]bool{4: true}

	got := staleConsumers(consumers, deploySeq, 2, seen)
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("got %v, want only app 1", got)
	}
	if again := staleConsumers(consumers, deploySeq, 2, seen); len(again) != 0 {
		t.Fatal("an app must be redeployed once per run")
	}
}

func TestWithSelf(t *testing.T) {
	target := &models.Application{ID: 5}
	if got := withSelf(nil, target); len(got) != 1 || got[0].ID != 5 {
		t.Fatalf("managed env on: got %v", got)
	}
	if got := withSelf([]models.Application{{ID: 5}}, target); len(got) != 1 {
		t.Fatalf("a self-referencing app must appear once, got %v", got)
	}
	target.MiabiEnvDisabled = true
	if got := withSelf(nil, target); len(got) != 0 {
		t.Fatalf("opted out: got %v", got)
	}
}
