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

package stack

import (
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func parseService(t *testing.T, y, name string) composeService {
	t.Helper()
	var cf composeFile
	if err := yaml.Unmarshal([]byte(y), &cf); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return cf.Services[name]
}

func TestComposeGPU_DeployReservation(t *testing.T) {
	svc := parseService(t, `
services:
  ollama:
    image: ollama/ollama
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: 2
              capabilities: [gpu, video, compute]
`, "ollama")
	g := composeGPURequest(svc)
	if g.Count != 2 || !slices.Equal(g.Capabilities, []string{"compute", "video"}) || len(g.Notes) != 0 {
		t.Errorf("got %+v", g)
	}
}

func TestComposeGPU_AllAndDeviceIDs(t *testing.T) {
	svc := parseService(t, `
services:
  a:
    image: x
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]
            - capabilities: [gpu]
              device_ids: ["0", "3"]
`, "a")
	g := composeGPURequest(svc)
	if g.Count != 3 {
		t.Errorf("count = %d, want 1 (all) + 2 (ids)", g.Count)
	}
	if len(g.Notes) != 2 {
		t.Errorf("notes = %v, want one for all and one for device_ids", g.Notes)
	}
	if g.Capabilities != nil {
		t.Errorf("gpu alone keeps the toolkit default, got %v", g.Capabilities)
	}
}

func TestComposeGPU_ServiceLevelGPUs(t *testing.T) {
	if g := composeGPURequest(parseService(t, "services:\n  a:\n    image: x\n    gpus: all\n", "a")); g.Count != 1 {
		t.Errorf("gpus: all → %+v", g)
	}
	g := composeGPURequest(parseService(t, `
services:
  a:
    image: x
    gpus:
      - driver: nvidia
        count: 1
        capabilities: [gpu, display, bogus]
`, "a"))
	if g.Count != 1 || !slices.Equal(g.Capabilities, []string{"display"}) {
		t.Errorf("got %+v", g)
	}
	if len(g.Notes) != 1 || !strings.Contains(g.Notes[0], "bogus") {
		t.Errorf("unsupported capability should be noted, got %v", g.Notes)
	}
}

func TestComposeGPU_NonGPUDeviceIsNoted(t *testing.T) {
	g := composeGPURequest(parseService(t, `
services:
  a:
    image: x
    deploy:
      resources:
        reservations:
          devices:
            - driver: rdma
              count: 1
              capabilities: [rdma]
`, "a"))
	if g.Count != 0 || len(g.Notes) != 1 {
		t.Errorf("got %+v", g)
	}
}
