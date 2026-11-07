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
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// Without the run's env every step would run without it while the run record says otherwise.
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
