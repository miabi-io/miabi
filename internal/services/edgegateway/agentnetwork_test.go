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
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

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

// The agent joins no network itself, and without the gateway network it cannot forward analytics.
func TestAgentIsAttachedToTheGatewayNetwork(t *testing.T) {
	dc := &netDocker{}
	s := agentNetService(t, "agent123")

	s.attachAgent(context.Background(), dc, &models.Server{ID: 7, Name: "edge-1"})

	if len(dc.connected) != 1 || dc.connected[0] != "miabi/agent123" {
		t.Fatalf("attachments = %v, want the agent joined to the gateway network", dc.connected)
	}
}

// No agent or no container id is not an error.
func TestAttachAgentIsANoOpWithoutAnAgent(t *testing.T) {
	dc := &netDocker{}
	agentNetService(t, "").attachAgent(context.Background(), dc, &models.Server{ID: 7, Name: "edge-1"})

	unwired := NewService(nil, "https://miabi.example.com", "goma:latest", "miabi", "ops@example.com")
	unwired.attachAgent(context.Background(), dc, &models.Server{ID: 7, Name: "edge-1"})

	if len(dc.connected) != 0 {
		t.Errorf("attachments = %v, want none", dc.connected)
	}
}

// Serving traffic matters more than analytics, so a failed attach must not fail the deploy.
func TestAttachAgentFailureDoesNotPropagate(t *testing.T) {
	dc := &netDocker{fail: errors.New("no such network")}
	agentNetService(t, "agent123").attachAgent(context.Background(), dc, &models.Server{ID: 7, Name: "edge-1"})
}
