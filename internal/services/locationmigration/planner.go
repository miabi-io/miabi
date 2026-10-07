// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package locationmigration

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/placement"
)

// DBChoice overrides the default strategy for one database instance.
type DBChoice struct {
	InstanceID       uint   `json:"instance_id"`
	Strategy         string `json:"strategy"`
	TargetInstanceID uint   `json:"target_instance_id,omitempty"`
}

// PlanRequest is what a person asks for: a location, and optionally a strategy per database.
type PlanRequest struct {
	Location  string
	Databases []DBChoice
	Admin     bool
}

// Plan is the dry run: what would move, how, and what stops it. Nothing is created.
func (s *Service) Plan(ctx context.Context, workspaceID, appID uint, req PlanRequest) (*models.MigrationPlan, error) {
	plan, _, err := s.plan(ctx, workspaceID, appID, req)
	return plan, err
}

func (s *Service) plan(ctx context.Context, workspaceID, appID uint, req PlanRequest) (*models.MigrationPlan, *models.Application, error) {
	app, err := s.Apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	location := strings.TrimSpace(req.Location)
	if location == "" {
		return nil, nil, ErrLocationRequired
	}
	c, err := s.Placer.ResolveLocation(workspaceID, location, req.Admin)
	if err != nil {
		return nil, nil, err
	}
	if c.ID == s.norm(app.ClusterID) {
		return nil, nil, ErrSameLocation
	}

	p := &planner{s: s, ctx: ctx, app: app, plan: &models.MigrationPlan{
		Location:      c.Name,
		LocationLabel: firstNonEmpty(c.DisplayName, c.Name),
		Volumes:       []models.MigrationVolume{},
		Databases:     []models.MigrationDBItem{},
		Blockers:      []models.MigrationIssue{},
		Warnings:      []models.MigrationIssue{},
	}}
	if err := p.load(); err != nil {
		return nil, nil, err
	}
	// A service app whose data is node-local runs where that data is, so its target is chosen like a
	// container's: a node, not the manager.
	service := app.RuntimeKind == models.RuntimeService && !p.hasLocalVolumes()
	at, err := s.Placer.Place(placement.Request{WorkspaceID: workspaceID, Location: c.Name, Admin: req.Admin, Service: service})
	if err != nil {
		return nil, nil, err
	}
	p.plan.TargetClusterID, p.plan.TargetServerID, p.plan.Service = at.ClusterID, at.ServerID, service

	p.checkApp()
	p.checkVolumes()
	p.checkDatabases(req.Databases)
	p.checkRouting()
	return p.plan, app, nil
}

type planner struct {
	s    *Service
	ctx  context.Context
	app  *models.Application
	plan *models.MigrationPlan

	peers     []models.Application // the workspace's other apps, with their env
	instances []models.DatabaseInstance
}

func (p *planner) block(code, resource, format string, args ...any) {
	p.plan.Blockers = append(p.plan.Blockers, models.MigrationIssue{Code: code, Resource: resource, Message: fmt.Sprintf(format, args...)})
}

func (p *planner) warn(code, resource, format string, args ...any) {
	p.plan.Warnings = append(p.plan.Warnings, models.MigrationIssue{Code: code, Resource: resource, Message: fmt.Sprintf(format, args...)})
}

func (p *planner) load() error {
	apps, err := p.s.Apps.ListByWorkspaceWithEnv(p.app.WorkspaceID)
	if err != nil {
		return err
	}
	for i := range apps {
		if apps[i].ID == p.app.ID {
			p.app.EnvVars = apps[i].EnvVars
			continue
		}
		p.peers = append(p.peers, apps[i])
	}
	p.instances, err = p.s.Databases.ListByWorkspaceRaw(p.app.WorkspaceID)
	return err
}

func (p *planner) hasLocalVolumes() bool {
	for _, m := range p.app.Mounts {
		if v := p.volume(m); v != nil && v.AccessMode != models.AccessRWX {
			return true
		}
	}
	return false
}

func (p *planner) volume(m models.AppMount) *models.Volume {
	if m.VolumeID == 0 || m.ConfigID != 0 {
		return nil
	}
	v, err := p.s.VolumeRepo.FindInWorkspace(p.app.WorkspaceID, m.VolumeID)
	if err != nil {
		return nil
	}
	return v
}

