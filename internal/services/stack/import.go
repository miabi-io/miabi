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
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/portbinding"
	"gopkg.in/yaml.v3"
)

var ErrComposeInvalid = errors.New("invalid docker-compose content")

// composeFile is the subset of the Compose spec Miabi imports.
type composeFile struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Image       string      `yaml:"image"`
	Ports       []string    `yaml:"ports"`
	Environment composeEnv  `yaml:"environment"`
	Command     composeList `yaml:"command"`
	Volumes     []string    `yaml:"volumes"`
}

type composeEnv []struct{ Key, Value string }

func (e *composeEnv) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i < len(node.Content); i += 2 {
			*e = append(*e, struct{ Key, Value string }{node.Content[i].Value, node.Content[i+1].Value})
		}
	case yaml.SequenceNode:
		for _, n := range node.Content {
			k, v, _ := strings.Cut(n.Value, "=")
			*e = append(*e, struct{ Key, Value string }{k, v})
		}
	}
	return nil
}

// composeList accepts both the string form ("nginx -g ...") and the list form.
type composeList []string

func (l *composeList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Value != "" {
			*l = strings.Fields(node.Value)
		}
	case yaml.SequenceNode:
		for _, n := range node.Content {
			*l = append(*l, n.Value)
		}
	}
	return nil
}

// ImportSkip records a service that could not be imported.
type ImportSkip struct {
	Service string `json:"service"`
	Reason  string `json:"reason"`
}

// The binding is filed pending so the stack still imports cleanly.
type PortConflict struct {
	Service  string `json:"service"`
	HostPort int    `json:"host_port"`
	Protocol string `json:"protocol"`
	UsedBy   string `json:"used_by"`
}

// ImportResult summarizes a compose import.
type ImportResult struct {
	Stack         *models.Stack  `json:"stack"`
	Created       []string       `json:"created"`
	Volumes       []string       `json:"volumes"`
	PortRequests  int            `json:"port_requests"`
	PortConflicts []PortConflict `json:"port_conflicts"`
	Skipped       []ImportSkip   `json:"skipped"`
}

// ImportPlacement is where an imported stack runs: its location and the single node its apps and volumes share.
type ImportPlacement struct {
	ClusterID uint
	ServerID  uint
}

func (s *Service) ImportCompose(ctx context.Context, workspaceID, userID uint, name, composeYAML string, at ImportPlacement) (*ImportResult, error) {
	var cf composeFile
	if err := yaml.Unmarshal([]byte(composeYAML), &cf); err != nil {
		return nil, ErrComposeInvalid
	}
	if len(cf.Services) == 0 {
		return nil, ErrComposeInvalid
	}

	st, err := s.Create(ctx, workspaceID, Input{
		Name:      name,
		Metadata:  models.SetBuiltin(models.Metadata{}, models.MetaManagedBy, models.ManagedByStackImport),
		ClusterID: at.ClusterID,
	})
	if err != nil {
		return nil, err
	}

	// Empty lists, not nil: a nil slice encodes as null, which the console reads as a list.
	result := &ImportResult{Stack: st, Created: []string{}, Volumes: []string{}, PortConflicts: []PortConflict{}, Skipped: []ImportSkip{}}
	// Named volumes are shared across services, so provision each once and reuse.
	volumes := map[string]*models.Volume{}

	names := make([]string, 0, len(cf.Services))
	for svc := range cf.Services {
		names = append(names, svc)
	}
	sort.Strings(names)

	for _, svc := range names {
		def := cf.Services[svc]
		if strings.TrimSpace(def.Image) == "" {
			result.Skipped = append(result.Skipped, ImportSkip{Service: svc, Reason: "no image (build-only not supported)"})
			continue
		}
		mappings := composePortMappings(def.Ports)
		app, err := s.app.Create(workspaceID, application.CreateInput{
			DisplayName: svc,
			Handle:      svc,
			SourceType:  models.AppSourceImage,
			Image:       strings.TrimSpace(def.Image),
			StackID:     &st.ID,
			ServerID:    at.ServerID,
			Ports:       portSpecs(mappings),
			Command:     def.Command,
			Metadata: models.SetBuiltin(models.Metadata{},
				models.MetaManagedBy, models.ManagedByStackImport,
				models.MetaStack, st.DockerName),
		})
		if err != nil {
			result.Skipped = append(result.Skipped, ImportSkip{Service: svc, Reason: err.Error()})
			continue
		}
		for _, kv := range def.Environment {
			_ = s.app.SetEnvVar(app.ID, kv.Key, kv.Value, false)
		}
		s.importVolumes(ctx, workspaceID, at.ServerID, st, svc, app, def.Volumes, volumes, result)
		s.importPorts(workspaceID, userID, svc, app, mappings, result)
		result.Created = append(result.Created, app.Name)
	}
	return result, nil
}

