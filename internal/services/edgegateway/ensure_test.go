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

package edgegateway

import (
	"context"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// A reconnect must not touch a gateway that already runs what we would deploy.
type fakeDC struct {
	docker.Client
	containers map[string]docker.Container
	pulled     []string
	ran        []docker.RunSpec
	removed    []string
}

// A first Ensure on a remote node also starts its Redis, which these tests don't count.
func (f *fakeDC) ranGateway() int {
	n := 0
	for _, spec := range f.ran {
		if spec.Name == ContainerName {
			n++
		}
	}
	return n
}

func newFakeDC() *fakeDC {
	return &fakeDC{containers: map[string]docker.Container{}}
}

func (f *fakeDC) InspectContainer(_ context.Context, id string) (docker.Container, error) {
	if c, ok := f.containers[id]; ok {
		return c, nil
	}
	return docker.Container{}, docker.ErrNotFound
}

func (f *fakeDC) ListContainers(context.Context, bool) ([]docker.Container, error) {
	out := make([]docker.Container, 0, len(f.containers))
	for _, c := range f.containers {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeDC) RunContainer(_ context.Context, spec docker.RunSpec) (string, error) {
	f.ran = append(f.ran, spec)
	f.containers[spec.Name] = docker.Container{
		ID: "c-" + spec.Name, Names: []string{"/" + spec.Name}, Image: spec.Image,
		State: "running", Labels: spec.Labels,
	}
	return "c-" + spec.Name, nil
}

func (f *fakeDC) RemoveContainer(_ context.Context, id string, _ bool) error {
	f.removed = append(f.removed, id)
	delete(f.containers, id)
	return nil
}

func (f *fakeDC) PullImage(_ context.Context, ref string, _ *docker.RegistryAuth) error {
	f.pulled = append(f.pulled, ref)
	return nil
}

func (f *fakeDC) EnsureNetwork(_ context.Context, name string) (string, error) { return name, nil }

func (f *fakeDC) RunOneShot(context.Context, docker.RunSpec) (int, string, error) { return 0, "", nil }

func (f *fakeDC) reset() { f.pulled, f.ran, f.removed = nil, nil, nil }

func ensureService() *Service {
	return NewService(nil, "https://miabi.example.com", "jkaninda/goma-gateway:1.2.3", "miabi", "ops@example.com")
}

func edgeNode() *models.Server {
	return &models.Server{ID: 2, Name: "edge-1", Connectivity: models.ConnectivityEdgeGateway}
}

// The bug this fixes: Ensure removed and recreated the node's only ingress on every single agent reconnect.
func TestEnsureLeavesAnUnchangedGatewayAlone(t *testing.T) {
	s, dc, srv := ensureService(), newFakeDC(), edgeNode()
	if err := s.Ensure(context.Background(), dc, srv, "tok", ""); err != nil {
		t.Fatal(err)
	}
	if dc.ranGateway() != 1 {
		t.Fatalf("first Ensure ran the gateway %d times; want 1", dc.ranGateway())
	}
	if dc.containers[ContainerName].Labels[docker.LabelSpecHash] == "" {
		t.Fatal("the gateway carries no spec fingerprint, so a reconnect can never tell it is unchanged")
	}

	dc.reset()
	if err := s.Ensure(context.Background(), dc, srv, "tok", ""); err != nil {
		t.Fatal(err)
	}
	if len(dc.ran) != 0 || len(dc.removed) != 0 || len(dc.pulled) != 0 {
		t.Fatalf("second Ensure ran=%v removed=%v pulled=%v; want it to do nothing at all", dc.ran, dc.removed, dc.pulled)
	}
}

func TestEnsureRecreatesWhenTheSpecOrConfigChanges(t *testing.T) {
	cases := map[string]func(*models.Server){
		"image changed":  func(srv *models.Server) { srv.GatewayImage = "jkaninda/goma-gateway:9.9.9" },
		"config changed": func(srv *models.Server) { srv.GatewayConfigYAML = "gateway:\n  providers: {}\n" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, dc, srv := ensureService(), newFakeDC(), edgeNode()
			if err := s.Ensure(context.Background(), dc, srv, "tok", ""); err != nil {
				t.Fatal(err)
			}
			dc.reset()

			change(srv)
			if err := s.Ensure(context.Background(), dc, srv, "tok", ""); err != nil {
				t.Fatal(err)
			}
			if dc.ranGateway() != 1 {
				t.Fatalf("ran the gateway %d times; want it recreated once", dc.ranGateway())
			}
			if len(dc.removed) == 0 {
				t.Fatal("the old gateway was never removed")
			}
		})
	}
}

// A gateway that is not running is not serving, so an Ensure must put it back even when the spec matches.
func TestEnsureRecreatesAStoppedGateway(t *testing.T) {
	s, dc, srv := ensureService(), newFakeDC(), edgeNode()
	if err := s.Ensure(context.Background(), dc, srv, "tok", ""); err != nil {
		t.Fatal(err)
	}
	stopped := dc.containers[ContainerName]
	stopped.State = "exited"
	dc.containers[ContainerName] = stopped
	dc.reset()

	if err := s.Ensure(context.Background(), dc, srv, "tok", ""); err != nil {
		t.Fatal(err)
	}
	if dc.ranGateway() != 1 {
		t.Fatalf("ran the gateway %d times; want a stopped gateway recreated once", dc.ranGateway())
	}
}

func TestFindCentralMatchesByRoleThenName(t *testing.T) {
	// A compose project prefixes container names, so the role label is what identifies it.
	labelled := newFakeDC()
	labelled.containers["x"] = docker.Container{
		ID: "g1", Names: []string{"/miabi-prod-gateway-1"}, Image: "jkaninda/goma-gateway",
		Labels: map[string]string{docker.LabelRole: docker.RoleGateway},
	}
	if c, ok := FindCentral(context.Background(), labelled); !ok || c.ID != "g1" {
		t.Fatalf("FindCentral by role = (%+v, %v); want the labelled gateway", c, ok)
	}

	// An older stack carries no role label; the conventional name still identifies it.
	named := newFakeDC()
	named.containers["x"] = docker.Container{ID: "g2", Names: []string{"/" + CentralContainerName}, Image: "goma"}
	if c, ok := FindCentral(context.Background(), named); !ok || c.ID != "g2" {
		t.Fatalf("FindCentral by name = (%+v, %v); want the conventionally named gateway", c, ok)
	}

	// Anything else is not the platform's gateway.
	other := newFakeDC()
	other.containers["x"] = docker.Container{ID: "g3", Names: []string{"/some-app"}, Image: "nginx"}
	if c, ok := FindCentral(context.Background(), other); ok {
		t.Fatalf("FindCentral = (%+v, %v); want no match", c, ok)
	}
}

func ranRedis(f *fakeDC) int {
	n := 0
	for _, spec := range f.ran {
		if spec.Name == RedisContainer {
			n++
		}
	}
	return n
}

// A Redis still on the old password would refuse the new gateway (WRONGPASS).
func TestNodeRedisRecreatedWhenPasswordChanges(t *testing.T) {
	s, dc, srv := ensureService(), newFakeDC(), edgeNode()
	ctx := context.Background()

	if err := s.ensureNodeRedis(ctx, dc, srv, "first-password"); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureNodeRedis(ctx, dc, srv, "first-password"); err != nil {
		t.Fatal(err)
	}
	if n := ranRedis(dc); n != 1 {
		t.Fatalf("same password: redis started %d times, want 1 (kept)", n)
	}

	if err := s.ensureNodeRedis(ctx, dc, srv, "second-password"); err != nil {
		t.Fatal(err)
	}
	if n := ranRedis(dc); n != 2 {
		t.Fatalf("new password: redis started %d times, want 2 (recreated)", n)
	}
	last := dc.ran[len(dc.ran)-1]
	if !strings.Contains(strings.Join(last.Cmd, " "), "--requirepass second-password") {
		t.Fatalf("recreated redis cmd = %v", last.Cmd)
	}
}

// Redis started before the spec hash existed carries no label; it is recreated once, then kept.
func TestNodeRedisWithoutSpecHashIsRecreatedOnce(t *testing.T) {
	s, dc, srv := ensureService(), newFakeDC(), edgeNode()
	dc.containers[RedisContainer] = docker.Container{Names: []string{"/" + RedisContainer}, State: "running", Labels: map[string]string{}}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := s.ensureNodeRedis(ctx, dc, srv, "pw"); err != nil {
			t.Fatal(err)
		}
	}
	if n := ranRedis(dc); n != 1 {
		t.Fatalf("redis started %d times, want 1", n)
	}
}