func (p *planner) checkApp() {
	app := p.app
	if app.CurrentReleaseID == nil {
		p.block("not_deployed", app.Name, "%s has never been deployed. Deploy it first; there is nothing running to move", app.Name)
	}
	if app.Status == models.AppStatusDeploying {
		p.block("deploying", app.Name, "a deployment of %s is in progress; wait for it to finish", app.Name)
	}
	if app.CanaryReleaseID != nil {
		p.block("canary", app.Name, "%s has a canary rollout in progress; promote or abort it first", app.Name)
	}
	if app.StackID != nil {
		for i := range p.peers {
			if p.peers[i].StackID != nil && *p.peers[i].StackID == *app.StackID {
				p.block("stack_members", app.Name, "%s is in a stack with other applications, and a stack lives in one location. Move it out of the stack first", app.Name)
				break
			}
		}
	}
	if m, err := p.s.Repo.ActiveForApp(app.ID); err == nil && m != nil {
		p.block("migration_active", app.Name, "migration #%d of this application is still open; finish, finalize or roll it back first", m.ID)
	}
	if app.GPUCount > 0 {
		p.warn("gpu", app.Name, "the app requests %d GPU(s); its deploy at the target fails unless a node there has them free", app.GPUCount)
	}
	if app.Status == models.AppStatusStopped {
		p.warn("stopped", app.Name, "the app is stopped; it will be started at the target")
	}
	if owner, ok := models.SourceOwnedElsewhere(app.Metadata); ok && owner == models.ManagedByGitOps {
		p.warn("gitops", app.Name, "managed by GitOps: if its manifest sets placement.location, change it to %q, or the next sync will refuse the app", p.plan.Location)
	}
	alias := node.AppAlias(app)
	for i := range p.peers {
		if envMentions(p.peers[i].EnvVars, alias) {
			p.warn("private_reach", p.peers[i].Name, "%s reaches this app by its private name; locations do not route to each other, so it will lose it", p.peers[i].Name)
		}
	}
}

func (p *planner) checkVolumes() {
	for _, m := range p.app.Mounts {
		if m.ConfigID != 0 {
			continue
		}
		if m.HostPath != "" || m.HostPreset != "" {
			p.warn("host_bind", m.Path, "the host bind at %s is not copied; the target node must provide the same path", m.Path)
			continue
		}
		v := p.volume(m)
		if v == nil {
			continue
		}
		item := models.MigrationVolume{VolumeID: v.ID, Name: v.Name, Driver: v.Driver, UsedBytes: v.UsedBytes, StorageClass: v.StorageClassName}
		if users, err := p.s.Volumes.VolumeConsumers(p.app.WorkspaceID, v.ID); err == nil {
			var others []string
			for _, u := range users {
				if u.AppID != p.app.ID {
					others = append(others, u.AppName)
				}
			}
			if len(others) > 0 {
				p.block("volume_shared", v.Name, "volume %s is also mounted by %s; detach it there before moving this app", v.Name, strings.Join(others, ", "))
			}
		}
		switch v.Driver {
		case models.VolumeDriverHost:
			p.block("volume_host", v.Name, "volume %s is a host path (%s) on its node; its data cannot be moved", v.Name, v.HostPath)
			item.Action = models.VolumeActionRedeclare
		case models.VolumeDriverNFS, models.VolumeDriverCIFS:
			item.Action = models.VolumeActionRedeclare
			p.warn("shared_storage", v.Name, "volume %s is on shared storage: its data stays on the share, and the target nodes must be able to mount it", v.Name)
		default:
			item.Action = models.VolumeActionCopy
			p.plan.CopyBytes += v.UsedBytes
			if !p.s.Volumes.ClassAvailable(p.plan.TargetServerID, v.StorageClassName) {
				item.StorageClass = models.DefaultStorageClassName
				p.warn("storage_class", v.Name, "storage class %s does not exist at the target; volume %s lands on the node's default storage", v.StorageClassName, v.Name)
			}
		}
		p.plan.Volumes = append(p.plan.Volumes, item)
	}
}

// dependency is an instance the app uses, and how.
type dependency struct {
	inst *models.DatabaseInstance
	dbs  []models.Database // the app's logical databases on it
}

