// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/swarm"
)

// Config object labels. Ownership is recorded on the object itself so GC and a
// re-import can identify what a config belongs to without consulting the DB.
const (
	ConfigWorkspaceLabel = "io.miabi.workspace"
	ConfigNameLabel      = "io.miabi.config"
	ConfigKeyLabel       = "io.miabi.config-key"
	ConfigDigestLabel    = "io.miabi.digest"
)

// maxConfigNameLen bounds a docker object name. The digest suffix is what makes
// the name unique, so truncation eats the middle and never the digest.
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

// ObjectName is the docker object name: content-addressed, so a content change
// yields a new object and the service update swaps it rather than mutating one
// in place (docker configs are immutable by design).
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

// EnsureConfig creates the config object if it does not exist and returns its id.
// Idempotent: the name embeds the digest, so an existing object with that name
// already holds exactly this content.
func (e *engineClient) EnsureConfig(ctx context.Context, obj ConfigObject) (string, error) {
	name := obj.ObjectName()
	existing, err := e.cli.ConfigList(ctx, types.ConfigListOptions{
		Filters: filters.NewArgs(filters.Arg("name", name)),
	})
	if err != nil {
		return "", fmt.Errorf("list configs: %w", err)
	}
	for _, c := range existing {
		if c.Spec.Name == name {
			return c.ID, nil
		}
	}
	created, err := e.cli.ConfigCreate(ctx, swarm.ConfigSpec{
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
	})
	if err != nil {
		return "", fmt.Errorf("create config %s: %w", name, err)
	}
	return created.ID, nil
}

// ListManagedConfigs returns every config object the platform owns, for GC.
func (e *engineClient) ListManagedConfigs(ctx context.Context) ([]ConfigInfo, error) {
	cfgs, err := e.cli.ConfigList(ctx, types.ConfigListOptions{
		Filters: filters.NewArgs(filters.Arg("label", ManagedLabel+"=true")),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ConfigInfo, 0, len(cfgs))
	for _, c := range cfgs {
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

// RemoveConfig deletes a config object. Docker refuses while a service still
// references it, which is the safety net that keeps GC from breaking a task.
func (e *engineClient) RemoveConfig(ctx context.Context, id string) error {
	return wrapNotFound(e.cli.ConfigRemove(ctx, id))
}
