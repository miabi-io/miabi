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

package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// Ownership is on the object itself so GC and re-import need not consult the DB.
const (
	ConfigWorkspaceLabel = "io.miabi.workspace"
	ConfigNameLabel      = "io.miabi.config"
	ConfigKeyLabel       = "io.miabi.config-key"
	ConfigDigestLabel    = "io.miabi.digest"
)

// The digest suffix makes the name unique, so truncation eats the middle, never the digest.
const maxConfigNameLen = 64

// ConfigInfo is a published config object, as GC sees it.
type ConfigInfo struct {
	ID        string
	Name      string
	Workspace string
	Config    string
	CreatedAt time.Time
}

// ConfigObject is one docker config to publish for a swarm task.
type ConfigObject struct {
	Workspace string // workspace uid
	Config    string // config resource name
	Key       string // file key within the config
	Digest    string // digest12 of the projected file (content + mode)
	Content   string
}

// Content-addressed because docker configs are immutable; a change yields a new object.
func (c ConfigObject) ObjectName() string {
	slug := strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(c.Key)
	base := fmt.Sprintf("miabi-cfg-%s-%s-%s", c.Workspace, c.Config, slug)
	suffix := "-" + c.Digest
	if len(base)+len(suffix) > maxConfigNameLen {
		keep := maxConfigNameLen - len(suffix)
		if keep < 1 {
			keep = 1
		}
		base = base[:keep]
	}
	return base + suffix
}

// Idempotent: the name embeds the digest, so an existing object already holds this content.
func (e *engineClient) EnsureConfig(ctx context.Context, obj ConfigObject) (string, error) {
	name := obj.ObjectName()
	existing, err := e.cli.ConfigList(ctx, client.ConfigListOptions{
		Filters: selectorFilters("name", name),
	})
	if err != nil {
		return "", fmt.Errorf("list configs: %w", err)
	}
	for _, c := range existing.Items {
		if c.Spec.Name == name {
			return c.ID, nil
		}
	}
	created, err := e.cli.ConfigCreate(ctx, client.ConfigCreateOptions{
		Spec: swarm.ConfigSpec{
			Annotations: swarm.Annotations{
				Name: name,
				Labels: map[string]string{
					ManagedLabel:         "true",
					ConfigWorkspaceLabel: obj.Workspace,
					ConfigNameLabel:      obj.Config,
					ConfigKeyLabel:       obj.Key,
					ConfigDigestLabel:    obj.Digest,
				},
			},
			Data: []byte(obj.Content),
		},
	})
	if err != nil {
		return "", fmt.Errorf("create config %s: %w", name, err)
	}
	return created.ID, nil
}

// ListManagedConfigs returns every config object the platform owns, for GC.
func (e *engineClient) ListManagedConfigs(ctx context.Context) ([]ConfigInfo, error) {
	cfgs, err := e.cli.ConfigList(ctx, client.ConfigListOptions{
		Filters: selectorFilters("label", ManagedLabel+"=true"),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ConfigInfo, 0, len(cfgs.Items))
	for _, c := range cfgs.Items {
		out = append(out, ConfigInfo{
			ID:        c.ID,
			Name:      c.Spec.Name,
			Workspace: c.Spec.Labels[ConfigWorkspaceLabel],
			Config:    c.Spec.Labels[ConfigNameLabel],
			CreatedAt: c.CreatedAt,
		})
	}
	return out, nil
}

// Docker refuses while a service references it, which keeps GC from breaking a task.
func (e *engineClient) RemoveConfig(ctx context.Context, id string) error {
	_, err := e.cli.ConfigRemove(ctx, id, client.ConfigRemoveOptions{})
	return wrapNotFound(err)
}
