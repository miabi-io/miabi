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

func TestAppResourceEmitsRegistryName(t *testing.T) {
	regID := uint(3)
	app := &models.Application{Name: "api", Image: "ghcr.io/org/api", RegistryID: &regID}
	regNameByID := map[uint]string{3: "ghcr"}

	res := appResource(app, map[int]bool{}, map[int]bool{}, nil, regNameByID, nil)
	if res.Application.Registry != "ghcr" {
		t.Errorf("registry = %q, want ghcr", res.Application.Registry)
	}

	// An anonymous pull carries no credential name...
	res = appResource(&models.Application{Name: "api"}, map[int]bool{}, map[int]bool{}, nil, regNameByID, nil)
	if res.Application.Registry != "" {
		t.Errorf("an app with no credential should emit an empty registry, got %q", res.Application.Registry)
	}
	res = appResource(app, map[int]bool{}, map[int]bool{}, nil, map[uint]string{}, nil)
	if res.Application.Registry != "" {
		t.Errorf("an unresolved credential id should read as anonymous, got %q", res.Application.Registry)
	}
}