func (p *planner) dependencies() []dependency {
	var deps []dependency
	for i := range p.instances {
		inst := &p.instances[i]
		dbs, _ := p.s.Databases.ListInstanceDatabases(inst.ID)
		var mine []models.Database
		for _, d := range dbs {
			if d.ApplicationID != nil && *d.ApplicationID == p.app.ID {
				mine = append(mine, d)
			}
		}
		owner, owned := models.Owner(inst.Metadata)
		ownedByApp := owned && owner.Kind == models.OwnerApp && owner.ID == p.app.ID
		linked, _ := linkedBy(p.instanceLinks(inst.ID), p.app.ID)
		if len(mine) > 0 || ownedByApp || linked || (inst.Host != "" && envMentions(p.app.EnvVars, inst.Host)) {
			deps = append(deps, dependency{inst: inst, dbs: mine})
		}
	}
	return deps
}

// exclusive reports whether nothing but the app uses an instance: every logical database on it is the app's,
// no other app is its owner or links it, and no other app's environment names it.
func (p *planner) exclusive(inst *models.DatabaseInstance) bool {
	dbs, _ := p.s.Databases.ListInstanceDatabases(inst.ID)
	for _, d := range dbs {
		if d.ApplicationID == nil || *d.ApplicationID != p.app.ID {
			return false
		}
	}
	if owner, ok := models.Owner(inst.Metadata); ok && owner.Kind == models.OwnerApp && owner.ID != p.app.ID && owner.ID != 0 {
		return false
	}
	if _, others := linkedBy(p.instanceLinks(inst.ID), p.app.ID); others {
		return false
	}
	for i := range p.peers {
		if inst.Host != "" && envMentions(p.peers[i].EnvVars, inst.Host) {
			return false
		}
	}
	return true
}

// instanceLinks lists the apps linked to an instance as a whole (Redis). A link's injected host can be
// skipped by its env mapping, so the env scan alone would miss it.
func (p *planner) instanceLinks(instanceID uint) []models.DatabaseInstanceLink {
	links, _ := p.s.Databases.ListInstanceLinks(instanceID)
	return links
}

// linkedBy reports whether appID links the instance, and whether any other app does.
func linkedBy(links []models.DatabaseInstanceLink, appID uint) (mine, others bool) {
	for _, l := range links {
		if l.ApplicationID == appID {
			mine = true
		} else {
			others = true
		}
	}
	return mine, others
}

func (p *planner) checkDatabases(choices []DBChoice) {
	sameArch := p.sameArch()
	for _, dep := range p.dependencies() {
		inst := dep.inst
		item := models.MigrationDBItem{
			InstanceID: inst.ID, InstanceName: inst.Name, Engine: inst.Engine, Version: inst.Version,
			Exclusive: p.exclusive(inst), Databases: []models.MigrationLogicalDB{},
		}
		for _, d := range dep.dbs {
			item.Databases = append(item.Databases, models.MigrationLogicalDB{ID: d.ID, Name: d.Name, EnvPrefix: d.EnvPrefix})
		}
		item.Strategies = strategies(inst.Engine, item.Exclusive, sameArch, len(dep.dbs) > 0)
		if len(item.Strategies) == 0 {
			switch {
			case !item.Exclusive:
				p.block("db_shared_unmovable", inst.Name, "%s (%s) is used by other applications and holds no database of this app's to restore elsewhere. Give this app its own instance first", inst.Name, inst.Engine)
			default:
				p.block("db_arch", inst.Name, "%s (%s) can only move whole, and the target node runs another CPU architecture", inst.Name, inst.Engine)
			}
			p.plan.Databases = append(p.plan.Databases, item)
			continue
		}
		item.Strategy = item.Strategies[0]
		if choice, ok := findChoice(choices, inst.ID); ok && choice.Strategy != "" {
			if !slices.Contains(item.Strategies, choice.Strategy) {
				p.block("db_strategy", inst.Name, "%q is not possible for %s; choose one of %s", choice.Strategy, inst.Name, strings.Join(item.Strategies, ", "))
			} else {
				item.Strategy = choice.Strategy
				item.TargetInstanceID = choice.TargetInstanceID
			}
		}
		if item.Strategy == models.DBStrategyExistingInstance {
			p.checkExistingTarget(inst, &item, dep.dbs)
		}
		if !sameArch && item.Exclusive && inst.SupportsLogicalDatabases() && inst.Engine != models.DBEngineLibSQL {
			p.warn("db_arch", inst.Name, "the target node runs another CPU architecture, so %s is restored from a dump rather than moved whole", inst.Name)
		}
		if item.Strategy != models.DBStrategyMove {
			p.warn("db_downtime", inst.Name, "%s is dumped and restored while the app is stopped: the downtime grows with its size", inst.Name)
		}
		p.plan.Databases = append(p.plan.Databases, item)
	}
}

