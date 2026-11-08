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
	"fmt"
	"github.com/miabi-io/miabi/internal/services/placement"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	configsvc "github.com/miabi-io/miabi/internal/services/config"
	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
)

type EnvChange struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`      // added | removed | changed
	Secret    bool   `json:"secret"`    // key is in the new spec's secretEnv
	Templated bool   `json:"templated"` // value contains {{ }} (interpolated on apply)
}

// AppChange is the per-application diff between two template versions.
type AppChange struct {
	AppID        uint        `json:"app_id"`
	Name         string      `json:"name"` // template service name
	OldImage     string      `json:"old_image,omitempty"`
	NewImage     string      `json:"new_image,omitempty"`
	ImageChanged bool        `json:"image_changed"`
	Env          []EnvChange `json:"env"`
	NewMounts    []string    `json:"new_mounts"` // newly-mounted volume names
}

// UpgradePlan is the preview of upgrading an install from one version to another.
type UpgradePlan struct {
	FromVersion  string           `json:"from_version"`
	ToVersion    string           `json:"to_version"`
	Apps         []AppChange      `json:"apps"`
	StackEnv     []EnvChange      `json:"stack_env"` // shared stack env changes (added applied; changed/removed warned)
	NewVolumes   []string         `json:"new_volumes"`
	NewConfigs   []string         `json:"new_configs"` // configs the new version ships that the install lacks
	NewDatabases []string         `json:"new_databases"`
	AddedApps    []string         `json:"added_apps"`   // in the new version, not installed
	RemovedApps  []string         `json:"removed_apps"` // installed, dropped by the new version
	NewInputs    []manifest.Input `json:"new_inputs"`
	Warnings     []string         `json:"warnings"`
	// Releases are the notes of every version the upgrade crosses, oldest first.
	Releases []ReleaseNote `json:"releases"`
	Breaking bool          `json:"breaking"`
	// LatestVersion differs from ToVersion when a requiresFrom rule makes the install step through ToVersion first.
	LatestVersion string `json:"latest_version"`
}

type upgradeInfo struct {
	releases []ReleaseNote
	breaking bool
	latest   string
	pins     map[string]string
}

// UpgradeApplyResult records what an apply actually did, per item.
type UpgradeApplyResult struct {
	FromVersion string   `json:"from_version"`
	ToVersion   string   `json:"to_version"`
	AppsBumped  []string `json:"apps_bumped"` // apps whose image/env changed + redeployed
	EnvApplied  []string `json:"env_applied"` // "app:KEY" env values set
	NewVolumes  []string `json:"new_volumes"` // volumes created
	NewConfigs  []string `json:"new_configs"` // configs created
	Warnings    []string `json:"warnings"`    // items surfaced but not applied
}

// target "" means the latest installable version, or the furthest one a requiresFrom rule lets the install reach.
func (s *Service) resolveUpgrade(workspaceID, installID uint, target string) (rec *models.TemplateInstall, newM, oldM *manifest.Manifest, byName map[string]uint, info upgradeInfo, err error) {
	rec, e := s.installs.FindInWorkspace(workspaceID, installID)
	if e != nil {
		return nil, nil, nil, nil, info, ErrInstallNotFound
	}
	explicit := target != ""
	if !explicit {
		target = s.installable(workspaceID, rec.TemplateName)
	}
	m, source, ok := s.resolveManifest(workspaceID, rec.TemplateName, target)
	if !ok {
		return nil, nil, nil, nil, info, ErrTemplateNotFound
	}
	info.latest = m.Metadata.Version
	if t, found := s.remoteTemplate(rec.TemplateName, source); found {
		reachable, blocked := upgradePath(t, rec.Version, m.Metadata.Version)
		if blocked != nil {
			if explicit || reachable == rec.Version {
				return nil, nil, nil, nil, info, fmt.Errorf("%w: %s %s requires upgrading from %s or later first; this install is on %s",
					ErrUpgradeBlocked, rec.TemplateName, blocked.Version, blocked.Release.RequiresFrom, rec.Version)
			}
			if m, _, ok = s.resolveManifest(workspaceID, rec.TemplateName, reachable); !ok {
				return nil, nil, nil, nil, info, ErrTemplateNotFound
			}
		}
		info.releases = releaseNotes(t, rec.Version, m.Metadata.Version)
		for _, r := range info.releases {
			info.breaking = info.breaking || r.Breaking
		}
		info.pins = s.releaseOf(rec.TemplateName, source, m.Metadata.Version).Images
	}
	if !isNewer(m.Metadata.Version, rec.Version) {
		return nil, nil, nil, nil, info, ErrAlreadyLatest
	}
	// Tolerate a pruned old version by diffing against an empty manifest.
	old, _, _ := s.resolveManifest(workspaceID, rec.TemplateName, rec.Version)

	// AppIDs line up with the installed manifest's Applications because apps were created in that order.
	byName = map[string]uint{}
	if old != nil {
		for i, app := range old.Applications {
			if i < len(rec.AppIDs) {
				byName[app.Name] = rec.AppIDs[i]
			}
		}
	} else if len(rec.AppIDs) == len(m.Applications) {
		for i, app := range m.Applications {
			byName[app.Name] = rec.AppIDs[i]
		}
	}
	return rec, m, old, byName, info, nil
}

func (s *Service) PlanUpgrade(workspaceID, installID uint, target string) (*UpgradePlan, error) {
	rec, newM, oldM, byName, info, err := s.resolveUpgrade(workspaceID, installID, target)
	if err != nil {
		return nil, err
	}
	plan := &UpgradePlan{
		FromVersion:   rec.Version,
		ToVersion:     newM.Metadata.Version,
		LatestVersion: info.latest,
		Releases:      info.releases,
		Breaking:      info.breaking,
		Apps:          []AppChange{},
		StackEnv:      []EnvChange{},
		NewVolumes:    []string{},
		NewConfigs:    []string{},
		NewDatabases:  []string{},
		AddedApps:     []string{},
		RemovedApps:   []string{},
		NewInputs:     []manifest.Input{},
		Warnings:      []string{},
	}
	if info.latest != plan.ToVersion {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s can't be reached from %s in one step; this upgrades to %s, then upgrade again", info.latest, rec.Version, plan.ToVersion))
	}

	oldApps := specsByName(oldM)
	newApps := specsByName(newM)

	for name, ns := range newApps {
		os, existed := oldApps[name]
		if !existed {
			plan.AddedApps = append(plan.AddedApps, name)
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("app %q is new in this version — create it manually (not added automatically)", name))
			continue
		}
		ch := AppChange{AppID: byName[name], Name: name}
		oldImg, newImg := imageRef(os), imageRef(ns)
		if oldImg != newImg {
			ch.ImageChanged, ch.OldImage, ch.NewImage = true, oldImg, newImg
		}
		ch.Env = diffEnv(os, ns, &plan.Warnings)
		ch.NewMounts = newMounts(os, ns)
		plan.Apps = append(plan.Apps, ch)
	}
	for name := range oldApps {
		if _, ok := newApps[name]; !ok {
			plan.RemovedApps = append(plan.RemovedApps, name)
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("app %q was removed in this version — left running; delete it manually if desired", name))
		}
	}

	// Shared stack env: added keys apply on upgrade; changed/removed are warned.
	plan.StackEnv = diffStackEnv(oldM, newM)
	if len(stackEnvOf(newM)) > 0 && rec.StackID == nil {
		plan.Warnings = append(plan.Warnings, "this version shares configuration across a stack, but this install has no stack — reinstall to adopt it")
	}

	plan.NewVolumes = added(volNames(oldM), volNames(newM))
	plan.NewConfigs = diffConfigs(oldM, newM, &plan.Warnings)
	plan.NewDatabases = added(dbNames(oldM), dbNames(newM))
	for _, d := range plan.NewDatabases {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("database dependency %q is new in this version — provision it manually (not added automatically)", d))
	}
	plan.NewInputs = newInputs(oldM, newM, rec.Inputs)
	return plan, nil
}

// With none of its apps left, a new volume is placed like any create rather than on the control-plane node.
func (s *Service) upgradeNode(workspaceID uint, appIDs []uint) uint {
	for _, id := range appIDs {
		if app, err := s.apps.Get(workspaceID, id); err == nil {
			return app.ServerID
		}
	}
	if s.placer != nil {
		if res, err := s.placer.Place(placement.Request{WorkspaceID: workspaceID}); err == nil {
			return res.ServerID
		}
	}
	return 0
}

// Changed or removed env, new databases and structural app changes are only warnings, never applied.
func (s *Service) ApplyUpgrade(ctx context.Context, workspaceID, installID uint, target string, newInputs map[string]string) (*UpgradeApplyResult, error) {
	rec, newM, oldM, byName, info, err := s.resolveUpgrade(workspaceID, installID, target)
	if err != nil {
		return nil, err
	}
	res := &UpgradeApplyResult{
		FromVersion: rec.Version,
		ToVersion:   newM.Metadata.Version,
		AppsBumped:  []string{},
		EnvApplied:  []string{},
		NewVolumes:  []string{},
		NewConfigs:  []string{},
		Warnings:    []string{},
	}

	// Merge any newly-answered inputs over the stored ones (used to render added env).
	inputs := map[string]string{}
	for k, v := range rec.Inputs {
		inputs[k] = v
	}
	for k, v := range newInputs {
		inputs[k] = v
	}

	volIDs := map[string]uint{}
	volNode := s.upgradeNode(workspaceID, rec.AppIDs)
	for _, v := range added(volNames(oldM), volNames(newM)) {
		meta := models.SetBuiltin(models.Metadata{},
			models.MetaManagedBy, models.ManagedByMarketplace,
			models.MetaTemplate, newM.Metadata.Name,
			models.MetaTemplateVersion, newM.Metadata.Version)
		vol, verr := s.volumes.Create(ctx, workspaceID, volNode, sanitizeName(newM.Metadata.Name+"-"+v), 0, meta, nil)
		if verr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("create volume %q: %v", v, verr))
			continue
		}
		volIDs[v] = vol.ID
		rec.VolumeIDs = append(rec.VolumeIDs, vol.ID)
		res.NewVolumes = append(res.NewVolumes, vol.Name)
	}
	for _, d := range added(dbNames(oldM), dbNames(newM)) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("database dependency %q not provisioned automatically — add it manually", d))
	}

	oldApps := specsByName(oldM)
	newApps := specsByName(newM)

	// App aliases feed the env and config-file interpolation that references siblings.
	appViews := map[string]manifest.AppView{}
	for name, appID := range byName {
		if app, gerr := s.apps.Get(workspaceID, appID); gerr == nil {
			appViews[name] = manifest.AppView{Alias: appAlias(app)}
		}
	}
	renderer := manifest.NewRenderer(manifest.Context{Inputs: inputs, Applications: appViews})

	// An existing config is never rewritten: it may have been edited since.
	cfgIDs := s.upgradeConfigs(workspaceID, rec, oldM, newM, renderer, res)

	deployed := map[uint]bool{}

	for name, ns := range newApps {
		appID, matched := byName[name]
		if !matched {
			res.Warnings = append(res.Warnings, fmt.Sprintf("app %q is new — create it manually", name))
			continue
		}
		app, gerr := s.apps.Get(workspaceID, appID)
		if gerr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("app %q (#%d) not found — skipped", name, appID))
			continue
		}
		os := oldApps[name]
		changed := false

		app.Metadata = models.SetBuiltin(app.Metadata, models.MetaTemplateVersion, newM.Metadata.Version)

		imageChanged := imageRef(os) != imageRef(ns)
		if imageChanged {
			app.Image, app.Tag = ns.Image, ns.Tag
		}
		if pinChanged := setPin(app, info.pins[name], imageChanged); pinChanged {
			imageChanged = true
		}
		if uerr := s.apps.Update(app); uerr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("update %q: %v", name, uerr))
		} else if imageChanged {
			changed = true
		}

		secret := secretSet(ns.SecretEnv)
		for key, tmpl := range ns.Env {
			if _, existed := os.Env[key]; existed {
				if os.Env[key] != tmpl {
					res.Warnings = append(res.Warnings, fmt.Sprintf("env %s:%s changed in the template — review and apply manually (kept current value)", name, key))
				}
				continue
			}
			rendered, rerr := renderEnvValue(renderer, name, key, tmpl)
			if rerr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("env %s:%s references data that can't be resolved on upgrade — set it manually", name, key))
				continue
			}
			if serr := s.apps.SetEnvVar(app.ID, key, rendered, secret[key]); serr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("set env %s:%s: %v", name, key, serr))
				continue
			}
			res.EnvApplied = append(res.EnvApplied, name+":"+key)
			changed = true
		}
		for key := range os.Env {
			if _, ok := ns.Env[key]; !ok {
				res.Warnings = append(res.Warnings, fmt.Sprintf("env %s:%s was removed from the template — left in place; remove it manually if desired", name, key))
			}
		}

		for _, mt := range ns.Mounts {
			if !isMountNew(os, mt) {
				continue
			}
			if mt.Config != "" {
				cid, ok := cfgIDs[mt.Config]
				if !ok {
					res.Warnings = append(res.Warnings, fmt.Sprintf("new mount %s on %q references a config that could not be resolved — attach manually", mountLabel(mt), name))
					continue
				}
				if aerr := s.apps.AttachConfig(app, cid, mt.Key, mt.Path, mt.Mode); aerr != nil {
					res.Warnings = append(res.Warnings, fmt.Sprintf("mount %s on %q: %v", mountLabel(mt), name, aerr))
					continue
				}
				changed = true
				continue
			}
			vid, ok := volIDs[mt.Volume]
			if !ok {
				res.Warnings = append(res.Warnings, fmt.Sprintf("new mount %s on %q references a volume not created by this upgrade — attach manually", mt.Volume, name))
				continue
			}
			if aerr := s.apps.AttachVolume(app, vid, mt.Path); aerr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("mount %s on %q: %v", mt.Volume, name, aerr))
				continue
			}
			changed = true
		}

		if changed {
			full, ferr := s.apps.Get(workspaceID, appID)
			if ferr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("reload %q before deploy: %v", name, ferr))
				continue
			}
			if _, derr := s.apps.Deploy(full, nil, "", ""); derr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("redeploy %q: %v", name, derr))
				continue
			}
			deployed[appID] = true
			res.AppsBumped = append(res.AppsBumped, name)
		}
	}
	for name := range oldApps {
		if _, ok := newApps[name]; !ok {
			res.Warnings = append(res.Warnings, fmt.Sprintf("app %q was dropped by this version — left running", name))
		}
	}

	// Shared env reaches containers only at deploy, so members not already bumped are redeployed.
	s.applyStackEnv(workspaceID, rec, oldM, newM, byName, renderer, deployed, res)

	rec.Version = newM.Metadata.Version
	if len(newInputs) > 0 {
		if rec.Inputs == nil {
			rec.Inputs = map[string]string{}
		}
		for k, v := range newInputs {
			rec.Inputs[k] = v
		}
	}
	if uerr := s.installs.Update(rec); uerr != nil {
		logger.Error("record template upgrade", "install", installID, "error", uerr)
		return res, uerr
	}
	return res, nil
}

func (s *Service) upgradeConfigs(workspaceID uint, rec *models.TemplateInstall, oldM, newM *manifest.Manifest, renderer *manifest.Renderer, res *UpgradeApplyResult) map[string]uint {
	ids := map[string]uint{}
	if len(newM.Configs) == 0 {
		return ids
	}
	if s.configs == nil {
		res.Warnings = append(res.Warnings, "this version ships configuration files, but configs are unavailable on this server — mount them manually")
		return ids
	}
	oldCfgs := configsByName(oldM)
	installed := rec.ConfigIDs

	legacyName := func(name string) string { return sanitizeName(newM.Metadata.Name + "-" + name) }
	resolve := func(name string) (*models.Config, bool) {
		if id := installed[name]; id != 0 {
			if cfg, err := s.configs.Get(workspaceID, id); err == nil {
				return cfg, true
			}
		}
		if cfg, err := s.configs.GetByName(workspaceID, legacyName(name)); err == nil {
			return cfg, true
		}
		return nil, false
	}

	for _, c := range newM.Configs {
		if oc, existed := oldCfgs[c.Name]; existed {
			cur, found := resolve(c.Name)
			if !found {
				res.Warnings = append(res.Warnings, fmt.Sprintf("config %q is missing from this workspace — recreate it manually", c.Name))
				continue
			}
			ids[c.Name] = cur.ID
			if !sameFiles(oc.Files, c.Files) {
				res.Warnings = append(res.Warnings, fmt.Sprintf("config %q changed in the template — review and apply manually (kept current content)", cur.Name))
			}
			continue
		}
		// Named like the install's other resources rather than after the template.
		wsName, nerr := s.configName(workspaceID, installLabel(rec, newM), c.Name)
		if nerr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("name config %q: %v", c.Name, nerr))
			continue
		}
		files, rerr := renderer.RenderFiles(c.Name, c.Files, c.Delimiters)
		if rerr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("config %q references data that can't be resolved on upgrade — create it manually", wsName))
			continue
		}
		cfg, cerr := s.configs.UpsertOwned(workspaceID, models.OwnerTemplateInstall, 0, configsvc.Input{
			Name:        wsName,
			DisplayName: installLabel(rec, newM) + " " + c.Name,
			Data:        files,
			Mode:        c.Mode,
			Sensitive:   c.Sensitive,
			Delimiters:  c.Delimiters,
		})
		if cerr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("create config %q: %v", wsName, cerr))
			continue
		}
		ids[c.Name] = cfg.ID
		res.NewConfigs = append(res.NewConfigs, cfg.Name)
	}
	for name := range oldCfgs {
		if !hasConfig(newM, name) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("config %q was removed from the template — left in place; delete it manually if desired", name))
		}
	}
	// The mount pass and the next upgrade resolve through the record.
	if s.installs != nil && len(ids) > 0 {
		rec.ConfigIDs = ids
	}
	return ids
}

func installLabel(rec *models.TemplateInstall, m *manifest.Manifest) string {
	if rec != nil && strings.TrimSpace(rec.DisplayName) != "" {
		return rec.DisplayName
	}
	if rec != nil && strings.TrimSpace(rec.TemplateDisplayName) != "" {
		return rec.TemplateDisplayName
	}
	return displayName(m, "")
}

func (s *Service) applyStackEnv(workspaceID uint, rec *models.TemplateInstall, oldM, newM *manifest.Manifest, byName map[string]uint, renderer *manifest.Renderer, deployed map[uint]bool, res *UpgradeApplyResult) {
	oldEnv, newEnv := stackEnvOf(oldM), stackEnvOf(newM)
	if len(oldEnv) == 0 && len(newEnv) == 0 {
		return
	}

	if len(newEnv) > 0 && (rec.StackID == nil || s.stacks == nil) {
		res.Warnings = append(res.Warnings, "this version shares configuration across a stack, but this install has no stack — reinstall to adopt it")
		return
	}

	secret := secretSet(stackSecretEnv(newM))
	applied := false
	for key, tmpl := range newEnv {
		if ov, existed := oldEnv[key]; existed {
			if ov != tmpl {
				res.Warnings = append(res.Warnings, fmt.Sprintf("stack env %s changed in the template — review and apply manually (kept current value)", key))
			}
			continue
		}
		rendered, rerr := renderEnvValue(renderer, "stack", key, tmpl)
		if rerr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("stack env %s references data that can't be resolved on upgrade — set it manually", key))
			continue
		}
		if _, serr := s.stacks.SetEnvVar(workspaceID, *rec.StackID, key, rendered, secret[key]); serr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("set stack env %s: %v", key, serr))
			continue
		}
		res.EnvApplied = append(res.EnvApplied, "stack:"+key)
		applied = true
	}
	for key := range oldEnv {
		if _, ok := newEnv[key]; !ok {
			res.Warnings = append(res.Warnings, fmt.Sprintf("stack env %s was removed from the template — left in place; remove it manually if desired", key))
		}
	}
	if !applied {
		return
	}

	// Propagate the new shared env to every member that wasn't already redeployed.
	for name, appID := range byName {
		if deployed[appID] {
			continue
		}
		full, ferr := s.apps.Get(workspaceID, appID)
		if ferr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("reload %q before deploy: %v", name, ferr))
			continue
		}
		if _, derr := s.apps.Deploy(full, nil, "", ""); derr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("redeploy %q: %v", name, derr))
			continue
		}
		deployed[appID] = true
		res.AppsBumped = append(res.AppsBumped, name)
	}
}

func specsByName(m *manifest.Manifest) map[string]manifest.AppSpec {
	out := map[string]manifest.AppSpec{}
	if m == nil {
		return out
	}
	for _, a := range m.Applications {
		out[a.Name] = a
	}
	return out
}

func imageRef(a manifest.AppSpec) string {
	if a.Tag != "" {
		return a.Image + ":" + a.Tag
	}
	return a.Image
}

func diffEnv(oldA, newA manifest.AppSpec, warnings *[]string) []EnvChange {
	secret := secretSet(newA.SecretEnv)
	out := []EnvChange{}
	for k, nv := range newA.Env {
		ov, existed := oldA.Env[k]
		switch {
		case !existed:
			out = append(out, EnvChange{Key: k, Kind: "added", Secret: secret[k], Templated: isTemplated(nv)})
		case ov != nv:
			out = append(out, EnvChange{Key: k, Kind: "changed", Secret: secret[k], Templated: isTemplated(nv)})
		}
	}
	for k := range oldA.Env {
		if _, ok := newA.Env[k]; !ok {
			out = append(out, EnvChange{Key: k, Kind: "removed"})
		}
	}
	return out
}

func stackEnvOf(m *manifest.Manifest) map[string]string {
	if m == nil || m.Stack == nil {
		return nil
	}
	return m.Stack.Env
}

func stackSecretEnv(m *manifest.Manifest) []string {
	if m == nil || m.Stack == nil {
		return nil
	}
	return m.Stack.SecretEnv
}

func diffStackEnv(oldM, newM *manifest.Manifest) []EnvChange {
	oldE, newE := stackEnvOf(oldM), stackEnvOf(newM)
	secret := secretSet(stackSecretEnv(newM))
	var out []EnvChange
	for k, nv := range newE {
		ov, existed := oldE[k]
		switch {
		case !existed:
			out = append(out, EnvChange{Key: k, Kind: "added", Secret: secret[k], Templated: isTemplated(nv)})
		case ov != nv:
			out = append(out, EnvChange{Key: k, Kind: "changed", Secret: secret[k], Templated: isTemplated(nv)})
		}
	}
	for k := range oldE {
		if _, ok := newE[k]; !ok {
			out = append(out, EnvChange{Key: k, Kind: "removed"})
		}
	}
	return out
}

func newMounts(oldA, newA manifest.AppSpec) []string {
	had := map[string]bool{}
	for _, m := range oldA.Mounts {
		had[mountKey(m)] = true
	}
	out := []string{}
	for _, m := range newA.Mounts {
		if !had[mountKey(m)] {
			out = append(out, mountLabel(m))
		}
	}
	return out
}

func isMountNew(oldA manifest.AppSpec, mt manifest.Mount) bool {
	for _, m := range oldA.Mounts {
		if mountKey(m) == mountKey(mt) {
			return false
		}
	}
	return true
}

// A config mount carries no volume, so keying on volume name alone collapsed all config mounts.
func mountKey(m manifest.Mount) string {
	if m.Config != "" {
		return "config:" + m.Config + "/" + m.Key
	}
	return "volume:" + m.Volume
}

// mountLabel names a mount for a plan entry or a warning.
func mountLabel(m manifest.Mount) string {
	if m.Config == "" {
		return m.Volume
	}
	if m.Key != "" {
		return "config " + m.Config + "/" + m.Key
	}
	return "config " + m.Config
}

func configsByName(m *manifest.Manifest) map[string]manifest.Config {
	out := map[string]manifest.Config{}
	if m == nil {
		return out
	}
	for _, c := range m.Configs {
		out[c.Name] = c
	}
	return out
}

func hasConfig(m *manifest.Manifest, name string) bool {
	_, ok := configsByName(m)[name]
	return ok
}

// sameFiles compares two configs' file sets by key and content.
func sameFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// Changed or dropped configs only warn; neither is applied automatically.
func diffConfigs(oldM, newM *manifest.Manifest, warnings *[]string) []string {
	oldCfgs, newCfgs := configsByName(oldM), configsByName(newM)
	out := []string{} // non-nil so JSON is [] not null (the UI reads .length)
	for _, c := range newM.Configs {
		oc, existed := oldCfgs[c.Name]
		switch {
		case !existed:
			out = append(out, c.Name)
		case !sameFiles(oc.Files, c.Files):
			*warnings = append(*warnings, fmt.Sprintf("config %q changed in this version — review and apply manually (current content is kept)", c.Name))
		}
	}
	for name := range oldCfgs {
		if _, ok := newCfgs[name]; !ok {
			*warnings = append(*warnings, fmt.Sprintf("config %q was removed in this version — left in place; delete it manually if desired", name))
		}
	}
	return out
}

func volNames(m *manifest.Manifest) map[string]bool {
	out := map[string]bool{}
	if m != nil {
		for _, v := range m.Volumes {
			out[v.Name] = true
		}
	}
	return out
}

func dbNames(m *manifest.Manifest) map[string]bool {
	out := map[string]bool{}
	if m != nil {
		for _, d := range m.Databases {
			out[d.Name] = true
		}
	}
	return out
}

// added returns the keys present in cur but not in old.
func added(old, cur map[string]bool) []string {
	var out []string
	for k := range cur {
		if !old[k] {
			out = append(out, k)
		}
	}
	return out
}

func newInputs(oldM, newM *manifest.Manifest, answered map[string]string) []manifest.Input {
	if newM == nil {
		return nil
	}
	oldKeys := map[string]bool{}
	if oldM != nil {
		for _, in := range oldM.Inputs {
			oldKeys[in.Key] = true
		}
	}
	var out []manifest.Input
	for _, in := range newM.Inputs {
		if _, has := answered[in.Key]; has {
			continue
		}
		if !oldKeys[in.Key] {
			out = append(out, in)
		}
	}
	return out
}

func isTemplated(v string) bool { return strings.Contains(v, "{{") }

func renderEnvValue(r *manifest.Renderer, appName, key, tmpl string) (string, error) {
	if !isTemplated(tmpl) {
		return tmpl, nil
	}
	rendered, err := r.RenderEnv(appName, map[string]string{key: tmpl})
	if err != nil {
		return "", err
	}
	return rendered[key], nil
}

// setPin moves the digest pin with the template; an unchanged image only redeploys when an existing pin moved.
func setPin(app *models.Application, pin string, imageChanged bool) bool {
	cur := app.Metadata[models.MetaDigest]
	switch {
	case pin == cur:
		return false
	case pin == "" && imageChanged:
		delete(app.Metadata, models.MetaDigest)
		return false
	case pin == "":
		return false
	}
	app.Metadata = models.SetBuiltin(app.Metadata, models.MetaDigest, pin)
	return cur != "" && !imageChanged
}
