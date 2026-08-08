// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package marketplace

import (
	"context"
	"fmt"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
)

// EnvChange is a single env key that differs between the installed template
// version and the target version.
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
	NewDatabases []string         `json:"new_databases"`
	AddedApps    []string         `json:"added_apps"`   // in the new version, not installed
	RemovedApps  []string         `json:"removed_apps"` // installed, dropped by the new version
	NewInputs    []manifest.Input `json:"new_inputs"`
	Warnings     []string         `json:"warnings"`
}

// UpgradeApplyResult records what an apply actually did, per item.
type UpgradeApplyResult struct {
	FromVersion string   `json:"from_version"`
	ToVersion   string   `json:"to_version"`
	AppsBumped  []string `json:"apps_bumped"` // apps whose image/env changed + redeployed
	EnvApplied  []string `json:"env_applied"` // "app:KEY" env values set
	NewVolumes  []string `json:"new_volumes"` // volumes created
	Warnings    []string `json:"warnings"`    // items surfaced but not applied
}

// resolveUpgrade loads the install, the new (target) manifest, and the installed
// version's manifest, and the name→appID map for the apps it created. target ""
// means the latest available version.
func (s *Service) resolveUpgrade(workspaceID, installID uint, target string) (rec *models.TemplateInstall, newM, oldM *manifest.Manifest, byName map[string]uint, err error) {
	rec, e := s.installs.FindInWorkspace(workspaceID, installID)
	if e != nil {
		return nil, nil, nil, nil, ErrInstallNotFound
	}
	m, _, ok := s.resolveManifest(workspaceID, rec.TemplateName, target)
	if !ok {
		return nil, nil, nil, nil, ErrTemplateNotFound
	}
	if !isNewer(m.Metadata.Version, rec.Version) {
		return nil, nil, nil, nil, ErrAlreadyLatest
	}
	// The installed version's manifest powers the diff; tolerate its absence
	// (an old version pruned from the catalog) by diffing against an empty one.
	old, _, _ := s.resolveManifest(workspaceID, rec.TemplateName, rec.Version)

	// Apps were created in manifest order at install, so AppIDs line up with the
	// installed manifest's Applications.
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
	return rec, m, old, byName, nil
}

// PlanUpgrade computes the diff between the installed version and target without
// changing anything.
func (s *Service) PlanUpgrade(workspaceID, installID uint, target string) (*UpgradePlan, error) {
	rec, newM, oldM, byName, err := s.resolveUpgrade(workspaceID, installID, target)
	if err != nil {
		return nil, err
	}
	plan := &UpgradePlan{
		FromVersion:  rec.Version,
		ToVersion:    newM.Metadata.Version,
		Apps:         []AppChange{},
		StackEnv:     []EnvChange{},
		NewVolumes:   []string{},
		NewDatabases: []string{},
		AddedApps:    []string{},
		RemovedApps:  []string{},
		NewInputs:    []manifest.Input{},
		Warnings:     []string{},
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
	plan.NewDatabases = added(dbNames(oldM), dbNames(newM))
	for _, d := range plan.NewDatabases {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("database dependency %q is new in this version — provision it manually (not added automatically)", d))
	}
	plan.NewInputs = newInputs(oldM, newM, rec.Inputs)
	return plan, nil
}

