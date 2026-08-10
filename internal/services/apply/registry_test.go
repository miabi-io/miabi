// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	// ...and neither does one whose credential vanished from under it, rather
	// than inventing a name the manifest could never match.
	res = appResource(app, map[int]bool{}, map[int]bool{}, nil, map[uint]string{}, nil)
	if res.Application.Registry != "" {
		t.Errorf("an unresolved credential id should read as anonymous, got %q", res.Application.Registry)
	}
}