func (s *Service) importPorts(workspaceID, userID uint, svc string, app *models.Application, mappings []portMapping, result *ImportResult) {
	if s.ports == nil {
		return
	}
	for _, m := range mappings {
		if m.Host == 0 {
			continue // not published to the host
		}
		// Never fails on a conflict, so a stack imports without a container crashing on a taken port.
		_, conflict, err := s.ports.RequestImport(workspaceID, userID, portbinding.RequestInput{
			ApplicationID: app.ID, ContainerPort: m.Container, HostPort: m.Host, Protocol: m.Proto,
		})
		if err != nil {
			result.Skipped = append(result.Skipped, ImportSkip{Service: svc, Reason: fmt.Sprintf("port %d:%d: %s", m.Host, m.Container, err.Error())})
			continue
		}
		result.PortRequests++
		if conflict != "" {
			result.PortConflicts = append(result.PortConflicts, PortConflict{Service: svc, HostPort: m.Host, Protocol: m.Proto, UsedBy: conflict})
		}
	}
}

// importVolumes provisions and attaches the named volumes a service declares.
func (s *Service) importVolumes(ctx context.Context, workspaceID, serverID uint, st *models.Stack, svc string, app *models.Application, decls []string, volumes map[string]*models.Volume, result *ImportResult) {
	for _, raw := range decls {
		m, ok := parseComposeVolume(raw)
		if !ok {
			continue
		}
		if m.Bind {
			result.Skipped = append(result.Skipped, ImportSkip{Service: svc, Reason: "bind mount not supported: " + raw})
			continue
		}
		// The named volume is shared; anonymous volumes are per service+path.
		key := m.Source
		if key == "" {
			key = svc + ":" + m.Target
		}
		vol := volumes[key]
		if vol == nil {
			volMeta := models.SetOwner(
				models.SetBuiltin(models.Metadata{}, models.MetaManagedBy, models.ManagedByStackImport, models.MetaStack, st.DockerName),
				models.OwnerStack, st.ID, st.Name)
			v, err := s.volumes.Create(ctx, workspaceID, serverID, volumeName(st.Name, m.Source, svc), 0, volMeta, nil)
			if err != nil {
				result.Skipped = append(result.Skipped, ImportSkip{Service: svc, Reason: "volume " + raw + ": " + err.Error()})
				continue
			}
			vol = v
			volumes[key] = v
			result.Volumes = append(result.Volumes, v.Name)
		}
		if err := s.app.AttachVolume(app, vol.ID, m.Target); err != nil {
			result.Skipped = append(result.Skipped, ImportSkip{Service: svc, Reason: "attach " + raw + ": " + err.Error()})
		}
	}
}

func volumeName(stackName, source, svc string) string {
	if source != "" {
		return stackName + "-" + source
	}
	return stackName + "-" + svc + "-data"
}

// composeMount is a parsed entry from a service's "volumes:" list.
type composeMount struct {
	Source string // named volume (empty = anonymous volume)
	Target string // container path
	Bind   bool   // host bind mount (unsupported)
}

func parseComposeVolume(v string) (composeMount, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return composeMount{}, false
	}
	parts := strings.Split(v, ":")
	if len(parts) == 1 { // anonymous volume: container path only
		return composeMount{Target: parts[0]}, true
	}
	source, target := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if target == "" {
		return composeMount{}, false
	}
	// A source containing a path separator (or "." / "~") is a host bind mount.
	if strings.ContainsAny(source, "/.~") {
		return composeMount{Source: source, Target: target, Bind: true}, true
	}
	return composeMount{Source: source, Target: target}, true
}

// Host is 0 when the port is only exposed, not published.
type portMapping struct {
	Host      int
	Container int
	Proto     string
}

func composePortMappings(ports []string) []portMapping {
	out := make([]portMapping, 0, len(ports))
	for _, p := range ports {
		if m, ok := parsePortMapping(p); ok {
			out = append(out, m)
		}
	}
	return out
}

func parsePortMapping(p string) (portMapping, bool) {
	proto := "tcp"
	if v, after, found := strings.Cut(p, "/"); found {
		p = v
		if after == "udp" {
			proto = "udp"
		}
	}
	parts := strings.Split(strings.TrimSpace(p), ":")
	container, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1]))
	if err != nil || container <= 0 {
		return portMapping{}, false
	}
	host := 0
	if len(parts) >= 2 { // the segment before the container port is the host port
		if h, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-2])); err == nil {
			host = h
		}
	}
	return portMapping{Host: host, Container: container, Proto: proto}, true
}

// portSpecs maps parsed port mappings to declared container ports.
func portSpecs(mappings []portMapping) []application.PortSpec {
	out := make([]application.PortSpec, 0, len(mappings))
	for _, m := range mappings {
		out = append(out, application.PortSpec{ContainerPort: m.Container, Protocol: m.Proto, Name: fmt.Sprintf("port-%d", m.Container)})
	}
	return out
}
