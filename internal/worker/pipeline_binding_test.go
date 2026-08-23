// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// A repository-bound pipeline's images are namespaced apart from application
// images, so a pipeline and an unrelated app of the same name can never push
// into the same image repository — which would be a silent overwrite, not an
// error.
func TestPipelineImageNameIsNamespacedApartFromApps(t *testing.T) {
	const name = "api"
	got := pipelineImageName(name)
	if got == name {
		t.Fatalf("pipeline image name %q collides with an application of the same name", got)
	}
	if !strings.HasPrefix(got, pipelineImagePrefix) {
		t.Errorf("pipeline image name %q is missing the %q prefix", got, pipelineImagePrefix)
	}
	// The prefix must stay a path-safe repository component: the registry resolves
	// the first segment to a workspace and takes the rest as the repository path.
	if strings.ContainsAny(got, "/: ") {
		t.Errorf("pipeline image name %q is not a single path component", got)
	}
	if strings.ToLower(got) != got {
		t.Errorf("pipeline image name %q must be lowercase for a Docker reference", got)
	}
}

// Checkout() is what jobInputs switches on to decide what to clone; these are the
// cases it has to distinguish.
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