// strategies lists what an instance allows, the default first. A whole move needs the instance to be the
// app's alone and the two nodes to share an on-disk format. A restore needs logical databases of the app's
// to dump, which Redis and libSQL do not offer here.
func strategies(engine models.DBEngine, exclusive, sameArch, hasDatabases bool) []string {
	logical := hasDatabases && engine != models.DBEngineRedis && engine != models.DBEngineLibSQL
	var out []string
	if exclusive && sameArch {
		out = append(out, models.DBStrategyMove)
	}
	if logical {
		out = append(out, models.DBStrategyNewInstance, models.DBStrategyExistingInstance)
	}
	return out
}

func findChoice(choices []DBChoice, instanceID uint) (DBChoice, bool) {
	for _, c := range choices {
		if c.InstanceID == instanceID {
			return c, true
		}
	}
	return DBChoice{}, false
}

func (p *planner) checkExistingTarget(src *models.DatabaseInstance, item *models.MigrationDBItem, dbs []models.Database) {
	if item.TargetInstanceID == 0 {
		p.block("db_target_required", src.Name, "choose the instance at the target to restore %s into", src.Name)
		return
	}
	var dst *models.DatabaseInstance
	for i := range p.instances {
		if p.instances[i].ID == item.TargetInstanceID {
			dst = &p.instances[i]
		}
	}
	if dst == nil {
		p.block("db_target_missing", src.Name, "the chosen target instance does not exist in this workspace")
		return
	}
	if p.s.norm(dst.ClusterID) != p.plan.TargetClusterID {
		p.block("db_target_location", dst.Name, "%s is not in %s", dst.Name, p.plan.LocationLabel)
	}
	if dst.Status != models.DBStatusRunning {
		p.block("db_target_stopped", dst.Name, "%s is not running", dst.Name)
	}
	if err := database.CanRestoreInto(src, dst); err != nil {
		p.block("db_target_version", dst.Name, "%s cannot take a dump of %s: %v", dst.Name, src.Name, err)
	}
	taken, _ := p.s.Databases.ListInstanceDatabases(dst.ID)
	for _, d := range dbs {
		for _, t := range taken {
			if t.Name == d.Name {
				p.block("db_name_taken", dst.Name, "%s already has a database named %s", dst.Name, d.Name)
			}
		}
	}
}

// sameArch compares the two nodes' CPU architectures. Unknown counts as different: a whole move onto a
// node whose format cannot be confirmed is the one mistake that corrupts data.
func (p *planner) sameArch() bool {
	src, err := p.s.Clients.For(p.app.ServerID)
	if err != nil {
		return false
	}
	dst, err := p.s.Clients.For(p.plan.TargetServerID)
	if err != nil {
		p.block("target_offline", p.plan.Location, "the target node is not reachable: %v", err)
		return false
	}
	a, aerr := src.Capabilities(p.ctx)
	b, berr := dst.Capabilities(p.ctx)
	return aerr == nil && berr == nil && a.Arch != "" && a.Arch == b.Arch
}

func (p *planner) checkRouting() {
	p.plan.GeneratedURLs = p.s.Routes.GeneratedURLs(p.app.WorkspaceID, p.app.ID)
	if len(p.plan.GeneratedURLs) > 0 {
		p.warn("generated_urls", p.app.Name, "generated URLs are per location, so %s will change", strings.Join(p.plan.GeneratedURLs, ", "))
	}
	p.plan.CustomDomains = p.s.Routes.CustomHosts(p.app.ID)
}

// envMentions reports whether any plain env value names the host. Secret values are ciphertext and skipped:
// a match there cannot be seen, which errs toward treating an instance as the app's alone.
func envMentions(env []models.AppEnvVar, host string) bool {
	if host == "" {
		return false
	}
	for _, e := range env {
		if !e.IsSecret && strings.Contains(e.Value, host) {
			return true
		}
	}
	return false
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
