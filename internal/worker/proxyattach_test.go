// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"reflect"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
)

type netCall struct {
	op      string
	network string
	id      string
	aliases []string
}

// netFake records network attach calls. It embeds docker.Client so only the methods used here need
// implementing.
type netFake struct {
	docker.Client
	calls []netCall
}

func (f *netFake) NetworkConnect(_ context.Context, name, containerID string, aliases []string) error {
	f.calls = append(f.calls, netCall{op: "connect", network: name, id: containerID, aliases: aliases})
	return nil
}

func (f *netFake) NetworkDisconnect(_ context.Context, name, containerID string, _ bool) error {
	f.calls = append(f.calls, netCall{op: "disconnect", network: name, id: containerID})
	return nil
}

type clusterStub bool

func (c clusterStub) IsSwarm(uint) bool                                                { return bool(c) }
func (c clusterStub) WorkspaceOverlay(uint, models.Network) bool                       { return bool(c) }
func (clusterStub) EnsureWorkspaceOverlay(context.Context, uint, models.Network) error { return nil }
func (c clusterStub) Manager(context.Context, uint) (docker.Client, error) {
	return nil, docker.ErrNotFound
}
func (clusterStub) ServiceEndpointMode(uint) models.ServiceEndpointMode {
	return models.ServiceEndpointVIP
}

// In cluster mode the gateway dials routed apps over the ingress overlay, so a route change must attach a
// running container to it as well — a container started before cluster mode was enabled is not on it.
func TestApplyAttachmentFollowsClusterMode(t *testing.T) {
	targets := []attachTarget{{id: "c1", alias: "mb-app-tok-7"}}
	aliases := []string{"mb-app-tok-7"}

	tests := []struct {
		name     string
		cluster  ClusterCap
		attached bool
		want     []netCall
	}{
		{
			name: "no cluster wiring attaches the proxy network only", attached: true,
			want: []netCall{{op: "connect", network: node.AppNetwork, id: "c1", aliases: aliases}},
		},
		{
			name: "swarm not active attaches the proxy network only", cluster: clusterStub(false), attached: true,
			want: []netCall{{op: "connect", network: node.AppNetwork, id: "c1", aliases: aliases}},
		},
		{
			name: "cluster mode also attaches the ingress overlay", cluster: clusterStub(true), attached: true,
			want: []netCall{
				{op: "connect", network: node.AppNetwork, id: "c1", aliases: aliases},
				{op: "connect", network: node.IngressOverlay, id: "c1", aliases: aliases},
			},
		},
		{
			name: "cluster mode also detaches the ingress overlay", cluster: clusterStub(true),
			want: []netCall{
				{op: "disconnect", network: node.AppNetwork, id: "c1"},
				{op: "disconnect", network: node.IngressOverlay, id: "c1"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &netFake{}
			r := &ProxyNetworkReconciler{}
			r.SetCluster(tt.cluster)
			r.applyAttachment(context.Background(), f, 0, targets, tt.attached)
			if !reflect.DeepEqual(f.calls, tt.want) {
				t.Errorf("calls = %+v, want %+v", f.calls, tt.want)
			}
		})
	}
}
