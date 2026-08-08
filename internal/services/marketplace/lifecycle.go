// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// InstallAppRef is a lightweight reference to an application an install created,
// for linking from the installs list.
type InstallAppRef struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`         // unique slug handle
	DisplayName string `json:"display_name"` // free-text label
	Status      string `json:"status"`
}

// InstallView is a template install annotated with upgrade availability and the
// applications it created (resolved live, so deleted apps drop off).
type InstallView struct {
	models.TemplateInstall
	Apps            []InstallAppRef `json:"apps"`
	LatestVersion   string          `json:"latest_version"`
	UpdateAvailable bool            `json:"update_available"`
}

// latestVersion returns the newest available version of a template visible to
// the workspace (custom import preferred), or "" if it is no longer in any
// catalog.
func (s *Service) latestVersion(workspaceID uint, slug string) string {
	if m, _, ok := s.resolveManifest(workspaceID, slug, ""); ok {
		return m.Metadata.Version
	}
	return ""
}

// isNewer reports whether latest is a different, non-empty version than current.
// (Versions in the catalog are published newest-first, so "different" is enough
// to flag an available update without a full semver parse.)
func isNewer(latest, current string) bool {
	latest, current = strings.TrimSpace(latest), strings.TrimSpace(current)
	return latest != "" && latest != current
}

// RemovedResource is one resource a teardown attempted to delete, for the
// post-delete follow-up. Error is set (and non-fatal) when removal failed.
type RemovedResource struct {
	Kind  string `json:"kind"` // application | database | database server | stack | volume
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
}

// UninstallResult is the follow-up of an uninstall: every resource the teardown
// touched and which of them failed (best-effort, so a partial install can always
// be cleaned up).
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

// Uninstall tears down everything a template install created — the logical databases its apps use, the
// apps and their stack, the instances and volumes it provisioned — then removes the install record.
// Best-effort: it continues past individual failures and reports them, so a partial install is cleanable.
func (s *Service) Uninstall(ctx context.Context, workspaceID, installID uint) (*UninstallResult, error) {
	rec, err := s.installs.FindInWorkspace(workspaceID, installID)
	if err != nil {
		return nil, ErrInstallNotFound
	}
	res := &UninstallResult{}

	// 1. Logical databases the apps use — deleted first, while their instances are still up so the DROP runs,
	// and so a dedicated instance is not later blocked by ErrInstanceInUse (the app→db link). This also reaps
	// logical databases on shared instances, which are not in DatabaseIDs and would otherwise orphan.
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

	// 2. Stop, then delete each app the install created — an app must be stopped
	// before it can be deleted. (AppIDs covers grouped and ungrouped installs;
	// the stack record itself is removed afterwards.)
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
	// Remove the now-empty stack record for a grouped install (apps already gone,
	// so withApps=false just detaches/deletes the record and reclaims its network).
	if rec.StackID != nil && s.stacks != nil {
		name := fmt.Sprintf("#%d", *rec.StackID)
		if err := s.stacks.Delete(ctx, workspaceID, *rec.StackID, false); err != nil {
			res.err("stack", name, err)
		} else {
			res.ok("stack", name)
		}
	}

	// 3. Database instances this install provisioned (its dedicated servers). Stop
	// a running instance first (Delete refuses while running); its logical
	// databases were already removed above, so ErrInstanceInUse no longer applies.
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

	// 4. Volumes (after the apps that mount them are gone).
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

// Upgrade is implemented in upgrade.go as PlanUpgrade (diff preview) +
// ApplyUpgrade (converge to a target version). ErrStructuralChange is retained
// for the API error mapping.
var _ = ErrStructuralChange
