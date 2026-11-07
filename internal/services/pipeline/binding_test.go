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

package pipeline

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func uptr(v uint) *uint { return &v }

func specWith(t *testing.T, steps string) *Spec {
	t.Helper()
	s, err := ParseSpec([]byte("apiVersion: miabi.io/v1\nkind: Pipeline\nmetadata:\n  name: p\nsteps:\n" + steps))
	if err != nil {
		t.Fatalf("fixture spec is invalid: %v", err)
	}
	return s
}

// Caught at save, not at dispatch with a runner already warm.
func TestDeployStepRequiresAnApplication(t *testing.T) {
	spec := specWith(t, "  - name: ship\n    uses: deploy\n")

	if err := validateStepsAgainstBinding(spec, nil, nil); !errors.Is(err, ErrDeployNeedsApp) {
		t.Errorf("unbound: got %v, want ErrDeployNeedsApp", err)
	}
	if err := validateStepsAgainstBinding(spec, nil, uptr(7)); !errors.Is(err, ErrDeployNeedsApp) {
		t.Errorf("repository-bound: got %v, want ErrDeployNeedsApp", err)
	}
	if err := validateStepsAgainstBinding(spec, uptr(3), nil); err != nil {
		t.Errorf("application-bound deploy was rejected: %v", err)
	}
}

// A build with nothing checked out would build an empty workspace and succeed, worse than failing.
func TestBuildStepRequiresASource(t *testing.T) {
	spec := specWith(t, "  - name: build\n    uses: build\n")

	if err := validateStepsAgainstBinding(spec, nil, nil); !errors.Is(err, ErrBuildNeedsSource) {
		t.Errorf("unbound: got %v, want ErrBuildNeedsSource", err)
	}
	for _, c := range []struct {
		name        string
		app, repoID *uint
	}{
		{"application", uptr(3), nil},
		{"repository", nil, uptr(7)},
	} {
		if err := validateStepsAgainstBinding(spec, c.app, c.repoID); err != nil {
			t.Errorf("%s-bound build was rejected: %v", c.name, err)
		}
	}
}

// Command-only steps need no checkout and no deploy target.
func TestCommandOnlyPipelineNeedsNoBinding(t *testing.T) {
	spec := specWith(t, "  - name: lint\n    image: node:20\n    run: npm run lint\n")
	if err := validateStepsAgainstBinding(spec, nil, nil); err != nil {
		t.Errorf("a command-only pipeline was rejected: %v", err)
	}
}

func TestBindingErrorNamesTheStep(t *testing.T) {
	spec := specWith(t, "  - name: lint\n    image: node:20\n    run: echo\n  - name: ship-it\n    uses: deploy\n")
	err := validateStepsAgainstBinding(spec, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "ship-it") {
		t.Errorf("error %v does not name the failing step", err)
	}
}

// Checkout() is what the worker switches on to decide what to clone.
func TestCheckoutKind(t *testing.T) {
	cases := []struct {
		name string
		def  models.PipelineDefinition
		want models.PipelineCheckout
	}{
		{"unbound", models.PipelineDefinition{}, models.CheckoutNone},
		{"application", models.PipelineDefinition{ApplicationID: uptr(1)}, models.CheckoutApplication},
		{"repository", models.PipelineDefinition{GitRepositoryID: uptr(2)}, models.CheckoutRepository},
		{"both", models.PipelineDefinition{ApplicationID: uptr(1), GitRepositoryID: uptr(2)}, models.CheckoutApplication},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.def.Checkout(); got != c.want {
				t.Errorf("Checkout() = %q, want %q", got, c.want)
			}
		})
	}
}
