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

package worker

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// A same-named pipeline and app pushing to one repository would silently overwrite, not error.
func TestPipelineImageNameIsNamespacedApartFromApps(t *testing.T) {
	const name = "api"
	got := pipelineImageName(name)
	if got == name {
		t.Fatalf("pipeline image name %q collides with an application of the same name", got)
	}
	if !strings.HasPrefix(got, pipelineImagePrefix) {
		t.Errorf("pipeline image name %q is missing the %q prefix", got, pipelineImagePrefix)
	}
	// The registry resolves the first segment to a workspace and takes the rest as the repository path.
	if strings.ContainsAny(got, "/: ") {
		t.Errorf("pipeline image name %q is not a single path component", got)
	}
	if strings.ToLower(got) != got {
		t.Errorf("pipeline image name %q must be lowercase for a Docker reference", got)
	}
}

func TestJobInputsCheckoutSelection(t *testing.T) {
	appID, repoID := uint(1), uint(2)
	cases := []struct {
		name string
		def  models.PipelineDefinition
		want models.PipelineCheckout
	}{
		{"application supplies the source", models.PipelineDefinition{ApplicationID: &appID}, models.CheckoutApplication},
		{"repository supplies the source", models.PipelineDefinition{GitRepositoryID: &repoID}, models.CheckoutRepository},
		{"neither: command-only", models.PipelineDefinition{}, models.CheckoutNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.def.Checkout(); got != c.want {
				t.Errorf("Checkout() = %q, want %q", got, c.want)
			}
		})
	}
}
