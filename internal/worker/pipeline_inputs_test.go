// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// The run carries the pipeline-level env; the job inputs must too, or every step
// runs without it while the run record says otherwise.
func TestJobInputsCarryRunEnv(t *testing.T) {
	h := &PipelineHandler{}
	run := &models.PipelineRun{
		ID: 1, WorkspaceID: 2, Commit: "abc",
		Env:   map[string]string{"NODE_ENV": "production", "NPM_TOKEN": "${{ secrets.NPM_TOKEN }}"},
		Steps: []models.PipelineStepRun{{Ordinal: 0, Name: "show", Env: map[string]string{"CI": "true"}}},
	}
	in, err := h.jobInputs(run, &models.PipelineDefinition{Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	if in.Env["NODE_ENV"] != "production" {
		t.Errorf("pipeline env missing from the job inputs: %v", in.Env)
	}
	if in.Env["NPM_TOKEN"] != "${{ secrets.NPM_TOKEN }}" {
		t.Errorf("the reference must reach the dispatcher unresolved: %v", in.Env)
	}
	if len(in.Steps) != 1 || in.Steps[0].Env["CI"] != "true" {
		t.Errorf("step env missing from the job inputs: %v", in.Steps)
	}
}
