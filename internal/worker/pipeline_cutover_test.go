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

func TestSubjectUser(t *testing.T) {
	if got := subjectUser(&models.PipelineRun{}); got != 0 {
		t.Errorf("no trigger user → %d, want 0", got)
	}
	u := uint(9)
	if got := subjectUser(&models.PipelineRun{TriggeredByUserID: &u}); got != 9 {
		t.Errorf("trigger user = %d, want 9", got)
	}
}

func TestJobInputsNoApp(t *testing.T) {
	h := &PipelineHandler{registry: "reg.example.com"}
	run := &models.PipelineRun{ID: 1, WorkspaceID: 42, Number: 3, Commit: "abc123"}
	def := &models.PipelineDefinition{Name: "deploy"}

	in, err := h.jobInputs(run, def)
	if err != nil {
		t.Fatalf("jobInputs: %v", err)
	}
	if in.Pipeline != "deploy" || in.Registry != "reg.example.com" || in.Ref != "abc123" {
		t.Errorf("job inputs = %+v", in)
	}
	if in.AppID != nil || in.Repository != "" || in.SourceURL != "" {
		t.Errorf("no-app pipeline should have no app/repo/source: %+v", in)
	}
	if in.Run != run {
		t.Error("run not carried through")
	}
}
