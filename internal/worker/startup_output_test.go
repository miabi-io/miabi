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
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

type outputFake struct {
	docker.Client
	lines []string
	err   error
	tail  string
}

func (f *outputFake) StreamLogs(_ context.Context, _ string, follow bool, tail string, sink func(docker.LogLine) error) error {
	if follow {
		return errors.New("a dead container must not be followed")
	}
	f.tail = tail
	if f.err != nil {
		return f.err
	}
	for _, l := range f.lines {
		if err := sink(docker.LogLine{Stream: "stderr", Text: l}); err != nil {
			return err
		}
	}
	return nil
}

func TestLogContainerOutputCopiesTheTailIntoTheDeploymentLog(t *testing.T) {
	h, _, db := newSwapHarness(t)
	fake := &outputFake{lines: []string{"Error: DATABASE_URL is not set", strings.Repeat("x", startupOutputLineMax+50)}}
	h.clients = stopClients{fake}
	app := &models.Application{Name: "api", Image: "api", Tag: "1"}
	db.Create(app)
	dep := &models.Deployment{ApplicationID: app.ID}
	db.Create(dep)

	h.logContainerOutput(app, dep.ID, "c1")

	got, _ := h.deployments.FindByID(dep.ID)
	if want := strconv.Itoa(startupOutputLines); fake.tail != want {
		t.Fatalf("tail = %q, want %s", fake.tail, want)
	}
	if !strings.Contains(got.Logs, "container output (last 2 lines):\n  | Error: DATABASE_URL is not set\n") {
		t.Fatalf("output not copied:\n%s", got.Logs)
	}
	if strings.Contains(got.Logs, strings.Repeat("x", startupOutputLineMax+1)) {
		t.Fatal("an over-long line was not truncated")
	}
}

func TestLogContainerOutputSaysWhyNothingWasCopied(t *testing.T) {
	for name, fake := range map[string]*outputFake{
		"silent":     {},
		"unreadable": {err: errors.New("no such container")},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, db := newSwapHarness(t)
			h.clients = stopClients{fake}
			app := &models.Application{Name: "api", Image: "api", Tag: "1"}
			db.Create(app)
			dep := &models.Deployment{ApplicationID: app.ID}
			db.Create(dep)

			h.logContainerOutput(app, dep.ID, "c1")

			got, _ := h.deployments.FindByID(dep.ID)
			want := map[string]string{"silent": "the container produced no output", "unreadable": "could not read the container output: no such container"}[name]
			if !strings.Contains(got.Logs, want) {
				t.Fatalf("log = %q, want %q", got.Logs, want)
			}
		})
	}
}

type inspectFake struct {
	docker.Client
	c docker.Container
}

func (f inspectFake) InspectContainer(context.Context, string) (docker.Container, error) {
	return f.c, nil
}

func TestHealthGateFailsFastOnACrashLoop(t *testing.T) {
	for name, c := range map[string]docker.Container{
		"between restarts": {State: "restarting", ExitCode: 1, RestartCount: 3},
		"just restarted":   {State: "running", ExitCode: 1, RestartCount: 1},
	} {
		t.Run(name, func(t *testing.T) {
			h := &DeployHandler{clients: stopClients{inspectFake{c: c}}}
			err := h.healthGate(context.Background(), &models.Application{}, "c1")
			if err == nil || !strings.Contains(err.Error(), "keeps exiting during startup (exit code 1") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestHealthGatePassesARunningContainer(t *testing.T) {
	h := &DeployHandler{clients: stopClients{inspectFake{c: docker.Container{State: "running"}}}}
	if err := h.healthGate(context.Background(), &models.Application{}, "c1"); err != nil {
		t.Fatal(err)
	}
}

func TestTaskCrashLoopCountsOnlyThisDeploy(t *testing.T) {
	start := time.Now()
	old := docker.TaskFailure{At: start.Add(-time.Minute), ExitCode: 2}
	fail := func(d time.Duration) docker.TaskFailure {
		return docker.TaskFailure{At: start.Add(d), ExitCode: 1, Error: "task: non-zero exit (1)"}
	}
	if err := taskCrashLoop([]docker.TaskFailure{old, old, old, fail(time.Second), fail(2 * time.Second)}, start); err != nil {
		t.Fatalf("earlier deploys' failures counted: %v", err)
	}
	err := taskCrashLoop([]docker.TaskFailure{old, fail(time.Second), fail(3 * time.Second), fail(2 * time.Second)}, start)
	want := "service tasks keep failing during startup (exit code 1, 3 failed tasks): task: non-zero exit (1)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
