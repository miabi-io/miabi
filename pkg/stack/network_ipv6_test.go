// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/miabi-io/miabi/pkg/stack/docker"
)

// netEngine tracks networks (name -> IPv6 on) and which containers are attached with which aliases.
type netEngine struct {
	*fakeEngine
	networks map[string]bool
	attached map[string]map[string][]string // container -> network -> aliases
	failIPv6 bool
	rootless bool
	created  []docker.NetworkSpec
}

func newNetEngine() *netEngine {
	return &netEngine{fakeEngine: &fakeEngine{containers: map[string][]string{}}, networks: map[string]bool{}, attached: map[string]map[string][]string{}}
}

func (e *netEngine) InspectNetwork(_ context.Context, name string) (docker.NetworkState, error) {
	v6, ok := e.networks[name]
	if !ok {
		return docker.NetworkState{}, docker.ErrNotFound
	}
	return docker.NetworkState{ID: name, EnableIPv6: v6}, nil
}

func (e *netEngine) EnsureNetworkSpec(_ context.Context, spec docker.NetworkSpec) (string, error) {
	if _, ok := e.networks[spec.Name]; ok {
		return spec.Name, nil
	}
	v6 := spec.EnableIPv6 != nil && *spec.EnableIPv6
	if v6 && e.failIPv6 {
		return "", errors.New("daemon cannot assign an IPv6 subnet")
	}
	e.networks[spec.Name] = v6
	e.created = append(e.created, spec)
	return spec.Name, nil
}

func (e *netEngine) RemoveNetwork(_ context.Context, name string) error {
	for _, nets := range e.attached {
		if _, ok := nets[name]; ok {
			return errors.New("network has active endpoints")
		}
	}
	delete(e.networks, name)
	return nil
}

func (e *netEngine) ListContainers(context.Context, bool) ([]docker.Container, error) {
	var out []docker.Container
	for id := range e.attached {
		out = append(out, docker.Container{ID: id})
	}
	return out, nil
}

func (e *netEngine) InspectContainer(_ context.Context, id string) (docker.Container, error) {
	c := docker.Container{ID: id}
	for n, aliases := range e.attached[id] {
		c.Networks = append(c.Networks, docker.ContainerNetwork{Name: n, Aliases: aliases})
	}
	return c, nil
}

func (e *netEngine) NetworkConnect(_ context.Context, name, id string, aliases []string) error {
	if _, ok := e.networks[name]; !ok {
		return docker.ErrNotFound
	}
	e.attached[id][name] = aliases
	return nil
}

func (e *netEngine) NetworkDisconnect(_ context.Context, name, id string, _ bool) error {
	delete(e.attached[id], name)
	return nil
}

func (e *netEngine) Rootless(context.Context) (bool, error) { return e.rootless, nil }

func ipv6Manifest() *Manifest {
	m := Defaults("miabi/miabi:test")
	on := true
	m.Network.IPv6 = &on
	return m
}

// An existing IPv4 proxy network with a gateway and an app on it.
func liveStack(m *Manifest) *netEngine {
	e := newNetEngine()
	e.networks[m.Network.Name] = false
	e.networks[m.InternalNetwork.Name] = false
	e.attached["gateway"] = map[string][]string{m.Network.Name: {"miabi-gateway"}, m.InternalNetwork.Name: nil}
	e.attached["app"] = map[string][]string{m.Network.Name: {"web-app"}}
	return e
}

func TestIPv6Upgrades(t *testing.T) {
	m := ipv6Manifest()
	e := liveStack(m)
	got, err := New(e, nil, "").IPv6Upgrades(context.Background(), m)
	if err != nil || !reflect.DeepEqual(got, []string{m.Network.Name}) {
		t.Fatalf("IPv6Upgrades = %v, %v; want [%s]", got, err, m.Network.Name)
	}

	e.networks[m.Network.Name] = true
	if got, _ := New(e, nil, "").IPv6Upgrades(context.Background(), m); len(got) != 0 {
		t.Fatalf("an IPv6 network was reported for recreate: %v", got)
	}
	if got, _ := New(newNetEngine(), nil, "").IPv6Upgrades(context.Background(), m); len(got) != 0 {
		t.Fatalf("a missing network was reported for recreate: %v", got)
	}
}

func TestEnsureNetworkRecreatesForIPv6(t *testing.T) {
	m := ipv6Manifest()
	e := liveStack(m)
	s := New(e, nil, "")
	s.AllowNetworkRecreate()
	if err := s.ensureNetwork(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if !e.networks[m.Network.Name] {
		t.Fatal("proxy network was not recreated with IPv6")
	}
	if e.networks[m.InternalNetwork.Name] {
		t.Fatal("the private network was given IPv6 it did not ask for")
	}
	for id, alias := range map[string]string{"gateway": "miabi-gateway", "app": "web-app"} {
		if got := e.attached[id][m.Network.Name]; !slices.Equal(got, []string{alias}) {
			t.Errorf("%s reattached with aliases %v, want [%s]", id, got, alias)
		}
	}
	if _, ok := e.attached["gateway"][m.InternalNetwork.Name]; !ok {
		t.Error("the gateway lost its private network")
	}
}

// Without the operator's go-ahead a converge (e.g. `miabi upgrade`) never cuts traffic.
func TestEnsureNetworkLeavesIPv4NetworkWithoutPermission(t *testing.T) {
	m := ipv6Manifest()
	e := liveStack(m)
	if err := New(e, nil, "").ensureNetwork(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if e.networks[m.Network.Name] || len(e.created) != 0 {
		t.Fatal("network was recreated without AllowNetworkRecreate")
	}
}

func TestRecreateRestoresNetworkWhenIPv6Fails(t *testing.T) {
	m := ipv6Manifest()
	e := liveStack(m)
	e.failIPv6 = true
	s := New(e, nil, "")
	s.AllowNetworkRecreate()
	if err := s.ensureNetwork(context.Background(), m); err == nil {
		t.Fatal("want the IPv6 create error")
	}
	if v6, ok := e.networks[m.Network.Name]; !ok || v6 {
		t.Fatalf("proxy network not restored as IPv4 (exists=%v ipv6=%v)", ok, v6)
	}
	for _, id := range []string{"gateway", "app"} {
		if _, ok := e.attached[id][m.Network.Name]; !ok {
			t.Errorf("%s left detached after the failed recreate", id)
		}
	}
}

func TestCheckDaemonRefusesRootless(t *testing.T) {
	e := newNetEngine()
	if err := New(e, nil, "").CheckDaemon(context.Background()); err != nil {
		t.Fatalf("rootful daemon refused: %v", err)
	}
	e.rootless = true
	if err := New(e, nil, "").CheckDaemon(context.Background()); !errors.Is(err, ErrRootlessDaemon) {
		t.Fatalf("CheckDaemon = %v, want ErrRootlessDaemon", err)
	}
}
