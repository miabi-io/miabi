// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/pkg/stack"
)

type netLister struct {
	docker.Client
	nets []docker.Network
	err  error
}

func (n netLister) ListNetworks(context.Context) ([]docker.Network, error) { return n.nets, n.err }

func proxyNet(name string) docker.Network {
	return docker.Network{Name: name, Labels: map[string]string{docker.LabelRole: docker.RoleProxyNetwork}}
}

func TestResolveAppNetwork(t *testing.T) {
	plain := func(name string) docker.Network { return docker.Network{Name: name} }
	internal := docker.Network{Name: "miabi-internal", Labels: map[string]string{docker.LabelRole: docker.RolePlatformInternal}}

	for _, tc := range []struct {
		name       string
		configured string
		dc         netLister
		want       string
	}{
		{"configured wins over discovery", "custom", netLister{nets: []docker.Network{proxyNet("other")}}, "custom"},
		{"configured survives a list error", "custom", netLister{err: errors.New("down")}, "custom"},
		{"unset with a list error assumes a pre-rename install", "", netLister{err: errors.New("down")}, stack.LegacyNetwork},
		{"fresh engine gets the new default", "", netLister{nets: []docker.Network{plain("bridge"), internal}}, stack.DefaultNetwork},
		{"labelled legacy network is kept", "", netLister{nets: []docker.Network{proxyNet(stack.LegacyNetwork), internal}}, stack.LegacyNetwork},
		{"unlabelled legacy network from a manual install is kept", "", netLister{nets: []docker.Network{plain(stack.LegacyNetwork)}}, stack.LegacyNetwork},
		{"a renamed labelled network is found by label", "", netLister{nets: []docker.Network{proxyNet("edge"), plain(stack.LegacyNetwork)}}, "edge"},
		{"both labelled prefers the new default", "", netLister{nets: []docker.Network{proxyNet(stack.LegacyNetwork), proxyNet(stack.DefaultNetwork)}}, stack.DefaultNetwork},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveAppNetwork(context.Background(), tc.dc, tc.configured); got != tc.want {
				t.Fatalf("ResolveAppNetwork = %q, want %q", got, tc.want)
			}
		})
	}
}