// ApplyUpgrade converges an install to the target version: it bumps each matched app's image, adds new
// env, creates new volumes and attaches their mounts, redeploys, and records the new version. Changed or
// removed env, new databases and structural app changes are surfaced as warnings, not applied.
func (s *Service) ApplyUpgrade(ctx context.Context, workspaceID, installID uint, target string, newInputs map[string]string) (*UpgradeApplyResult, error) {
	rec, newM, oldM, byName, err := s.resolveUpgrade(workspaceID, installID, target)
	if err != nil {
		return nil, err
	}
	res := &UpgradeApplyResult{
		FromVersion: rec.Version,
		ToVersion:   newM.Metadata.Version,
		AppsBumped:  []string{},
		EnvApplied:  []string{},
		NewVolumes:  []string{},
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

	// 1. New volumes (additive). Mounts attach below.
	volIDs := map[string]uint{}
	for _, v := range added(volNames(oldM), volNames(newM)) {
		meta := models.SetBuiltin(models.Metadata{},
			models.MetaManagedBy, models.ManagedByMarketplace,
			models.MetaTemplate, newM.Metadata.Name,
			models.MetaTemplateVersion, newM.Metadata.Version)
		vol, verr := s.volumes.Create(ctx, workspaceID, 0, sanitizeName(newM.Metadata.Name+"-"+v), 0, meta, nil)
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

	// 2. App-by-app: image bump + additive env + new mounts, then redeploy.
	//    App aliases feed env interpolation that references siblings.
	appViews := map[string]manifest.AppView{}
	for name, appID := range byName {
		if app, gerr := s.apps.Get(workspaceID, appID); gerr == nil {
			appViews[name] = manifest.AppView{Alias: appAlias(app)}
		}
	}
	renderer := manifest.NewRenderer(manifest.Context{Inputs: inputs, Applications: appViews})

	// Tracks apps already redeployed this upgrade, so a later shared-env change
	// doesn't redeploy the same app twice.
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
		if uerr := s.apps.Update(app); uerr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("update %q: %v", name, uerr))
		} else if imageChanged {
			changed = true
		}

		// Additive env only (never overwrite an existing key or regenerate a secret).
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
			if isMountNew(os, mt.Volume) {
				if vid, ok := volIDs[mt.Volume]; ok {
					if aerr := s.apps.AttachVolume(app, vid, mt.Path); aerr != nil {
						res.Warnings = append(res.Warnings, fmt.Sprintf("mount %s on %q: %v", mt.Volume, name, aerr))
					} else {
						changed = true
					}
				} else {
					res.Warnings = append(res.Warnings, fmt.Sprintf("new mount %s on %q references a volume not created by this upgrade — attach manually", mt.Volume, name))
				}
			}
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

	// 2b. Shared stack env (additive, mirrors per-app env): add new shared keys to
	//     the install's stack and redeploy any member not already bumped, since
	//     shared env only reaches containers at deploy time.
	s.applyStackEnv(workspaceID, rec, oldM, newM, byName, renderer, deployed, res)

	// 3. Record the new version + merged inputs.
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

// applyStackEnv reconciles the install's shared stack env toward the target version, additively and
// conservatively like the per-app path: new keys are rendered and set, changed or removed ones are warnings.
// Shared env only reaches containers at deploy time, so members not already redeployed are redeployed.
func (s *Service) applyStackEnv(workspaceID uint, rec *models.TemplateInstall, oldM, newM *manifest.Manifest, byName map[string]uint, renderer *manifest.Renderer, deployed map[uint]bool, res *UpgradeApplyResult) {
	oldEnv, newEnv := stackEnvOf(oldM), stackEnvOf(newM)
	if len(oldEnv) == 0 && len(newEnv) == 0 {
		return
	}
	// The new version groups config into a stack the install doesn't have: a
	// structural change we don't perform automatically (mirrors added apps).
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
		if serr := s.stacks.SetEnvVar(workspaceID, *rec.StackID, key, rendered, secret[key]); serr != nil {
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

// stackEnvOf / stackSecretEnv read a manifest's shared stack env, tolerating a
// missing stack block (returns nil).
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
		had[m.Volume] = true
	}
	out := []string{} // non-nil so JSON is [] not null (the UI reads .length)
	for _, m := range newA.Mounts {
		if !had[m.Volume] {
			out = append(out, m.Volume)
		}
	}
	return out
}

func isMountNew(oldA manifest.AppSpec, volume string) bool {
	for _, m := range oldA.Mounts {
		if m.Volume == volume {
			return false
		}
	}
	return true
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

// renderEnvValue interpolates a single env value, returning an error if it
// references context that can't be resolved (e.g. a database connection).
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
