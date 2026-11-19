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

package managedenv

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func varsMap(vars []Var) map[string]Var {
	out := map[string]Var{}
	for _, v := range vars {
		out[v.Name] = v
	}
	return out
}

func TestBuildNeverEmitsEmpty(t *testing.T) {
	for _, v := range Build(Input{}) {
		if v.Value == "" {
			t.Fatalf("%s emitted empty", v.Name)
		}
	}
}

func TestBuildServiceUsesTaskTemplates(t *testing.T) {
	got := varsMap(Build(Input{AppName: "web", Service: true}))
	if v := got[Replica]; v.Value != "{{.Task.Slot}}" || !v.PerReplica {
		t.Fatalf("replica = %+v", v)
	}
	if v := got[NodeID]; v.Value != "{{.Node.ID}}" || !v.PerReplica {
		t.Fatalf("node id = %+v", v)
	}
	if _, ok := got[Node]; ok {
		t.Fatal("an unpinned service must not claim a node")
	}
	container := varsMap(Build(Input{AppName: "web", Node: "edge-1"}))
	if container[Replica].Value != "1" || container[Node].Value != "edge-1" {
		t.Fatalf("container = %+v", container)
	}
	if _, ok := container[NodeID]; ok {
		t.Fatal("a container gets no node-id template")
	}
}

func TestBuildJobFieldsOnlyForJobs(t *testing.T) {
	app := varsMap(Build(Input{Kind: KindApp, JobName: "x", JobRunID: 4}))
	if _, ok := app[JobName]; ok {
		t.Fatal("app got job fields")
	}
	job := varsMap(Build(Input{Kind: KindCronJob, JobName: "nightly", JobRunID: 4}))
	if job[KindVar].Value != "cronjob" || job[JobName].Value != "nightly" || job[JobRunID].Value != "4" {
		t.Fatalf("job = %+v", job)
	}
}

func TestVersionOf(t *testing.T) {
	for tag, want := range map[string]string{"v1.4.2": "1.4.2", "2.0.0-rc.1": "2.0.0-rc.1", "latest": "", "": "", "v": ""} {
		if got := versionOf(tag); got != want {
			t.Errorf("versionOf(%q) = %q, want %q", tag, got, want)
		}
	}
}

func TestNamesCoverBuild(t *testing.T) {
	in := Input{
		AppName: "a", AppID: 1, WorkspaceName: "w", WorkspaceID: 1, Stack: "s", Kind: KindJob, JobName: "j", JobRunID: 1,
		DeploymentID: 1, Release: 1, ImageTag: "v1.0.0", Commit: "c", Branch: "b", Image: "i", Digest: "d", Port: 80,
		PublicURL: "https://x", PublicHost: "x", Location: "l", Node: "n", NanoCPUs: 1e9, MemoryBytes: 1 << 20,
	}
	for _, v := range append(Build(in), Build(Input{Service: true})...) {
		if !IsManaged(v.Name) {
			t.Errorf("%s is built but not listed in Names", v.Name)
		}
	}
}

type fakeWorkspaces struct{}

func (fakeWorkspaces) FindByID(id uint) (*models.Workspace, error) {
	return &models.Workspace{Name: "shop"}, nil
}

type fakeClusters struct{}

func (fakeClusters) FindByID(id uint) (*models.Cluster, error) {
	return &models.Cluster{Name: "eu-1"}, nil
}

type fakeServers struct{}

func (fakeServers) FindByID(id uint) (*models.Server, error) {
	if id == 2 {
		return &models.Server{Name: "edge-2"}, nil
	}
	return nil, errors.New("not found")
}
func (fakeServers) FindLocal() (*models.Server, error) { return &models.Server{Name: "local"}, nil }

type fakeRoutes struct{ routes []models.Route }

func (f fakeRoutes) ListByApp(uint) ([]models.Route, error) { return f.routes, nil }

func TestSourceForApp(t *testing.T) {
	src := &Source{
		Workspaces: fakeWorkspaces{}, Clusters: fakeClusters{}, Servers: fakeServers{},
		Routes: fakeRoutes{routes: []models.Route{{ID: 1, Enabled: true, Hosts: []string{"shop.example.com"}}}},
	}
	app := &models.Application{Name: "web", Port: 80, Stack: &models.Stack{Name: "store"}, SourceType: models.AppSourceGit, GitRef: "main"}
	in := src.ForApp(app)
	if in.WorkspaceName != "shop" || in.Location != "eu-1" || in.Node != "local" || in.Stack != "store" || in.Branch != "main" {
		t.Fatalf("in = %+v", in)
	}
	if in.PublicURL != "https://shop.example.com" || in.PublicHost != "shop.example.com" {
		t.Fatalf("address = %q %q", in.PublicURL, in.PublicHost)
	}

	svc := &models.Application{Name: "web", RuntimeKind: models.RuntimeService, ServerID: 2}
	if got := src.ForApp(svc).Node; got != "" {
		t.Fatalf("unpinned service node = %q", got)
	}
	svc.PinnedServerID = 2
	if got := src.ForApp(svc).Node; got != "edge-2" {
		t.Fatalf("pinned service node = %q", got)
	}

	if got := (*Source)(nil).ForApp(app); got.AppName != "web" || got.WorkspaceName != "" {
		t.Fatalf("nil source = %+v", got)
	}
}
