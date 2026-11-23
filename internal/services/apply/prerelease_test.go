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
	"context"
	"math"
	"strings"
	"testing"

	d "github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
)

const gatedBundle = `apiVersion: miabi.io/v1
kind: Application
metadata: {name: web}
spec: {image: ghcr.io/org/web}
---
apiVersion: miabi.io/v1
kind: Job
metadata: {name: migrate}
spec: {app: web, command: [./web, migrate], runPolicy: beforeRelease, timeoutSeconds: 60}
---
apiVersion: miabi.io/v1
kind: Job
metadata: {name: report}
spec: {app: web, command: [./web, report], runPolicy: onRelease}
`

func TestBeforeReleaseJobsOf(t *testing.T) {
	set, err := d.Parse([]byte(gatedBundle))
	if err != nil {
		t.Fatal(err)
	}
	if got := beforeReleaseJobsOf(set, "web"); len(got) != 1 || got[0] != "migrate" {
		t.Errorf("web gated by %v, want [migrate]", got)
	}
	if got := beforeReleaseJobsOf(set, "api"); len(got) != 0 {
		t.Errorf("api gated by %v, want none", got)
	}
}

// The deploy waits for the end of the run, so it can't reach the worker before its Job is stored.
func TestGatedAppDeployIsDeferred(t *testing.T) {
	set, err := d.Parse([]byte(gatedBundle))
	if err != nil {
		t.Fatal(err)
	}
	ctx, run := withRunState(context.Background())
	s := &Service{}
	app := &models.Application{ID: 7, Name: "web"}
	if err := s.deployOrDefer(ctx, app, set); err != nil {
		t.Fatal(err)
	}
	if len(run.deferred) != 1 || !run.deployedApps[7] || run.deploySeq[7] != math.MaxInt {
		t.Fatalf("deferred = %v, deployed = %v, seq = %d", run.deferred, run.deployedApps, run.deploySeq[7])
	}
	out := s.deployDeferred(run, set, []Failure{{Kind: string(d.KindJob), Name: "migrate", Error: "boom"}})
	if len(out) != 1 || out[0].Name != "web" || !strings.Contains(out[0].Error, "beforeRelease job migrate failed to apply") {
		t.Errorf("failures = %+v, want web held back by its failed job", out)
	}
}
