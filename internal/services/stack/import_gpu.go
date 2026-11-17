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
	"fmt"
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"gopkg.in/yaml.v3"
)

type composeDeploy struct {
	Resources struct {
		Reservations struct {
			Devices []composeDevice `yaml:"devices"`
		} `yaml:"reservations"`
	} `yaml:"resources"`
}

type composeDevice struct {
	Driver       string   `yaml:"driver"`
	Count        any      `yaml:"count"`
	DeviceIDs    []string `yaml:"device_ids"`
	Capabilities []string `yaml:"capabilities"`
}

// composeGPUs reads either `gpus: all` or a list of device entries, the two forms the Compose spec allows.
type composeGPUs []composeDevice

func (g *composeGPUs) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*g = composeGPUs{{Driver: "nvidia", Count: node.Value, Capabilities: []string{"gpu"}}}
		return nil
	}
	var list []composeDevice
	if err := node.Decode(&list); err != nil {
		return err
	}
	*g = list
	return nil
}

type gpuImport struct {
	Count        int
	Capabilities []string
	Notes        []string
}

// Device IDs and "all" cannot carry over: Miabi picks cards on the node and counts whole devices against the plan.
func composeGPURequest(def composeService) gpuImport {
	var out gpuImport
	caps := []string{}
	entries := append(append([]composeDevice{}, def.Deploy.Resources.Reservations.Devices...), def.GPUs...)
	for _, d := range entries {
		if !isGPUDevice(d) {
			out.Notes = append(out.Notes, fmt.Sprintf("device reservation for driver %q is not supported", d.Driver))
			continue
		}
		switch n, all := deviceCount(d.Count); {
		case len(d.DeviceIDs) > 0:
			out.Count += len(d.DeviceIDs)
			out.Notes = append(out.Notes, "GPU device_ids are not kept; Miabi picks the cards on the node")
		case all:
			out.Count++
			out.Notes = append(out.Notes, `GPU count "all" imported as 1 GPU; raise it in the app's resources`)
		default:
			out.Count += n
		}
		for _, c := range d.Capabilities {
			c = strings.ToLower(strings.TrimSpace(c))
			if c == "gpu" || c == "nvidia" || c == "" {
				continue
			}
			if _, err := models.NormalizeGPUCapabilities([]string{c}); err != nil {
				out.Notes = append(out.Notes, fmt.Sprintf("GPU capability %q is not supported", c))
				continue
			}
			caps = append(caps, c)
		}
	}
	if out.Count > 64 {
		out.Count = 64
	}
	if out.Count > 0 {
		out.Capabilities, _ = models.NormalizeGPUCapabilities(caps)
	}
	return out
}

func isGPUDevice(d composeDevice) bool {
	if strings.EqualFold(strings.TrimSpace(d.Driver), "nvidia") {
		return true
	}
	for _, c := range d.Capabilities {
		if strings.EqualFold(strings.TrimSpace(c), "gpu") {
			return true
		}
	}
	return false
}

// Compose defaults an omitted count to all devices.
func deviceCount(v any) (n int, all bool) {
	switch c := v.(type) {
	case nil:
		return 0, true
	case int:
		if c < 0 {
			return 0, true
		}
		return c, false
	case string:
		s := strings.TrimSpace(c)
		if strings.EqualFold(s, "all") || s == "" {
			return 0, true
		}
		if i, err := strconv.Atoi(s); err == nil && i >= 0 {
			return i, false
		}
		return 0, true
	}
	return 0, true
}
