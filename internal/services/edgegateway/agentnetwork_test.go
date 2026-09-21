// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package edgegateway

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// netDocker records the network attachments asked for, so the test can assert what the control
// plane did without a Docker engine.
type netDocker struct {
	docker.Client
	connected []string
	fail      error
}

func (d *netDocker) NetworkConnect(_ context.Context, name, containerID string, _ []string) error {
	if d.fail != nil {
		return d.fail
	}
	d.connected = append(d.connected, name+"/"+containerID)
	return nil
}

func agentNetService(t *testing.T, agentID string) *Service {
	t.Helper()
	s := NewService(nil, "https://miabi.example.com", "goma:latest", "miabi", "ops@example.com")
	s.SetAnalytics("goma:analytics")
	s.SetAgentContainer(func(uint) string { return agentID })
	return s
}

// The agent is installed before the gateway network exists and joins no network of its own, so the
// control plane attaches it — otherwise it cannot resolve mb-node-gateway-redis to forward
// analytics, and the dashboards look exactly like "no traffic".
func TestAgentIsAttachedToTheGatewayNetwork(t *testing.T) {
	dc := &netDocker{}
	s := agentNetService(t, "agent123")

	s.attachAgent(context.Background(), dc, &models.Server{ID: 7, Name: "edge-1"})

	if len(dc.connected) != 1 || dc.connected[0] != "miabi/agent123" {
		t.Fatalf("attachments = %v, want the agent joined to the gateway network", dc.connected)
	}
}

// Nothing to attach: the manager has no agent, and an offline or undetected agent reports no
// container id. Neither is an error.
func TestAttachAgentIsANoOpWithoutAnAgent(t *testing.T) {
	dc := &netDocker{}
	agentNetService(t, "").attachAgent(context.Background(), dc, &models.Server{ID: 7, Name: "edge-1"})

	unwired := NewService(nil, "https://miabi.example.com", "goma:latest", "miabi", "ops@example.com")
	unwired.attachAgent(context.Background(), dc, &models.Server{ID: 7, Name: "edge-1"})

	if len(dc.connected) != 0 {
		t.Errorf("attachments = %v, want none", dc.connected)
	}
}

// A gateway that serves traffic is worth more than its analytics, so a failed attach is logged and
// swallowed rather than failing the deploy.
func TestAttachAgentFailureDoesNotPropagate(t *testing.T) {
	dc := &netDocker{fail: errors.New("no such network")}
	agentNetService(t, "agent123").attachAgent(context.Background(), dc, &models.Server{ID: 7, Name: "edge-1"})
}
