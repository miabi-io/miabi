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

package marketplace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

var (
	ErrInstallNotFound  = errors.New("install not found")
	ErrAlreadyLatest    = errors.New("already on the latest version")
	ErrStructuralChange = errors.New("template structure changed; upgrade manually")
)

type InstallAppRef struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`         // unique slug handle
	DisplayName string `json:"display_name"` // free-text label
	Status      string `json:"status"`
}

// Apps are resolved live, so deleted ones drop off.
type InstallView struct {
	models.TemplateInstall
	Apps            []InstallAppRef `json:"apps"`
	LatestVersion   string          `json:"latest_version"`
	UpdateAvailable bool            `json:"update_available"`
}

// Custom imports are preferred; "" means the template is no longer in any catalog.
func (s *Service) latestVersion(workspaceID uint, slug string) string {
	if m, _, ok := s.resolveManifest(workspaceID, slug, ""); ok {
		return m.Metadata.Version
	}
	return ""
}

// Catalog versions are published newest-first, so "different" suffices without a semver parse.
func isNewer(latest, current string) bool {
	latest, current = strings.TrimSpace(latest), strings.TrimSpace(current)
	return latest != "" && latest != current
}

// Error is set (non-fatal) when removal failed.
type RemovedResource struct {
	Kind  string `json:"kind"` // application | config | database | database server | stack | volume
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
}

// Best-effort so a partial install can always be cleaned up.
type UninstallResult struct {
	Removed []RemovedResource `json:"removed"`
	Failed  int               `json:"failed"`
}

func (r *UninstallResult) ok(kind, name string) {
	r.Removed = append(r.Removed, RemovedResource{Kind: kind, Name: name})
}
func (r *UninstallResult) err(kind, name string, e error) {
	r.Removed = append(r.Removed, RemovedResource{Kind: kind, Name: name, Error: e.Error()})
	r.Failed++
}

// Uninstall continues past individual failures and reports them, so a partial install is cleanable.
func (s *Service) Uninstall(ctx context.Context, workspaceID, installID uint) (*UninstallResult, error) {
	rec, err := s.installs.FindInWorkspace(workspaceID, installID)
	if err != nil {
		return nil, ErrInstallNotFound
	}
	res := &UninstallResult{}

	// First, while instances are up so DROP runs; also reaps databases on shared instances absent from DatabaseIDs.
	for _, id := range rec.AppIDs {
		dbs, lerr := s.dbs.ListByApp(workspaceID, id)
		if lerr != nil {
			continue
		}
		for i := range dbs {
			if err := s.dbs.DeleteDatabase(ctx, workspaceID, dbs[i].ID); err != nil {
				res.err("database", dbs[i].Name, err)
			} else {
				res.ok("database", dbs[i].Name)
			}
		}
	}

	// An app must be stopped before it can be deleted.
	for _, id := range rec.AppIDs {
		app, err := s.apps.Get(workspaceID, id)
		if err != nil {
			continue // already gone
		}
		_ = s.apps.Stop(ctx, app) // best-effort; a stopped app has no live container
		if err := s.apps.Delete(ctx, app); err != nil {
			res.err("application", app.Name, err)
		} else {
			res.ok("application", app.Name)
		}
	}
	// Apps are already gone, so withApps=false just deletes the record and reclaims its network.
	if rec.StackID != nil && s.stacks != nil {
		name := fmt.Sprintf("#%d", *rec.StackID)
		if err := s.stacks.Delete(ctx, workspaceID, *rec.StackID, false); err != nil {
			res.err("stack", name, err)
		} else {
			res.ok("stack", name)
		}
	}

	// Delete refuses a running instance; its logical databases are gone, so ErrInstanceInUse no longer applies.
	for _, id := range rec.DatabaseIDs {
		inst, err := s.dbs.Get(workspaceID, id)
		if err != nil {
			continue
		}
		if inst.Status == models.DBStatusRunning {
			_ = s.dbs.Stop(ctx, inst) // best-effort; mutates inst.Status
		}
		if err := s.dbs.Delete(ctx, inst); err != nil {
			res.err("database server", inst.Name, err)
		} else {
			res.ok("database server", inst.Name)
		}
	}

	// After the apps that mount them; a leftover config would be silently adopted by the next install.
	for _, id := range rec.ConfigIDs {
		if s.configs == nil {
			break
		}
		cfg, err := s.configs.Get(workspaceID, id)
		if err != nil {
			continue
		}
		if err := s.configs.Delete(workspaceID, cfg.ID); err != nil {
			res.err("config", cfg.Name, err)
		} else {
			res.ok("config", cfg.Name)
		}
	}

	// After the apps that mount them are gone.
	for _, id := range rec.VolumeIDs {
		vol, err := s.volumes.Get(workspaceID, id)
		if err != nil {
			continue
		}
		if err := s.volumes.Delete(ctx, vol); err != nil {
			res.err("volume", vol.Name, err)
		} else {
			res.ok("volume", vol.Name)
		}
	}

	if err := s.installs.Delete(rec.ID); err != nil {
		res.err("install record", fmt.Sprintf("#%d", rec.ID), err)
	}
	return res, nil
}

var _ = ErrStructuralChange
