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

// Package apply converges a workspace to miabi.io/v1 manifests; GitOps reuses the same engine.
package apply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/mwcatalog"
	"github.com/miabi-io/miabi/internal/services/application"
	certificatesvc "github.com/miabi-io/miabi/internal/services/certificate"
	configsvc "github.com/miabi-io/miabi/internal/services/config"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/services/domain"
	"github.com/miabi-io/miabi/internal/services/gitrepo"
	"github.com/miabi-io/miabi/internal/services/job"
	middlewaresvc "github.com/miabi-io/miabi/internal/services/middleware"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/placement"
	"github.com/miabi-io/miabi/internal/services/portbinding"
	"github.com/miabi-io/miabi/internal/services/registry"
	"github.com/miabi-io/miabi/internal/services/route"
	"github.com/miabi-io/miabi/internal/services/secret"
	"github.com/miabi-io/miabi/internal/services/stack"
	"github.com/miabi-io/miabi/internal/services/storage"
	"github.com/miabi-io/miabi/pkg/sealed"
)

// MetaDigest pins image@digest; the snapshot reports the running digest, so a failed pin shows as drift.
const MetaDigest = models.MetaDigest

// ManagedByGitOps marks resources the declarative engine reconciles for a GitOps project.
const ManagedByGitOps = models.ManagedByGitOps

// Each run prunes only its own owner's resources, so a CLI prune never deletes a GitOps project's.
const ManagedByApply = models.ManagedByApply

var (
	// ErrInvalidManifest signals a parse/validation failure (HTTP 400).
	ErrInvalidManifest = errors.New("invalid manifest")
	// ErrOwnedByGitOps refuses a one-shot apply that would change a GitOps project's resource (HTTP 409).
	ErrOwnedByGitOps = errors.New("owned by a GitOps project")
	// ErrPlainSecret refuses a Secret with a plaintext value where only sealed values are allowed.
	ErrPlainSecret = errors.New("this source only accepts sealed secrets, but these Secrets carry a plaintext value")
	// The plan still lists the change.
	ErrUnsupportedKind  = errors.New("kind not yet supported by apply")
	ErrResourceNotFound = errors.New("resource not found in manifests")
)

// Service computes and executes declarative plans for a workspace.
type Service struct {
	apps       *application.Service
	storage    *storage.Service
	dbs        *database.Service
	stacks     *stack.Service
	secrets    *secret.Service
	routes     *route.Service
	domains    *domain.Service
	registries *registry.Service

	extAccess bool
	bindings  *portbinding.Service

	// nil makes a manifest declaring one error rather than silently skip what an app mounts.
	configs *configsvc.Service
	// Locations never share a network, and a location's nodes share one only in a swarm; nil means not clustered.
	cluster ClusterCap
	placer  Placer
	// Certificates carry a private key so they are not declarable; nil leaves tls: custom unresolvable.
	certificates *certificatesvc.Service
	// nil makes a declared middleware fail loudly instead of silently vanishing from its routes.
	middlewares *middlewaresvc.Service
	// jobs converges kinds CronJob and Job.
	jobs *job.Service
	// unsealer opens a SealedSecret's value; nil makes one fail rather than store nothing.
	unsealer Unsealer
	// gitSources converges kind: GitSource.
	gitSources GitSources
	// gitRepos converges kind: GitRepository.
	gitRepos *gitrepo.Service
}

// Unsealer opens sealed secret values with the workspace's sealing keys. Satisfied by the sealing service.
type Unsealer interface {
	Unseal(workspaceID uint, name, value string) (plain string, keyVersion int, err error)
}

// SetUnsealer wires kind: SealedSecret.
func (s *Service) SetUnsealer(u Unsealer) { s.unsealer = u }

// SetConfigs wires the config service (kind: Config).
func (s *Service) SetConfigs(c *configsvc.Service) { s.configs = c }

type ClusterCap interface {
	IsSwarm(clusterID uint) bool
	ClusterOfServer(serverID uint) uint
	LocationLabel(clusterID uint) string
}

func (s *Service) SetCluster(c ClusterCap) { s.cluster = c }

// Placer resolves where a created resource lands. Satisfied by the placement service.
type Placer interface {
	Place(req placement.Request) (placement.Result, error)
	LocationName(clusterID uint) string
	ResolveLocation(workspaceID uint, location string, admin bool) (*models.Cluster, error)
}

// SetPlacer wires spec.location. Without it every resource is created on the local node.
func (s *Service) SetPlacer(p Placer) { s.placer = p }

func (s *Service) place(ctx context.Context, workspaceID uint, ch declarative.Change, location string, service bool) (placement.Result, error) {
	if s.placer == nil {
		return placement.Result{}, nil
	}
	res, err := s.placer.Place(placement.Request{WorkspaceID: workspaceID, Location: location, Service: service, Admin: isAdmin(ctx)})
	if err != nil {
		return res, fmt.Errorf("%w: %s %q: %v", ErrInvalidManifest, strings.ToLower(string(ch.Kind)), ch.Name, err)
	}
	return res, nil
}

// With no location, stack or volume, the app follows its referenced databases when they share a location.
func (s *Service) placeApp(ctx context.Context, workspaceID uint, name string, spec *declarative.ApplicationSpec, refs specRefs) (placement.Result, error) {
	if s.placer == nil {
		return placement.Result{}, nil
	}
	req := placement.Request{
		WorkspaceID: workspaceID, Location: spec.Location(), Admin: isAdmin(ctx),
		Service: spec.Runtime() == string(models.RuntimeService),
	}
	if spec.Stack != "" {
		if st, err := s.findStack(workspaceID, spec.Stack); err == nil {
			stackLocation := s.placer.LocationName(st.ClusterID)
			switch {
			case req.Location == "":
				req.Location = stackLocation
			case stackLocation != "" && stackLocation != req.Location:
				return placement.Result{}, stackLocationError(name, req.Location, spec.Stack, stackLocation)
			}
		}
	}
	var mounted string
	for _, m := range spec.Mounts {
		if m.Volume == "" {
			continue
		}
		if v, err := s.findVolume(workspaceID, m.Volume); err == nil && v.Driver != models.VolumeDriverHost && v.ServerID != 0 {
			req.Colocate, mounted = v.ServerID, m.Volume
			break
		}
	}
	if req.Location == "" && mounted == "" && len(refs.dbs) > 0 {
		if cluster := sharedCluster(refs.dbs, s.databaseNodes(workspaceID)); cluster != 0 {
			req.Location = s.placer.LocationName(cluster)
		}
	}
	res, err := s.placer.Place(req)
	switch {
	case errors.Is(err, placement.ErrLocationMismatch):
		return res, fmt.Errorf("%w: application %q mounts volume %q, which is in another location. Declare the same location on both",
			ErrInvalidManifest, name, mounted)
	case err != nil:
		return res, fmt.Errorf("%w: application %q: %v", ErrInvalidManifest, name, err)
	}
	return res, nil
}

// Node-local data is reachable from one node; placed alone, the volume would leave its app half-applied.
func (s *Service) placeVolume(ctx context.Context, workspaceID uint, ch declarative.Change, spec *declarative.VolumeSpec, set *declarative.ResourceSet) (placement.Result, error) {
	location := spec.Location()
	if s.placer == nil || set == nil {
		return s.place(ctx, workspaceID, ch, location, false)
	}
	app, appName := mountingApp(set, ch.Name)
	if app == nil {
		return s.place(ctx, workspaceID, ch, location, false)
	}
	appLocation := s.appLocation(workspaceID, app, set)
	switch {
	case location == "":
		location = appLocation
	case appLocation != "" && appLocation != location:
		return placement.Result{}, volumeLocationError(appName, appLocation, ch.Name, location)
	}
	req := placement.Request{WorkspaceID: workspaceID, Location: location, Admin: isAdmin(ctx)}
	if existing, err := s.findApp(workspaceID, appName); err == nil {
		if existing.RuntimeKind != models.RuntimeService {
			req.Colocate = existing.ServerID
		}
		if req.Location == "" {
			req.Location = s.placer.LocationName(existing.ClusterID)
		}
	}
	if req.Colocate == 0 {
		for _, m := range app.Mounts {
			if m.Volume == "" || m.Volume == ch.Name {
				continue
			}
			if v, err := s.findVolume(workspaceID, m.Volume); err == nil && v.Driver != models.VolumeDriverHost && v.AccessMode != models.AccessRWX {
				req.Colocate = v.ServerID
				break
			}
		}
	}
	res, err := s.placer.Place(req)
	switch {
	case errors.Is(err, placement.ErrLocationMismatch):
		return res, volumeLocationError(appName, s.placer.LocationName(res.ClusterID), ch.Name, location)
	case err != nil:
		return res, fmt.Errorf("%w: volume %q: %v", ErrInvalidManifest, ch.Name, err)
	}
	return res, nil
}

// mountingApp is the first application in the bundle that mounts the named volume.
func mountingApp(set *declarative.ResourceSet, volume string) (*declarative.ApplicationSpec, string) {
	for _, r := range set.ByKind(declarative.KindApplication) {
		if r.Application == nil {
			continue
		}
		for _, m := range r.Application.Mounts {
			if m.Volume == volume {
				return r.Application, r.Metadata.Name
			}
		}
	}
	return nil, ""
}

// Empty when neither the app nor its stack names a location.
func (s *Service) appLocation(workspaceID uint, app *declarative.ApplicationSpec, set *declarative.ResourceSet) string {
	if l := app.Location(); l != "" || app.Stack == "" {
		return l
	}
	if st, ok := set.Get(string(declarative.KindStack) + "/" + app.Stack); ok && st.Stack.Location() != "" {
		return st.Stack.Location()
	}
	if st, err := s.findStack(workspaceID, app.Stack); err == nil {
		return s.placer.LocationName(st.ClusterID)
	}
	return ""
}

func volumeLocationError(app, appLocation, volume, volumeLocation string) error {
	return fmt.Errorf("%w: application %q runs in %q but mounts volume %q in %q, and an app can only mount a volume in its own location. "+
		"Declare the same location on both", ErrInvalidManifest, app, appLocation, volume, volumeLocation)
}

// 0 when the resources span several clusters or none is known.
func sharedCluster(refs map[string]bool, nodes map[string]appPlacement) uint {
	var cluster uint
	for ref := range refs {
		p, ok := nodes[ref]
		if !ok {
			continue
		}
		if cluster != 0 && p.cluster != cluster {
			return 0
		}
		cluster = p.cluster
	}
	return cluster
}

func stackLocationError(app, location, stack, stackLocation string) error {
	return fmt.Errorf("%w: application %q is declared in %q, but its stack %q is in %q, and an app runs in its stack's location. "+
		"Set location to %q or leave it out", ErrInvalidManifest, app, location, stack, stackLocation, stackLocation)
}

// Existing resources are not judged: a location cordoned since is not an error.
func (s *Service) checkLocations(workspaceID uint, plan *declarative.Plan, desired *declarative.ResourceSet, admin bool) error {
	if s.placer == nil {
		return nil
	}
	for _, ch := range plan.Changes {
		// Refused here so the rest of the bundle doesn't apply around a change that can't happen.
		if ch.Action == declarative.ActionUpdate {
			if err := refuseImmutable(ch); err != nil {
				return err
			}
			continue
		}
		if ch.Action != declarative.ActionCreate {
			continue
		}
		r, ok := desired.Get(string(ch.Kind) + "/" + ch.Name)
		if !ok {
			continue
		}
		location := locationOf(r)
		if location == "" {
			continue
		}
		c, err := s.placer.ResolveLocation(workspaceID, location, admin)
		if err != nil {
			return fmt.Errorf("%w: %s %q: location %q: %v", ErrInvalidManifest, strings.ToLower(string(ch.Kind)), ch.Name, location, err)
		}
		a := r.Application
		if a == nil {
			continue
		}
		if a.Runtime() == string(models.RuntimeService) && s.cluster != nil && !s.cluster.IsSwarm(c.ID) {
			return fmt.Errorf("%w: application %q runs as a service, but location %q runs no swarm. "+
				"Use runtime: container or a location that runs a swarm", ErrInvalidManifest, ch.Name, location)
		}
		if st, ok := desired.Get(string(declarative.KindStack) + "/" + a.Stack); ok && a.Stack != "" &&
			st.Stack.Location() != "" && st.Stack.Location() != location {
			return stackLocationError(ch.Name, location, a.Stack, st.Stack.Location())
		}
	}
	return s.checkMountLocations(workspaceID, desired)
}

// Found at apply time, the volume and earlier resources would already exist.
func (s *Service) checkMountLocations(workspaceID uint, desired *declarative.ResourceSet) error {
	for _, r := range desired.ByKind(declarative.KindApplication) {
		a := r.Application
		if a == nil {
			continue
		}
		existing, _ := s.findApp(workspaceID, r.Metadata.Name)
		appLocation := s.appLocation(workspaceID, a, desired)
		if appLocation == "" && existing != nil {
			appLocation = s.placer.LocationName(existing.ClusterID)
		}
		runtime, replicas := a.Runtime(), a.Replicas()
		if existing != nil {
			if runtime == "" {
				runtime = string(existing.RuntimeKind)
			}
			if replicas == 0 {
				replicas = existing.Replicas
			}
		}
		replicated := runtime == string(models.RuntimeService) && replicas > 1
		for _, m := range a.Mounts {
			if m.Volume == "" {
				continue
			}
			volLocation, nodeLocal := "", false
			if vr, ok := desired.Get(string(declarative.KindVolume) + "/" + m.Volume); ok {
				// A volume created from a manifest is always node-local.
				volLocation, nodeLocal = vr.Volume.Location(), true
			}
			if v, err := s.findVolume(workspaceID, m.Volume); err == nil {
				if v.Driver == models.VolumeDriverHost {
					continue
				}
				volLocation, nodeLocal = s.placer.LocationName(v.ClusterID), v.AccessMode != models.AccessRWX
			}
			if appLocation != "" && volLocation != "" && volLocation != appLocation {
				return volumeLocationError(r.Metadata.Name, appLocation, m.Volume, volLocation)
			}
			if replicated && nodeLocal {
				return fmt.Errorf("%w: application %q runs %d replicas but mounts node-local volume %q, which exists on one node only. "+
					"Run one replica, or mount shared storage", ErrInvalidManifest, r.Metadata.Name, replicas, m.Volume)
			}
		}
	}
	return nil
}

func locationOf(r declarative.Resource) string {
	switch {
	case r.Application != nil:
		return r.Application.Location()
	case r.Stack != nil:
		return r.Stack.Location()
	case r.Database != nil:
		return r.Database.Location()
	case r.Volume != nil:
		return r.Volume.Location()
	}
	return ""
}

// defaultLocation is where a create that names no location lands, so an export can leave it unsaid.
func (s *Service) defaultLocation(workspaceID uint) string {
	if s.placer == nil {
		return ""
	}
	c, err := s.placer.ResolveLocation(workspaceID, "", false)
	if err != nil {
		return ""
	}
	return c.Name
}

func (s *Service) locationName(clusterID uint) string {
	if s.placer == nil {
		return ""
	}
	return s.placer.LocationName(clusterID)
}

// Nothing is rescheduled after create.
func refuseMove(ch declarative.Change) error {
	for _, f := range ch.Fields {
		if f.Field == "placement.location" {
			hint := ""
			if ch.Kind == declarative.KindApplication {
				hint = " An application can be moved with its data by a location migration (Enterprise); update the manifest after it."
			}
			return fmt.Errorf("%w: %s %q is in %q; moving it to %q is not supported. Delete it and apply again.%s",
				ErrInvalidManifest, strings.ToLower(string(ch.Kind)), ch.Name, f.From, f.To, hint)
		}
	}
	return nil
}

// A volume's storage class is where its bytes live; converging it would silently recreate the volume.
func refuseImmutable(ch declarative.Change) error {
	if err := refuseMove(ch); err != nil {
		return err
	}
	if err := refuseRunMove(ch); err != nil {
		return err
	}
	for _, f := range ch.Fields {
		if f.Field == "storage.class" {
			return fmt.Errorf("%w: %s %q is on storage class %q; moving it to %q would relocate its data. Delete it and apply again, or migrate it",
				ErrInvalidManifest, strings.ToLower(string(ch.Kind)), ch.Name, f.From, f.To)
		}
	}
	return nil
}

// Size is a soft quota number; shrinking is refused so a manifest can't undo a console expansion.
func (s *Service) resizeVolume(workspaceID uint, ch declarative.Change) error {
	for _, f := range ch.Fields {
		if f.Field != "size" {
			continue
		}
		size, err := strconv.ParseInt(f.To, 10, 64)
		if err != nil {
			return fmt.Errorf("%w: volume %q: invalid size %q", ErrInvalidManifest, ch.Name, f.To)
		}
		v, err := s.findVolume(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		if err := s.storage.Resize(workspaceID, v.ID, size); err != nil {
			if errors.Is(err, storage.ErrVolumeShrink) || errors.Is(err, storage.ErrVolumeBelowUsage) {
				return fmt.Errorf("%w: volume %q: %s", ErrInvalidManifest, ch.Name, err)
			}
			return err
		}
		return nil
	}
	return nil
}

func (s *Service) clusterID(id uint) uint {
	if id == models.DefaultClusterID && s.cluster != nil {
		return s.cluster.ClusterOfServer(0)
	}
	return id
}

func (s *Service) SetCertificates(c *certificatesvc.Service) { s.certificates = c }

// SetMiddlewares wires the gateway-middleware service (kind: Middleware).
func (s *Service) SetMiddlewares(m *middlewaresvc.Service) { s.middlewares = m }

// NewService wires the apply engine over the existing resource services.
func NewService(apps *application.Service, storage *storage.Service, dbs *database.Service, stacks *stack.Service, secrets *secret.Service, routes *route.Service, domains *domain.Service, registries *registry.Service) *Service {
	return &Service{
		apps: apps, storage: storage, dbs: dbs, stacks: stacks,
		secrets: secrets, routes: routes, domains: domains, registries: registries,
	}
}

// Nil-safe: when unset, the exposure fields are ignored.
func (s *Service) SetPortExposure(bindings *portbinding.Service) {
	s.extAccess, s.bindings = true, bindings
}

// Options tunes a plan/apply.
type Options struct {
	Prune bool
	// Labels every touched resource with the GitOps project id so it can be listed or torn down alone.
	OwnerSource string
	// GitOps never sets it: a sync acts on nobody's behalf.
	Admin bool
	// For previews; a real apply still converges everything else and records the app as failed.
	CheckReferences bool
	// RequireSealedSecrets refuses the bundle when a Secret carries a plaintext value.
	RequireSealedSecrets bool
	// The resources stay the project's, so its next sync reverts the change.
	Force bool
}

type adminCtxKey struct{}

func withRunOptions(ctx context.Context, opts Options) context.Context {
	ctx = withOwnerSource(ctx, opts.OwnerSource)
	if opts.Admin {
		ctx = context.WithValue(ctx, adminCtxKey{}, true)
	}
	return ctx
}

type runStateCtxKey struct{}

// runState collects what one apply changed, so work that depends on several changes runs once at the end.
type runState struct {
	mu             sync.Mutex
	changedStacks  map[uint]bool
	rotatedSecrets map[string]bool
	deployedApps   map[uint]bool
}

func withRunState(ctx context.Context) (context.Context, *runState) {
	st := &runState{changedStacks: map[uint]bool{}, rotatedSecrets: map[string]bool{}, deployedApps: map[uint]bool{}}
	return context.WithValue(ctx, runStateCtxKey{}, st), st
}

func runStateOf(ctx context.Context) *runState {
	st, _ := ctx.Value(runStateCtxKey{}).(*runState)
	return st
}

func stackEnvChanged(ctx context.Context, stackID uint) {
	if st, ok := ctx.Value(runStateCtxKey{}).(*runState); ok {
		st.mu.Lock()
		st.changedStacks[stackID] = true
		st.mu.Unlock()
	}
}

func appDeployed(ctx context.Context, appID uint) {
	if st, ok := ctx.Value(runStateCtxKey{}).(*runState); ok {
		st.mu.Lock()
		st.deployedApps[appID] = true
		st.mu.Unlock()
	}
}

func isAdmin(ctx context.Context) bool {
	v, _ := ctx.Value(adminCtxKey{}).(bool)
	return v
}

// Carried in the context to avoid widening every signature; per-apply so concurrent runs don't share it.
type ownerSourceCtxKey struct{}

func withOwnerSource(ctx context.Context, src string) context.Context {
	if src == "" {
		return ctx
	}
	return context.WithValue(ctx, ownerSourceCtxKey{}, src)
}

func ownerSource(ctx context.Context) string {
	v, _ := ctx.Value(ownerSourceCtxKey{}).(string)
	return v
}

func managedBy(ctx context.Context) string {
	return ownerOf(ownerSource(ctx))
}

func ownerOf(source string) string {
	if source != "" {
		return ManagedByGitOps
	}
	return ManagedByApply
}

func claim(ctx context.Context, m models.Metadata) models.Metadata {
	if ownerSource(ctx) == "" || m[models.MetaGitOpsSource] != "" {
		return m
	}
	if owner := m[models.MetaManagedBy]; owner != ManagedByGitOps && owner != ManagedByApply {
		return m
	}
	return tagSource(ctx, models.SetBuiltin(m, models.MetaManagedBy, ManagedByGitOps))
}

// Judged by managed-by, since older resources carry no source label.
func refuseGitOpsOwned(plan *declarative.Plan, actual *declarative.ResourceSet) error {
	var owned []string
	for _, ch := range plan.Changes {
		if ch.Action != declarative.ActionUpdate {
			continue
		}
		if a, ok := actual.Get(string(ch.Kind) + "/" + ch.Name); ok && a.Metadata.Labels[declarative.LabelManagedBy] == ManagedByGitOps {
			owned = append(owned, string(ch.Kind)+"/"+ch.Name)
		}
	}
	if len(owned) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s. Change them in the project's Git repository, or use --force to override until its next sync",
		ErrOwnedByGitOps, strings.Join(owned, ", "))
}

func tagSource(ctx context.Context, m models.Metadata) models.Metadata {
	if src := ownerSource(ctx); src != "" {
		return models.SetBuiltin(m, models.MetaGitOpsSource, src)
	}
	return m
}

// Result is the outcome of an apply run.
type Result struct {
	Plan        *declarative.Plan `json:"plan"`
	Applied     int               `json:"applied"`
	DryRun      bool              `json:"dry_run"`
	Failures    []Failure         `json:"failures,omitempty"`
	WorkspaceID uint              `json:"workspace_id"`
	// A converged sync is still degraded by these.
	FailedJobs []string `json:"failed_jobs,omitempty"`
}

// Failure records one change that could not be applied.
type Failure struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"`
	Error  string `json:"error"`
}

func (s *Service) Plan(ctx context.Context, workspaceID uint, manifests []byte, opts Options) (*declarative.Plan, *declarative.ResourceSet, error) {
	desired, err := declarative.Parse(manifests)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if opts.RequireSealedSecrets {
		if names := declarative.PlainSecrets(desired); len(names) > 0 {
			return nil, nil, fmt.Errorf("%w: %w: %s (seal them with `miabi secrets seal -f <file> --in-place`)",
				ErrInvalidManifest, ErrPlainSecret, strings.Join(names, ", "))
		}
	}
	// Before rendering, which replaces every reference it can resolve with its value.
	if opts.CheckReferences {
		if err := declarative.CheckReferences(desired.References(), s.knownNames(workspaceID, desired)); err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
		}
	}
	s.render(workspaceID, desired)
	actual, err := s.snapshot(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}

	plan := declarative.BuildPlan(desired, actual, declarative.PlanOptions{
		Prune: opts.Prune, PruneManagedBy: ownerOf(opts.OwnerSource), PruneGitOpsSource: opts.OwnerSource,
	})
	if opts.OwnerSource == "" && !opts.Force {
		if err := refuseGitOpsOwned(plan, actual); err != nil {
			return nil, nil, err
		}
	}
	if err := s.checkLocations(workspaceID, plan, desired, opts.Admin); err != nil {
		return nil, nil, err
	}
	return plan, desired, nil
}

// A failed change is recorded and the run continues so independent resources still converge.
func (s *Service) Apply(ctx context.Context, workspaceID uint, manifests []byte, opts Options) (*Result, error) {
	plan, desired, err := s.Plan(ctx, workspaceID, manifests, opts)
	if err != nil {
		return nil, err
	}
	// Carry the owning source into execution so created resources get labeled.
	ctx = withRunOptions(ctx, opts)
	ctx, run := withRunState(ctx)
	res := &Result{Plan: plan, WorkspaceID: workspaceID}
	for _, ch := range plan.Changes {
		if ch.Action == declarative.ActionNoop {
			continue
		}
		dr, _ := desired.Get(string(ch.Kind) + "/" + ch.Name)
		if err := s.execute(ctx, workspaceID, ch, dr, desired); err != nil {
			res.Failures = append(res.Failures, Failure{
				Kind: string(ch.Kind), Name: ch.Name, Action: string(ch.Action), Error: err.Error(),
			})
			continue
		}
		res.Applied++
	}
	res.Failures = append(res.Failures, s.redeployChanged(workspaceID, run)...)
	// Derived from the raw manifest because the change loop has already resolved env templates.
	if raw, perr := declarative.Parse(manifests); perr == nil {
		s.linkDatabasesToApps(workspaceID, raw)
	}
	res.FailedJobs = s.failedJobs(workspaceID, opts.OwnerSource)
	return res, nil
}

func (s *Service) ApplyResource(ctx context.Context, workspaceID uint, manifests []byte, opts Options, kind, name string) (*Result, error) {
	plan, desired, err := s.Plan(ctx, workspaceID, manifests, opts)
	if err != nil {
		return nil, err
	}
	key := string(declarative.Kind(kind)) + "/" + name
	ctx = withRunOptions(ctx, opts)
	ctx, run := withRunState(ctx)
	res := &Result{WorkspaceID: workspaceID}
	for _, ch := range plan.Changes {
		if string(ch.Kind)+"/"+ch.Name != key {
			continue // a prune-delete or another resource — never touched by a single sync
		}
		res.Plan = &declarative.Plan{Changes: []declarative.Change{ch}}
		if ch.Action == declarative.ActionNoop {
			return res, nil
		}
		dr, _ := desired.Get(key)
		if err := s.execute(ctx, workspaceID, ch, dr, desired); err != nil {
			res.Failures = append(res.Failures, Failure{Kind: string(ch.Kind), Name: ch.Name, Action: string(ch.Action), Error: err.Error()})
			return res, nil
		}
		res.Applied++
		res.Failures = append(res.Failures, s.redeployChanged(workspaceID, run)...)
		// Keep database↔app links in sync when the applied resource is an app/db.
		if raw, perr := declarative.Parse(manifests); perr == nil {
			s.linkDatabasesToApps(workspaceID, raw)
		}
		return res, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrResourceNotFound, key)
}

func (s *Service) DeleteResource(ctx context.Context, workspaceID uint, kind, name string) (*Result, error) {
	actual, err := s.snapshot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if _, ok := actual.Get(string(declarative.Kind(kind)) + "/" + name); !ok {
		return &Result{WorkspaceID: workspaceID}, nil // already gone
	}
	ch := declarative.Change{Kind: declarative.Kind(kind), Name: name, Action: declarative.ActionDelete}
	res := &Result{WorkspaceID: workspaceID, Plan: &declarative.Plan{Changes: []declarative.Change{ch}}}
	if err := s.execute(ctx, workspaceID, ch, declarative.Resource{}, nil); err != nil {
		res.Failures = append(res.Failures, Failure{Kind: kind, Name: name, Action: string(ch.Action), Error: err.Error()})
		return res, err
	}
	res.Applied++
	return res, nil
}

// Best-effort and idempotent: never steals an already attached database.
func (s *Service) linkDatabasesToApps(workspaceID uint, raw *declarative.ResourceSet) {
	if raw == nil {
		return
	}
	all := raw.All()
	for i := range all {
		if all[i].Database == nil {
			continue
		}
		dep := all[i].Metadata.Name
		appName := rawConsumerApp(all, dep)
		if appName == "" {
			continue
		}
		// Resolve the declaratively named database so we never attach another app's database on the same instance.
		db, _, ok := s.dbs.FindDatabaseByDeclName(workspaceID, dep)
		if !ok {
			// Legacy rows without a declaratively named database.
			inst, err := s.findInstance(workspaceID, dep)
			if err != nil {
				continue
			}
			dbs, err := s.dbs.ListDatabases(workspaceID, inst.ID)
			if err != nil || len(dbs) == 0 {
				continue // no logical database (e.g. Redis) to attach
			}
			db = &dbs[0]
		}
		if db.ApplicationID != nil {
			continue // already attached (to this app or, deliberately, another)
		}
		app, err := s.findApp(workspaceID, appName)
		if err != nil {
			continue
		}
		_, _ = s.dbs.AttachToApp(workspaceID, db.ID, app.ID, database.EnvLink{})
	}
}

// Empty when the consumer is ambiguous; mirrors the marketplace consumerApp heuristic.
func rawConsumerApp(all []declarative.Resource, dep string) string {
	token := ".databases." + dep + "."
	apps, sole := 0, ""
	for i := range all {
		if all[i].Application == nil {
			continue
		}
		apps++
		sole = all[i].Metadata.Name
		for _, v := range all[i].Application.Env {
			if strings.Contains(v, token) {
				return all[i].Metadata.Name
			}
		}
	}
	// Stack-shared connection details name no app, so the stack's first member owns the link.
	for i := range all {
		st := all[i].Stack
		if st == nil || !referencesToken(st.Env, token) {
			continue
		}
		for j := range all {
			if a := all[j].Application; a != nil && a.Stack == all[i].Metadata.Name {
				return all[j].Metadata.Name
			}
		}
	}
	if apps == 1 {
		return sole
	}
	return ""
}

func referencesToken(env map[string]string, token string) bool {
	for _, v := range env {
		if strings.Contains(v, token) {
			return true
		}
	}
	return false
}

// Teardown leaves resources that predate per-source labeling untouched.
func (s *Service) Teardown(ctx context.Context, workspaceID uint, sourceID string) (*Result, error) {
	if sourceID == "" {
		return &Result{}, nil
	}
	actual, err := s.snapshot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	// Diffing against an empty set makes BuildPlan emit ordered deletes for exactly these.
	owned := declarative.NewResourceSet()
	for _, r := range actual.All() {
		if r.Metadata.Labels[declarative.LabelManagedBy] == ManagedByGitOps &&
			r.Metadata.Labels[declarative.LabelGitOpsSource] == sourceID {
			owned.Add(r)
		}
	}
	plan := declarative.BuildPlan(declarative.NewResourceSet(), owned, declarative.PlanOptions{
		Prune: true, PruneManagedBy: ManagedByGitOps,
	})
	res := &Result{Plan: plan, WorkspaceID: workspaceID}
	for _, ch := range plan.Changes {
		if ch.Action != declarative.ActionDelete {
			continue
		}
		if err := s.execute(ctx, workspaceID, ch, declarative.Resource{}, nil); err != nil {
			res.Failures = append(res.Failures, Failure{
				Kind: string(ch.Kind), Name: ch.Name, Action: string(ch.Action), Error: err.Error(),
			})
			continue
		}
		res.Applied++
	}
	return res, nil
}

// Delete ignores ownership, unlike a pruning Apply, since the caller named each resource explicitly.
func (s *Service) Delete(ctx context.Context, workspaceID uint, manifests []byte, dryRun bool) (*Result, error) {
	desired, err := declarative.Parse(manifests)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	actual, err := s.snapshot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	// Diffing against an empty set makes BuildPlan emit ordered deletes for exactly these.
	targets := declarative.NewResourceSet()
	for _, d := range desired.All() {
		if a, ok := actual.Get(d.Key()); ok {
			targets.Add(a)
		}
	}
	plan := declarative.BuildPlan(declarative.NewResourceSet(), targets, declarative.PlanOptions{Prune: true})
	res := &Result{Plan: plan, WorkspaceID: workspaceID, DryRun: dryRun}
	if dryRun {
		return res, nil
	}
	for _, ch := range plan.Changes {
		if ch.Action != declarative.ActionDelete {
			continue
		}
		if err := s.execute(ctx, workspaceID, ch, declarative.Resource{}, nil); err != nil {
			res.Failures = append(res.Failures, Failure{
				Kind: string(ch.Kind), Name: ch.Name, Action: string(ch.Action), Error: err.Error(),
			})
			continue
		}
		res.Applied++
	}
	return res, nil
}

// Lenient: a reference to a not-yet-provisioned database in the same bundle stays a template.
func (s *Service) render(workspaceID uint, desired *declarative.ResourceSet) {
	ctx := declarative.RenderContext{
		Databases: s.databaseViews(workspaceID),
		Secrets:   s.secretViews(workspaceID),
		// Overlaid with the bundle so a sibling reference resolves on the first run.
		Apps: s.appViews(workspaceID, desired),
	}
	r := declarative.NewRenderer(ctx)
	for i := range desired.All() {
		res := desired.All()[i]
		switch {
		case res.Application != nil && len(res.Application.Env) > 0:
			res.Application.Env = r.RenderEnvLenient(res.Metadata.Name, res.Application.Env)
			res.Application.SecretEnv = withKeys(res.Application.SecretEnv, r.CredentialKeys())
			desired.Add(res) // write back the rendered copy
		case res.Stack != nil && len(res.Stack.Env) > 0:
			res.Stack.Env = r.RenderEnvLenient(res.Metadata.Name, res.Stack.Env)
			res.Stack.SecretEnv = withKeys(res.Stack.SecretEnv, r.CredentialKeys())
			desired.Add(res)
		case res.Registry != nil:
			s.renderRegistryPasswordLenient(r, res.Metadata.Name, res.Registry)
			desired.Add(res)
		case res.Config != nil:
			// Lenient so a later database in the bundle doesn't break planning; applyConfig re-renders strictly.
			if data, err := s.renderConfigData(workspaceID, res.Metadata.Name, res.Config); err == nil {
				res.Config.DigestFP = configsvc.Digest(data)
			}
			desired.Add(res)
		case res.SealedSecret != nil:
			res.SealedSecret.SealedFP = sealed.Fingerprint(res.SealedSecret.Value)
			desired.Add(res)
		case res.Middleware != nil:
			// Fingerprinted from the rendered rule so a rotated secret converges instead of comparing equal.
			if rule, err := s.renderMiddlewareRule(workspaceID, res.Middleware); err == nil {
				res.Middleware.RuleFP = middlewareRuleFP(rule)
			}
			desired.Add(res)
		}
	}
	s.stampConfigFP(workspaceID, desired)
}

// Inputs exist only in marketplace templates, never in an apply.
func (s *Service) knownNames(workspaceID uint, desired *declarative.ResourceSet) declarative.KnownNames {
	known := declarative.KnownNames{"databases": {}, "secrets": {}, "applications": {}, "inputs": {}}
	for name := range s.databaseViews(workspaceID) {
		known["databases"][name] = true
	}
	for name := range s.secretViews(workspaceID) {
		known["secrets"][name] = true
	}
	for name := range s.appViews(workspaceID, desired) {
		known["applications"][name] = true
	}
	for _, r := range desired.All() {
		switch r.Kind {
		case declarative.KindDatabase:
			known["databases"][r.Metadata.Name] = true
		case declarative.KindSecret, declarative.KindSealedSecret:
			known["secrets"][r.Metadata.Name] = true
		case declarative.KindApplication:
			known["applications"][r.Metadata.Name] = true
		}
	}
	return known
}

// Mounted content is invisible to other diffed fields; reloadPolicy: none stays empty to suppress the redeploy.
func (s *Service) stampConfigFP(workspaceID uint, set *declarative.ResourceSet) {
	digests := map[string]string{}
	if s.configs != nil {
		if live, err := s.configs.List(workspaceID); err == nil {
			for i := range live {
				digests[live[i].Name] = live[i].Digest
			}
		}
	}
	// A config in the same bundle isn't live yet, so its rendered fingerprint stands in to avoid false drift.
	for _, res := range set.All() {
		if res.Config != nil && res.Config.DigestFP != "" {
			digests[res.Metadata.Name] = res.Config.DigestFP
		}
	}
	for _, res := range set.All() {
		if res.Application == nil || res.Application.ReloadPolicy == declarative.ReloadNone {
			continue
		}
		res.Application.ConfigFP = configFingerprint(res.Application.Mounts, digests)
		set.Add(res)
	}
}

// Canonical JSON so Go's randomized map ordering can't make an unchanged rule look changed.
func middlewareRuleFP(rule map[string]any) string {
	if len(rule) == 0 {
		return ""
	}
	b, err := json.Marshal(canonicalRule(rule))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Key-sorted maps so encoding/json emits the same bytes for the same content.
func canonicalRule(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([][2]any, 0, len(keys))
		for _, k := range keys {
			out = append(out, [2]any{k, canonicalRule(t[k])})
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = canonicalRule(t[i])
		}
		return out
	default:
		return v
	}
}

// Sorted by path so the value is stable across mount reordering.
func configFingerprint(mounts []declarative.MountSpec, digests map[string]string) string {
	var parts []string
	for _, mt := range mounts {
		if mt.Config == "" {
			continue
		}
		parts = append(parts, mt.Path+"\x00"+digests[mt.Config])
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

// Strict: an unresolvable reference fails the apply rather than storing a broken credential.
func (s *Service) renderRegistryPassword(r *declarative.Renderer, name string, spec *declarative.RegistrySpec) error {
	if spec.Password == "" {
		return nil
	}
	// The runtime ${{ secrets.X }} form must pass through untouched; Go templates would misread the inner {{ }}.
	if ref := secret.RefName(spec.Password); ref != "" {
		// Fingerprint the canonical spelling so spacing variants don't cause perpetual drift.
		spec.PasswordFP = registry.Fingerprint(name, secret.Ref(ref))
		return nil
	}
	rendered, err := r.RenderString("registry."+name+".password", spec.Password)
	if err != nil {
		return err
	}
	spec.Password = rendered
	if !strings.Contains(rendered, "{{") {
		spec.PasswordFP = registry.Fingerprint(name, rendered)
	}
	return nil
}

func (s *Service) renderRegistryPasswordLenient(r *declarative.Renderer, name string, spec *declarative.RegistrySpec) {
	_ = s.renderRegistryPassword(r, name, spec)
}

// Strict: a reference that still cannot resolve at execution time is a real error.
func (s *Service) renderAppEnv(workspaceID uint, spec *declarative.ApplicationSpec, set *declarative.ResourceSet) error {
	if spec == nil || len(spec.Env) == 0 {
		return nil
	}
	r := declarative.NewRenderer(declarative.RenderContext{
		Databases: s.databaseViews(workspaceID),
		Secrets:   s.secretViews(workspaceID),
		// Apps share a plan rank, so the bundle overlay makes a not-yet-created sibling addressable by name.
		Apps: s.appViews(workspaceID, set),
	})
	rendered, err := r.RenderEnv("application", spec.Env)
	if err != nil {
		return err
	}
	spec.Env = rendered
	spec.SecretEnv = withKeys(spec.SecretEnv, r.CredentialKeys())
	return nil
}

// Keys a render resolved a credential into are stored encrypted even when secretEnv omits them.
func withKeys(secretEnv, keys []string) []string {
	for _, k := range keys {
		if !slices.Contains(secretEnv, k) {
			secretEnv = append(secretEnv, k)
		}
	}
	return secretEnv
}

// Must run on the raw env, before renderAppEnv replaces the templates.
var appRefPattern = regexp.MustCompile(`\{\{[-\s]*\.applications\.([A-Za-z0-9_-]+)`)

var dbRefPattern = regexp.MustCompile(`\{\{[-\s]*\.databases\.([A-Za-z0-9_-]+)`)

type specRefs struct {
	apps, dbs map[string]bool
}

func refsOf(spec *declarative.ApplicationSpec) specRefs {
	if spec == nil {
		return specRefs{}
	}
	return specRefs{apps: envRefs(spec.Env, appRefPattern), dbs: envRefs(spec.Env, dbRefPattern)}
}

// Only stacks declared in the bundle are read; console-made stacks carry no references.
func (r specRefs) withStackRefs(spec *declarative.ApplicationSpec, set *declarative.ResourceSet) specRefs {
	if spec == nil || spec.Stack == "" || set == nil {
		return r
	}
	st, ok := set.Get(string(declarative.KindStack) + "/" + spec.Stack)
	if !ok || st.Stack == nil {
		return r
	}
	for _, m := range []struct {
		into    *map[string]bool
		pattern *regexp.Regexp
	}{{&r.apps, appRefPattern}, {&r.dbs, dbRefPattern}} {
		for name := range envRefs(st.Stack.Env, m.pattern) {
			if *m.into == nil {
				*m.into = map[string]bool{}
			}
			(*m.into)[name] = true
		}
	}
	return r
}

// Only references whose target placement is known are judged; the rest are left to the render.
func (s *Service) checkAppRefPlacement(workspaceID uint, name string, refs specRefs, self *appPlacement) error {
	if len(refs.apps) == 0 && len(refs.dbs) == 0 {
		return nil
	}
	apps, err := s.appNodes(workspaceID)
	if err != nil {
		return nil // placement unknown: never fail an apply on a lookup that is only advisory
	}
	if self != nil {
		apps[name] = *self
	}
	check := s.refCheck()
	if err := check.refError(name, apps, "application", refs.apps, apps); err != nil {
		return err
	}
	if len(refs.dbs) == 0 {
		return nil
	}
	return check.refError(name, apps, "database", refs.dbs, s.databaseNodes(workspaceID))
}

func envRefs(env map[string]string, pattern *regexp.Regexp) map[string]bool {
	refs := map[string]bool{}
	for _, v := range env {
		for _, m := range pattern.FindAllStringSubmatch(v, -1) {
			refs[m[1]] = true
		}
	}
	return refs
}

type refCheck struct {
	swarm func(clusterID uint) bool
	label func(clusterID uint) string
}

func (s *Service) refCheck() refCheck {
	if s.cluster == nil {
		return refCheck{}
	}
	return refCheck{swarm: s.cluster.IsSwarm, label: s.cluster.LocationLabel}
}

func (c refCheck) location(clusterID uint) string {
	if c.label == nil {
		return fmt.Sprintf("location %d", clusterID)
	}
	return c.label(clusterID)
}

// Pure so the rule can be tested without a workspace.
func (c refCheck) refError(name string, apps map[string]appPlacement, kind string, refs map[string]bool, targets map[string]appPlacement) error {
	self, ok := apps[name]
	if !ok {
		return nil // this app has no live placement yet, so nothing to compare against
	}
	for _, ref := range sortedKeys(refs) {
		target, known := targets[ref]
		if !known {
			continue
		}
		if target.cluster != self.cluster {
			return fmt.Errorf("%w: application %q is in %s, %s %q is in %s — private networks don't span locations. "+
				"Deploy them to one location", ErrInvalidManifest, name, c.location(self.cluster), kind, ref, c.location(target.cluster))
		}
		if target.id == self.id || (c.swarm != nil && c.swarm(self.cluster)) {
			continue
		}
		return fmt.Errorf("%w: application %q references %s %q, but they run on different nodes (%s and %s) "+
			"and a workspace network is node-local outside cluster mode — the address would not resolve. "+
			"Place them on one node, or enable cluster mode so the network spans both",
			ErrInvalidManifest, name, kind, ref, self.node, target.node)
	}
	return nil
}

// Deterministic so re-running an apply shows the same message.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type appPlacement struct {
	id      uint
	node    string
	cluster uint
}

func (s *Service) placementOf(serverID uint, serverName string, clusterID uint) appPlacement {
	label := serverName
	if label == "" {
		label = fmt.Sprintf("node %d", serverID)
		if serverID == 0 {
			label = "the local node"
		}
	}
	return appPlacement{id: serverID, node: label, cluster: s.clusterID(clusterID)}
}

func (s *Service) appNodes(workspaceID uint) (map[string]appPlacement, error) {
	apps, err := s.apps.List(workspaceID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]appPlacement, len(apps))
	for i := range apps {
		a := &apps[i]
		out[a.Name] = s.placementOf(a.ServerID, a.ServerName, a.ClusterID)
	}
	return out, nil
}

func (s *Service) databaseNodes(workspaceID uint) map[string]appPlacement {
	out := map[string]appPlacement{}
	instances, err := s.dbs.List(workspaceID)
	if err != nil {
		return out
	}
	byID := make(map[uint]appPlacement, len(instances))
	for i := range instances {
		inst := &instances[i]
		p := s.placementOf(inst.ServerID, inst.ServerName, inst.ClusterID)
		byID[inst.ID] = p
		out[inst.Name] = p
	}
	dbs, err := s.dbs.ListDatabasesByWorkspace(workspaceID)
	if err != nil {
		return out
	}
	for i := range dbs {
		if n := dbs[i].Metadata[models.MetaDeclarativeName]; n != "" {
			if p, ok := byID[dbs[i].InstanceID]; ok {
				out[n] = p
			}
		}
	}
	return out
}

// A manifest Database resolves to its own logical database's credentials, not the instance's.
func (s *Service) databaseViews(workspaceID uint) map[string]declarative.ConnView {
	out := map[string]declarative.ConnView{}
	instances, err := s.dbs.List(workspaceID)
	if err != nil {
		return out
	}
	instByID := map[uint]*models.DatabaseInstance{}
	for i := range instances {
		instByID[instances[i].ID] = &instances[i]
	}

	claimed := map[uint]bool{} // instance ids represented by a decl-named database
	if dbs, dErr := s.dbs.ListDatabasesByWorkspace(workspaceID); dErr == nil {
		for i := range dbs {
			declName := dbs[i].Metadata[models.MetaDeclarativeName]
			if declName == "" {
				continue
			}
			inst := instByID[dbs[i].InstanceID]
			if inst == nil {
				continue
			}
			conn, cErr := s.dbs.DatabaseConnection(inst, &dbs[i])
			if cErr != nil {
				continue
			}
			v := connView(conn)
			v.PasswordSecret, v.URISecret = database.PasswordSecretName(inst, &dbs[i]), database.URLSecretName(inst, &dbs[i])
			out[declName] = v
			claimed[inst.ID] = true
		}
	}

	// Remaining instances keep the legacy instance-name keying.
	for i := range instances {
		if claimed[instances[i].ID] {
			continue
		}
		if _, ok := out[instances[i].Name]; ok {
			continue
		}
		conn, err := s.appConnection(workspaceID, &instances[i])
		if err != nil {
			continue
		}
		out[instances[i].Name] = conn
	}
	return out
}

// Falls back to the admin connection for Redis or legacy instances without a logical database.
func (s *Service) appConnection(workspaceID uint, inst *models.DatabaseInstance) (declarative.ConnView, error) {
	if inst.SupportsLogicalDatabases() {
		if dbs, err := s.dbs.ListDatabases(workspaceID, inst.ID); err == nil && len(dbs) > 0 {
			if conn, cErr := s.dbs.DatabaseConnection(inst, &dbs[0]); cErr == nil {
				v := connView(conn)
				v.PasswordSecret, v.URISecret = database.PasswordSecretName(inst, &dbs[0]), database.URLSecretName(inst, &dbs[0])
				return v, nil
			}
		}
	}
	conn, err := s.dbs.InstanceConnection(inst)
	if err != nil {
		return declarative.ConnView{}, err
	}
	v := connView(conn)
	v.PasswordSecret, v.URISecret = database.InstancePasswordSecretName(inst), database.InstanceURLSecretName(inst)
	return v, nil
}

// readOnlySpec reports whether a logical database has a read-only user, for the snapshot.
func readOnlySpec(d *models.Database) *bool {
	on := d.ReadOnlyUsername != ""
	return &on
}

func connView(conn database.ConnectionInfo) declarative.ConnView {
	return declarative.ConnView{
		Host: conn.Host, Port: strconv.Itoa(conn.Port), User: conn.Username,
		Password: conn.Password, Name: conn.Database, URI: conn.URI,
		ReadOnlyUser: conn.ReadOnlyUsername, ReadOnlyPassword: conn.ReadOnlyPassword, ReadOnlyURI: conn.ReadOnlyURI,
	}
}

// Without secret values, any env referencing a secret fails the strict render and the app never redeploys.
func (s *Service) appViews(workspaceID uint, desired *declarative.ResourceSet) map[string]declarative.AppView {
	out := map[string]declarative.AppView{}
	if apps, err := s.apps.List(workspaceID); err == nil {
		for i := range apps {
			a := &apps[i]
			out[a.Name] = declarative.AppView{
				Host: a.Name, Port: portString(a.Port), Scheme: "http", Alias: node.AppAlias(a),
			}
		}
	}
	return overlayDesiredApps(out, desired)
}

// Split out because it decides whether a first apply works, and needs no database to test.
func overlayDesiredApps(live map[string]declarative.AppView, desired *declarative.ResourceSet) map[string]declarative.AppView {
	out := make(map[string]declarative.AppView, len(live))
	for k, v := range live {
		out[k] = v
	}
	if desired == nil {
		return out
	}
	for _, res := range desired.All() {
		if res.Application == nil {
			continue
		}
		name := res.Metadata.Name
		view := declarative.AppView{Host: name, Scheme: "http", Alias: out[name].Alias}
		if p, ok := primaryPort(res.Application.Ports); ok {
			view.Port = portString(p.Container)
			if p.Scheme != "" {
				view.Scheme = p.Scheme
			}
		} else {
			// Keep the live app's ports when the bundle omits them rather than dropping to a portless URL.
			view.Port, view.Scheme = out[name].Port, firstNonEmpty(out[name].Scheme, "http")
		}
		out[name] = view
	}
	return out
}

// Same rule the app service uses when it records Application.Port.
func primaryPort(ports []declarative.PortSpec) (declarative.PortSpec, bool) {
	for _, p := range ports {
		if p.Container > 0 {
			return p, true
		}
	}
	return declarative.PortSpec{}, false
}

func portString(p int) string {
	if p <= 0 {
		return ""
	}
	return strconv.Itoa(p)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Kept for render paths that predate application references.
func (s *Service) appAliasViews(workspaceID uint) map[string]declarative.AppView {
	return s.appViews(workspaceID, nil)
}

func (s *Service) secretViews(workspaceID uint) map[string]string {
	out := map[string]string{}
	secrets, err := s.secrets.List(workspaceID)
	if err != nil {
		return out
	}
	for i := range secrets {
		val, err := s.secrets.Reveal(workspaceID, secrets[i].ID)
		if err != nil {
			continue
		}
		out[secrets[i].Name] = val
	}
	return out
}

var ErrNotExportable = errors.New("this application cannot be exported as a manifest")

// Reuses the plan snapshot so an export can't drift; only mounted volumes are included so --prune can't delete others.
func (s *Service) ExportApplication(ctx context.Context, workspaceID uint, name string, includeJobs bool) ([]byte, error) {
	set, err := s.snapshot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	app, ok := set.Get(string(declarative.KindApplication) + "/" + name)
	if !ok {
		return nil, fmt.Errorf("%w: application %q not found", ErrNotExportable, name)
	}
	out := declarative.NewResourceSet()
	// A location the bundle would land in anyway is left out, so the export applies to another install.
	defaultLocation := s.defaultLocation(workspaceID)
	// Volumes first: the bundle reads top-down, and a mount reads better after the thing it mounts.
	for _, mt := range app.Application.Mounts {
		if mt.Volume == "" {
			continue
		}
		if v, found := set.Get(string(declarative.KindVolume) + "/" + mt.Volume); found {
			if v.Volume != nil && v.Volume.Location() == defaultLocation {
				v.Volume.Placement = nil
			}
			out.Add(v)
		}
	}
	trimDefaults(app.Application, defaultLocation)
	// Uids are per-install and would bind this document to a different resource elsewhere.
	app.Metadata = declarative.Meta{Name: app.Metadata.Name, Annotations: app.Metadata.Annotations}
	out.Add(app)
	if includeJobs {
		exportJobs(set, out, name)
	}
	return declarative.Marshal(out)
}

// trimDefaults drops what an apply elsewhere would choose anyway, and the blocks that leaves empty.
func trimDefaults(a *declarative.ApplicationSpec, defaultLocation string) {
	// The export holds one app, not its Stack, and a manifest may only name a Stack it declares.
	a.Stack = ""
	if p := a.Placement; p != nil {
		if p.Location == defaultLocation {
			p.Location = ""
		}
		if p.Location == "" && p.Constraints == nil {
			a.Placement = nil
		}
	}
	if hc := a.Healthcheck; hc != nil {
		if hc.Type == string(models.HealthcheckImage) {
			a.Healthcheck = nil
		} else {
			hc.IntervalSeconds = trimDefault(hc.IntervalSeconds, 30)
			hc.TimeoutSeconds = trimDefault(hc.TimeoutSeconds, 5)
			hc.Retries = trimDefault(hc.Retries, 3)
		}
	}
	if d := a.Deployment; d != nil {
		if d.Runtime == string(models.RuntimeContainer) {
			d.Runtime = ""
		}
		if d.Strategy == string(models.DeployRolling) {
			d.Strategy = ""
		}
		if *d == (declarative.DeploymentSpec{}) {
			a.Deployment = nil
		}
	}
}

func locationSpec(location string) *declarative.PlacementSpec {
	if location == "" {
		return nil
	}
	return &declarative.PlacementSpec{Location: location}
}

// "0" means none, so a manifest removing a limit with "0" still compares against it.
func databaseResourcesSpec(inst *models.DatabaseInstance) *declarative.DatabaseResourcesSpec {
	return &declarative.DatabaseResourcesSpec{
		Memory: strconv.FormatInt(inst.MemoryBytes, 10),
		CPU:    strconv.FormatFloat(float64(inst.NanoCPUs)/1e9, 'f', -1, 64),
		Size:   inst.SizeClass,
	}
}

func databaseResources(spec *declarative.DatabaseSpec) database.Resources {
	if spec == nil || spec.Resources == nil {
		return database.Resources{}
	}
	mb, _ := spec.Resources.MemoryBytes()
	nc, _ := spec.Resources.NanoCPUs()
	return database.Resources{MemoryBytes: mb, NanoCPUs: nc, Size: spec.Resources.Size}
}

func databaseLocationSpec(location string) *declarative.DatabasePlacementSpec {
	if location == "" {
		return nil
	}
	return &declarative.DatabasePlacementSpec{Location: location}
}

func (s *Service) snapshot(ctx context.Context, workspaceID uint) (*declarative.ResourceSet, error) {
	set := declarative.NewResourceSet()
	appSlugByID := map[uint]string{}

	// Apps reference volumes by name, so the id -> name map must exist first.
	vols, err := s.storage.List(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot volumes: %w", err)
	}
	cfgNameByID := map[uint]string{}
	if s.configs != nil {
		if live, cerr := s.configs.List(workspaceID); cerr == nil {
			for i := range live {
				cfgNameByID[live[i].ID] = live[i].Name
			}
		}
	}
	volNameByID := make(map[uint]string, len(vols))
	for i := range vols {
		volNameByID[vols[i].ID] = vols[i].Name
		size := ""
		if vols[i].SizeBytes > 0 {
			size = strconv.FormatInt(vols[i].SizeBytes, 10)
		}
		set.Add(declarative.Resource{
			APIVersion: declarative.APIVersion, Kind: declarative.KindVolume,
			Metadata: metaA(vols[i].UID, vols[i].Name, vols[i].Metadata, vols[i].Annotations),
			Volume: &declarative.VolumeSpec{
				Size:         size,
				StorageClass: vols[i].StorageClassName,
				Placement:    locationSpec(s.locationName(vols[i].ClusterID)),
			},
		})
	}

	// Apps reference registries by name; password fingerprints reveal rotations without handling the token.
	regs, err := s.registries.List(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot registries: %w", err)
	}
	fps, err := s.registries.Fingerprints(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot registry fingerprints: %w", err)
	}
	regNameByID := make(map[uint]string, len(regs))
	for i := range regs {
		regNameByID[regs[i].ID] = regs[i].Name
		set.Add(declarative.Resource{
			APIVersion: declarative.APIVersion, Kind: declarative.KindRegistry,
			Metadata: metaA(regs[i].UID, regs[i].Name, regs[i].Metadata, regs[i].Annotations),
			Registry: &declarative.RegistrySpec{
				Server:     regs[i].Server,
				Username:   regs[i].Username,
				PasswordFP: fps[regs[i].Name],
			},
		})
	}

	// Stacks before apps, like volumes and registries: an app names its stack.
	stacks, err := s.stacks.List(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot stacks: %w", err)
	}
	stackNameByID := make(map[uint]string, len(stacks))
	for i := range stacks {
		stackNameByID[stacks[i].ID] = stacks[i].Name
	}

	apps, err := s.apps.List(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot apps: %w", err)
	}
	for i := range apps {
		appSlugByID[apps[i].ID] = apps[i].Name
		full, err := s.apps.Get(workspaceID, apps[i].ID)
		if err != nil {
			continue
		}
		ext, pub := s.exposedPorts(workspaceID, full.ID)
		r := appResource(full, ext, pub, volNameByID, regNameByID, cfgNameByID)
		if r.Application.Digest != "" {
			r.Application.Digest = s.liveDigest(full)
		}
		if full.StackID != nil {
			r.Application.Stack = stackNameByID[*full.StackID]
		}
		if loc := s.locationName(full.ClusterID); loc != "" {
			if r.Application.Placement == nil {
				r.Application.Placement = &declarative.ApplicationPlacementSpec{}
			}
			r.Application.Placement.Location = loc
		}
		set.Add(r)
	}

	instances, err := s.dbs.List(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot databases: %w", err)
	}
	// Surfaced under the manifest name so a prune removes exactly that database, not its shared instance.
	declDBsByInstance := map[uint][]models.Database{}
	if dbs, dErr := s.dbs.ListDatabasesByWorkspace(workspaceID); dErr == nil {
		for i := range dbs {
			if dbs[i].Metadata[models.MetaDeclarativeName] != "" {
				declDBsByInstance[dbs[i].InstanceID] = append(declDBsByInstance[dbs[i].InstanceID], dbs[i])
			}
		}
	}
	for i := range instances {
		inst := &instances[i]
		if decls := declDBsByInstance[inst.ID]; len(decls) > 0 {
			// The instance host has no manifest identity of its own.
			for j := range decls {
				d := &decls[j]
				set.Add(declarative.Resource{
					APIVersion: declarative.APIVersion, Kind: declarative.KindDatabase,
					Metadata: metaA(d.UID, d.Metadata[models.MetaDeclarativeName], d.Metadata, nil),
					Database: &declarative.DatabaseSpec{
						Engine: string(inst.Engine), Version: inst.Version, Instance: "auto",
						Placement:    databaseLocationSpec(s.locationName(inst.ClusterID)),
						Resources:    databaseResourcesSpec(inst),
						ReadOnlyUser: readOnlySpec(d),
					},
				})
			}
			continue
		}
		set.Add(declarative.Resource{
			APIVersion: declarative.APIVersion, Kind: declarative.KindDatabase,
			Metadata: metaA(inst.UID, inst.Name, inst.Metadata, inst.Annotations),
			Database: &declarative.DatabaseSpec{
				Engine: string(inst.Engine), Version: inst.Version, Instance: "auto",
				Placement: databaseLocationSpec(s.locationName(inst.ClusterID)),
				Resources: databaseResourcesSpec(inst),
			},
		})
	}

	for i := range stacks {
		spec := &declarative.StackSpec{Description: stacks[i].Description, Placement: locationSpec(s.locationName(stacks[i].ClusterID))}
		spec.Env, spec.SecretEnv = s.stackEnvOf(workspaceID, stacks[i].ID)
		set.Add(declarative.Resource{
			APIVersion: declarative.APIVersion, Kind: declarative.KindStack,
			Metadata: metaA(stacks[i].UID, stacks[i].Name, stacks[i].Metadata, stacks[i].Annotations),
			Stack:    spec,
		})
	}

	secrets, err := s.secrets.List(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot secrets: %w", err)
	}
	for i := range secrets {
		r := declarative.Resource{APIVersion: declarative.APIVersion, Metadata: meta(secrets[i].UID, secrets[i].Name, secrets[i].Metadata)}
		if secrets[i].SealedFP != "" {
			r.Kind, r.SealedSecret = declarative.KindSealedSecret, &declarative.SealedSecretSpec{SealedFP: secrets[i].SealedFP}
		} else {
			r.Kind, r.Secret = declarative.KindSecret, &declarative.SecretSpec{}
		}
		set.Add(r)
	}

	if s.configs != nil {
		configs, cerr := s.configs.List(workspaceID)
		if cerr != nil {
			return nil, fmt.Errorf("snapshot configs: %w", cerr)
		}
		for i := range configs {
			c := configs[i]
			spec := &declarative.ConfigSpec{
				Mode: c.Mode, Sensitive: c.Sensitive, Delimiters: c.Delimiters,
				DigestFP: c.Digest,
			}
			// A sensitive config's content never enters a plan; only its fingerprint is carried.
			if !c.Sensitive {
				if data, derr := s.configs.Data(&c); derr == nil {
					spec.Data = data
				}
			}
			set.Add(declarative.Resource{
				APIVersion: declarative.APIVersion, Kind: declarative.KindConfig,
				Metadata: meta(c.UID, c.Name, c.Metadata), Config: spec,
			})
		}
	}

	if s.middlewares != nil {
		mws, merr := s.middlewares.List(workspaceID)
		if merr != nil {
			return nil, fmt.Errorf("snapshot middlewares: %w", merr)
		}
		for i := range mws {
			mw := mws[i]
			// Fingerprint the decrypted rule to match the desired side, and redact what reaches the plan.
			plain, derr := mwcatalog.DecryptSecrets(mw.Type, mw.Rule)
			if derr != nil {
				plain = mw.Rule
			}
			plain = mwcatalog.DropRetired(mw.Type, plain)
			set.Add(declarative.Resource{
				APIVersion: declarative.APIVersion, Kind: declarative.KindMiddleware,
				Metadata: declarative.Meta{Name: mw.Name},
				Middleware: &declarative.MiddlewareSpec{
					Type:   mw.Type,
					Paths:  append([]string(nil), mw.Paths...),
					Rule:   mwcatalog.Redact(mw.Type, mw.Rule),
					RuleFP: middlewareRuleFP(plain),
				},
			})
		}
	}

	if err := s.snapshotGitSources(workspaceID, set); err != nil {
		return nil, err
	}
	if err := s.snapshotGitRepositories(workspaceID, set); err != nil {
		return nil, err
	}

	// Map certificate ids back to names or every custom-TLS route would read as drift.
	certNameByID := map[uint]string{}
	if s.certificates != nil {
		if certs, cerr := s.certificates.List(workspaceID); cerr == nil {
			for i := range certs {
				certNameByID[certs[i].ID] = certs[i].Name
			}
		}
	}

	routes, err := s.routes.List(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot routes: %w", err)
	}
	for i := range routes {
		r := routes[i]
		// Generated routes are owned by the externalAccess port flag, so skip them as phantom Routes.
		if r.Generated {
			continue
		}
		path := r.Path
		if path == "" {
			path = "/"
		}
		set.Add(declarative.Resource{
			APIVersion: declarative.APIVersion, Kind: declarative.KindRoute,
			Metadata: meta(r.UID, r.Name, r.Metadata),
			Route:    routeSpecOf(r, appSlugByID[r.ApplicationID], path, certNameByID),
		})
	}

	domains, err := s.domains.List(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot domains: %w", err)
	}
	for i := range domains {
		d := domains[i]
		// Domains have no managed-by label, so a GitOps prune treats them as user-owned and never deletes them.
		set.Add(declarative.Resource{
			APIVersion: declarative.APIVersion, Kind: declarative.KindDomain,
			Metadata: declarative.Meta{Name: d.Name},
			Domain:   &declarative.DomainSpec{TLS: declarative.DomainTLSOf(string(d.TLSMode)), Wildcard: d.Wildcard},
		})
	}
	if err := s.snapshotJobs(workspaceID, set, appSlugByID, regNameByID); err != nil {
		return nil, err
	}
	// Both sides of the diff must carry the fingerprint or it reads as drift.
	s.stampConfigFP(workspaceID, set)
	return set, nil
}

// The owner label keeps prune scoped.
func meta(uid, name string, m models.Metadata) declarative.Meta {
	out := declarative.Meta{UID: uid, Name: name}
	if owner := m[models.MetaManagedBy]; owner != "" {
		out.Labels = map[string]string{declarative.LabelManagedBy: owner}
	}
	// Surface the owning GitOps source so a per-project teardown can match it.
	if src := m[models.MetaGitOpsSource]; src != "" {
		if out.Labels == nil {
			out.Labels = map[string]string{}
		}
		out.Labels[declarative.LabelGitOpsSource] = src
	}
	return out
}

func metaA(uid, name string, m, annotations models.Metadata) declarative.Meta {
	out := meta(uid, name, m)
	if len(annotations) > 0 {
		out.Annotations = annotations
	}
	return out
}

// A pin whose deploy failed plans an update instead of reading as converged.
func (s *Service) liveDigest(app *models.Application) string {
	var latest *models.Deployment
	if deps, err := s.apps.ListDeployments(app.ID, 1); err == nil && len(deps) > 0 {
		latest = &deps[0].Deployment
	}
	active, err := s.apps.ActiveRelease(app.ID)
	if err != nil {
		active = nil
	}
	return runningDigest(app.Metadata[MetaDigest], latest, active)
}

// An in-flight deploy toward the pin counts as the pin, or every sync would re-enqueue a slow rollout.
func runningDigest(pin string, latest *models.Deployment, active *models.Release) string {
	if latest != nil && !latest.Status.IsTerminal() && refDigest(latest.Image) == pin {
		return pin
	}
	running := ""
	if active != nil {
		running = active.Digest
		if running == "" {
			running = refDigest(active.Image)
		}
	}
	if running == pin {
		return pin
	}
	if running == "" {
		running = "an unpinned image"
	}
	return pin + " (not running; running " + running + ")"
}

// refDigest returns the "sha256:…" part of an image reference pinned by digest, or "".
func refDigest(ref string) string {
	if _, d, ok := strings.Cut(ref, "@"); ok {
		return d
	}
	return ""
}

// ext and pub are the currently exposed and published ports, so exposure converges idempotently.
func appResource(app *models.Application, ext, pub map[int]bool, volNameByID, regNameByID, cfgNameByID map[uint]string) declarative.Resource {
	spec := &declarative.ApplicationSpec{
		Image:           app.Image,
		Tag:             app.Tag,
		Digest:          app.Metadata[MetaDigest],
		Command:         app.Command,
		ContainerLabels: app.ContainerLabels,
		ExternalLabel:   app.ExternalLabel,
		Security:        securitySpecOf(app),
		Deployment:      deploymentSpecOf(app),
		Healthcheck:     healthcheckSpecOf(app),
	}
	// A git app is described by its source; the generated image tag means nothing in a manifest.
	if app.SourceType == models.AppSourceGit {
		spec.Image, spec.Tag, spec.Digest = "", "", ""
		spec.Source = &declarative.SourceSpec{
			Git: app.GitRepo, Ref: app.GitRef,
			Builder: app.Builder, Buildpacks: app.Buildpacks, BuildEnv: app.BuildEnv,
		}
		if app.BuildMethod != "" && app.BuildMethod != models.BuildAuto {
			spec.Source.BuildMethod = string(app.BuildMethod)
		}
	}
	// A deleted credential's dangling id reads as anonymous rather than inventing a name.
	if app.RegistryID != nil {
		spec.Registry = regNameByID[*app.RegistryID]
	}
	if app.ReloadPolicy != "" && app.ReloadPolicy != models.ReloadRestart {
		spec.ReloadPolicy = app.ReloadPolicy
	}
	if app.RuntimeKind == models.RuntimeService && len(app.PlacementConstraints) > 0 {
		spec.Placement = &declarative.ApplicationPlacementSpec{Constraints: app.PlacementConstraints}
	}
	// Privileged host-preset binds (VolumeID 0) aren't manifest-expressible.
	for _, m := range app.Mounts {
		if m.ConfigID != 0 {
			name := cfgNameByID[m.ConfigID]
			if name == "" {
				continue
			}
			spec.Mounts = append(spec.Mounts, declarative.MountSpec{
				Config: name, Key: m.ConfigKey, Path: m.Path, Mode: m.Mode, ReadOnly: true,
			})
			continue
		}
		if m.VolumeID == 0 {
			continue
		}
		name := volNameByID[m.VolumeID]
		if name == "" {
			continue // volume outside this snapshot (shouldn't happen); skip
		}
		spec.Mounts = append(spec.Mounts, declarative.MountSpec{Volume: name, Path: m.Path, ReadOnly: m.ReadOnly})
	}
	for _, p := range app.Ports {
		spec.Ports = append(spec.Ports, declarative.PortSpec{
			Name:           exportPortName(p.Name),
			Container:      p.ContainerPort,
			Protocol:       p.Protocol,
			Scheme:         p.Scheme,
			ExternalAccess: ext[p.ContainerPort],
			Publish:        pub[p.ContainerPort],
		})
	}
	if app.MemoryBytes > 0 || app.NanoCPUs > 0 || app.GPUCount > 0 {
		spec.Resources = &declarative.ResourceSpec{
			Memory:  strconv.FormatInt(app.MemoryBytes, 10), // bytes; canonical for diff
			CPU:     strconv.FormatFloat(float64(app.NanoCPUs)/1e9, 'f', -1, 64),
			GPU:     app.GPUCount,
			GPUKind: app.GPUKind,
		}
	}
	env := map[string]string{}
	var secretEnv []string
	for _, ev := range app.EnvVars {
		if ev.IsSecret {
			secretEnv = append(secretEnv, ev.Key)
			env[ev.Key] = "" // masked by the diff engine
			continue
		}
		env[ev.Key] = ev.Value
	}
	spec.Env = env
	spec.SecretEnv = secretEnv
	return declarative.Resource{
		APIVersion: declarative.APIVersion, Kind: declarative.KindApplication,
		Metadata: metaA(app.UID, app.Name, app.Metadata, app.Annotations), Application: spec,
	}
}

// tlsToSpec maps a route's TLS mode to the declarative spelling (none -> off).
func tlsToSpec(m models.RouteTLSMode) string {
	if m == models.RouteTLSNone {
		return "off"
	}
	return string(m)
}

// A console name a manifest would refuse is left out, so an export always applies.
func exportPortName(name string) string {
	if declarative.ValidPortName(name) {
		return name
	}
	return ""
}

// tlsFromSpec maps the declarative TLS spelling to a route TLS mode.
func tlsFromSpec(tls string) models.RouteTLSMode {
	switch tls {
	case "off":
		return models.RouteTLSNone
	case "custom":
		return models.RouteTLSCustom
	default:
		return models.RouteTLSACME
	}
}

// set is nil on the delete-only paths, which render nothing.
func (s *Service) execute(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource, set *declarative.ResourceSet) error {
	switch ch.Kind {
	case declarative.KindApplication:
		return s.applyApplication(ctx, workspaceID, ch, desired, set)
	case declarative.KindVolume:
		return s.applyVolume(ctx, workspaceID, ch, desired, set)
	case declarative.KindDatabase:
		return s.applyDatabase(ctx, workspaceID, ch, desired)
	case declarative.KindStack:
		return s.applyStack(ctx, workspaceID, ch, desired, set)
	case declarative.KindSecret:
		return s.applySecret(workspaceID, ch, desired)
	case declarative.KindSealedSecret:
		return s.applySealedSecret(ctx, workspaceID, ch, desired)
	case declarative.KindRoute:
		return s.applyRoute(ctx, workspaceID, ch, desired)
	case declarative.KindDomain:
		return s.applyDomain(workspaceID, ch, desired)
	case declarative.KindRegistry:
		return s.applyRegistry(ctx, workspaceID, ch, desired)
	case declarative.KindConfig:
		return s.applyConfig(workspaceID, ch, desired)
	case declarative.KindMiddleware:
		return s.applyMiddleware(ctx, workspaceID, ch, desired)
	case declarative.KindCronJob:
		return s.applyCronJob(ctx, workspaceID, ch, desired)
	case declarative.KindJob:
		return s.applyJob(ctx, workspaceID, ch, desired)
	case declarative.KindGitSource:
		return s.applyGitSource(ctx, workspaceID, ch, desired)
	case declarative.KindGitRepository:
		return s.applyGitRepository(ctx, workspaceID, ch, desired)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedKind, ch.Kind)
	}
}

// Verification is a runtime action, so a freshly applied domain starts unverified.
func (s *Service) applyDomain(workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	switch ch.Action {
	case declarative.ActionCreate:
		_, err := s.domains.Create(workspaceID, domain.Input{
			Name:     ch.Name,
			TLSMode:  models.DomainTLSMode(desired.Domain.TLSMode()),
			Wildcard: desired.Domain.Wildcard,
		})
		return err
	case declarative.ActionUpdate:
		d, err := s.findDomain(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		_, err = s.domains.Update(workspaceID, d.ID, domain.Input{
			Name:     ch.Name,
			TLSMode:  models.DomainTLSMode(desired.Domain.TLSMode()),
			Wildcard: desired.Domain.Wildcard,
		})
		return err
	case declarative.ActionDelete:
		d, err := s.findDomain(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		return s.domains.Delete(context.Background(), workspaceID, d.ID)
	}
	return nil
}

func (s *Service) findDomain(workspaceID uint, name string) (*models.Domain, error) {
	domains, err := s.domains.List(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range domains {
		if domains[i].Name == name {
			return &domains[i], nil
		}
	}
	return nil, fmt.Errorf("domain %q not found", name)
}

func (s *Service) applyStack(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource, set *declarative.ResourceSet) error {
	if ch.Action != declarative.ActionDelete {
		// Databases sort before stacks, so database refs are live here; the plan-time render was lenient.
		if err := s.renderStackEnv(workspaceID, desired.Stack, set); err != nil {
			return fmt.Errorf("%w: stack %q: %v", ErrInvalidManifest, ch.Name, err)
		}
	}
	switch ch.Action {
	case declarative.ActionCreate:
		target, err := s.place(ctx, workspaceID, ch, desired.Stack.Location(), true)
		if err != nil {
			return err
		}
		st, err := s.stacks.Create(ctx, workspaceID, stack.Input{
			Name:        ch.Name,
			Description: desired.Stack.Description,
			ClusterID:   target.ClusterID,
			Metadata:    tagSource(ctx, models.SetBuiltin(models.Metadata{}, models.MetaManagedBy, managedBy(ctx))),
			Annotations: desired.Metadata.Annotations,
		})
		if err != nil {
			return err
		}
		_, err = s.reconcileStackEnv(workspaceID, st.ID, desired.Stack)
		return err
	case declarative.ActionUpdate:
		if err := refuseMove(ch); err != nil {
			return err
		}
		st, err := s.findStack(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		desc := desired.Stack.Description
		if _, err := s.stacks.Update(workspaceID, st.ID, stack.UpdateInput{
			Description: &desc, Annotations: desired.Metadata.Annotations,
		}); err != nil {
			return err
		}
		changed, err := s.reconcileStackEnv(workspaceID, st.ID, desired.Stack)
		if changed {
			stackEnvChanged(ctx, st.ID)
		}
		return err
	case declarative.ActionDelete:
		st, err := s.findStack(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		return s.stacks.Delete(ctx, workspaceID, st.ID, false)
	}
	return nil
}

func (s *Service) applySecret(workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	switch ch.Action {
	case declarative.ActionCreate:
		spec := desired.Secret
		value := spec.Value
		if spec.Generate {
			value = declarative.GenerateSecret(*spec)
		}
		_, err := s.secrets.Create(workspaceID, ch.Name, value, "", nil)
		return err
	case declarative.ActionDelete:
		sec, err := s.findSecret(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		return s.secrets.Delete(workspaceID, sec.ID)
	}
	// Secret values are write-only and never diffed, so update is a no-op here.
	return nil
}

// Create and update share a path: an update may be adopting a plain Secret of the same name.
func (s *Service) applySealedSecret(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	if ch.Action == declarative.ActionDelete {
		sec, err := s.findSecret(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		return s.secrets.Delete(workspaceID, sec.ID)
	}
	if s.unsealer == nil {
		return errors.New("sealed secrets are not available on this instance")
	}
	spec := desired.SealedSecret
	value, ver, err := s.unsealer.Unseal(workspaceID, ch.Name, spec.Value)
	if err != nil {
		return fmt.Errorf("value: %w", err)
	}
	run := runStateOf(ctx)
	if run == nil {
		_, err = s.secrets.ApplySealed(workspaceID, ch.Name, value, spec.SealedFP, ver)
		return err
	}
	// Redeployed once at the end of the run, with the apps a changed stack env also needs.
	_, changed, err := s.secrets.ApplySealedDeferred(workspaceID, ch.Name, value, spec.SealedFP, ver)
	if changed {
		run.mu.Lock()
		run.rotatedSecrets[ch.Name] = true
		run.mu.Unlock()
	}
	return err
}

// Rendered here because delimiters differ per config and an unresolved reference must fail the apply.
func (s *Service) applyConfig(workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	if s.configs == nil {
		return fmt.Errorf("%w: %s", ErrUnsupportedKind, declarative.KindConfig)
	}
	switch ch.Action {
	case declarative.ActionCreate, declarative.ActionUpdate:
		spec := desired.Config
		if spec == nil {
			return fmt.Errorf("%w: config %q: spec is required", ErrInvalidManifest, ch.Name)
		}
		data, err := s.renderConfigData(workspaceID, ch.Name, spec)
		if err != nil {
			return fmt.Errorf("%w: config %q: %v", ErrInvalidManifest, ch.Name, err)
		}
		in := configsvc.Input{
			Name: ch.Name, Data: data, Mode: spec.Mode,
			Sensitive: spec.Sensitive, Delimiters: spec.Delimiters,
		}
		if ch.Action == declarative.ActionCreate {
			_, cerr := s.configs.Create(workspaceID, in, nil)
			return cerr
		}
		cfg, gerr := s.configs.GetByName(workspaceID, ch.Name)
		if gerr != nil {
			return gerr
		}
		_, uerr := s.configs.Update(workspaceID, cfg.ID, in, nil)
		return uerr
	case declarative.ActionDelete:
		cfg, err := s.configs.GetByName(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		return s.configs.Delete(workspaceID, cfg.ID)
	}
	return nil
}

// The rule is rendered so credentials can be vault references instead of literals in the repository.
func (s *Service) applyMiddleware(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	if s.middlewares == nil {
		return fmt.Errorf("%w: %s", ErrUnsupportedKind, declarative.KindMiddleware)
	}
	switch ch.Action {
	case declarative.ActionCreate, declarative.ActionUpdate:
		spec := desired.Middleware
		if spec == nil {
			return fmt.Errorf("%w: middleware %q: spec is required", ErrInvalidManifest, ch.Name)
		}
		rule, err := s.renderMiddlewareRule(workspaceID, spec)
		if err != nil {
			return fmt.Errorf("%w: middleware %q: %v", ErrInvalidManifest, ch.Name, err)
		}
		in := middlewaresvc.Input{Name: ch.Name, Type: spec.Type, Paths: spec.Paths, Rule: rule}
		if ch.Action == declarative.ActionCreate {
			_, cerr := s.middlewares.Create(ctx, workspaceID, in)
			return cerr
		}
		mw, gerr := s.middlewares.GetByName(workspaceID, ch.Name)
		if gerr != nil {
			return gerr
		}
		_, uerr := s.middlewares.Update(ctx, workspaceID, mw.ID, in)
		return uerr
	case declarative.ActionDelete:
		mw, err := s.middlewares.GetByName(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		return s.middlewares.Delete(ctx, workspaceID, mw.ID)
	}
	return nil
}

// Only strings are rendered; numbers, booleans and structure pass through.
func (s *Service) renderMiddlewareRule(workspaceID uint, spec *declarative.MiddlewareSpec) (map[string]any, error) {
	if len(spec.Rule) == 0 {
		return nil, nil
	}
	r := declarative.NewRenderer(declarative.RenderContext{
		Databases: s.databaseViews(workspaceID),
		Secrets:   s.secretViews(workspaceID),
		Apps:      s.appAliasViews(workspaceID),
	})
	out, err := renderRuleValues(r, "middleware", spec.Rule)
	if err != nil {
		return nil, err
	}
	return mwcatalog.DropRetired(spec.Type, out), nil
}

// Rules are free-form (the gateway catalogue owns the shape), so the walk can't be struct-driven.
func renderRuleValues(r *declarative.Renderer, scope string, in map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(in))
	for k, v := range in {
		rv, err := renderRuleValue(r, scope+"."+k, v)
		if err != nil {
			return nil, err
		}
		out[k] = rv
	}
	return out, nil
}

func renderRuleValue(r *declarative.Renderer, scope string, v any) (any, error) {
	switch t := v.(type) {
	case string:
		return r.RenderString(scope, t)
	case map[string]any:
		return renderRuleValues(r, scope, t)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			rv, err := renderRuleValue(r, fmt.Sprintf("%s[%d]", scope, i), t[i])
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	default:
		return v, nil
	}
}

// Honours the config's own delimiters so files using {{ }} are not mangled.
func (s *Service) renderConfigData(workspaceID uint, name string, spec *declarative.ConfigSpec) (map[string]string, error) {
	r := declarative.NewRenderer(declarative.RenderContext{
		Databases: s.databaseViews(workspaceID),
		Secrets:   s.secretViews(workspaceID),
		Apps:      s.appAliasViews(workspaceID),
	})
	return r.RenderFiles("config."+name, spec.Data, spec.Delimiters)
}

// Re-rendered strictly; Secret ranks below Registry, so a secret in the same bundle already exists.
func (s *Service) applyRegistry(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	switch ch.Action {
	case declarative.ActionCreate, declarative.ActionUpdate:
		spec := desired.Registry
		if spec == nil {
			return fmt.Errorf("%w: registry %q: spec is required", ErrInvalidManifest, ch.Name)
		}
		r := declarative.NewRenderer(declarative.RenderContext{Secrets: s.secretViews(workspaceID)})
		if err := s.renderRegistryPassword(r, ch.Name, spec); err != nil {
			return fmt.Errorf("%w: registry %q: %v", ErrInvalidManifest, ch.Name, err)
		}
		in := registry.Input{
			Name:     ch.Name,
			Server:   spec.Server,
			Username: spec.Username,
			Secret:   spec.Password,
			// Sanitized so a manifest can't spoof provenance; managed-by scopes a later prune.
			Metadata: tagSource(ctx, models.SetBuiltin(models.SanitizeUserMetadata(desired.Metadata.Labels),
				models.MetaManagedBy, managedBy(ctx),
			)),
			Annotations: desired.Metadata.Annotations,
		}
		if ch.Action == declarative.ActionCreate {
			if spec.Password == "" {
				return fmt.Errorf("%w: registry %q: password is required to create the credential (set spec.password, or create it once in the UI and drop the field)", ErrInvalidManifest, ch.Name)
			}
			_, err := s.registries.Create(workspaceID, in)
			return err
		}
		reg, err := s.registries.FindByName(workspaceID, ch.Name)
		if err != nil {
			return fmt.Errorf("registry %q: %w", ch.Name, err)
		}
		// An empty password leaves the stored one, so a token can be managed out-of-band.
		_, err = s.registries.Update(workspaceID, reg.ID, in)
		return err
	case declarative.ActionDelete:
		reg, err := s.registries.FindByName(workspaceID, ch.Name)
		if err != nil {
			return fmt.Errorf("registry %q: %w", ch.Name, err)
		}
		// The FK nulls dependent apps' RegistryID (ON DELETE SET NULL), so they fall back to anonymous pulls.
		return s.registries.Delete(workspaceID, reg.ID)
	}
	return nil
}

func (s *Service) applyRoute(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	spec := desired.Route
	switch ch.Action {
	case declarative.ActionCreate:
		app, err := s.findApp(workspaceID, spec.App)
		if err != nil {
			return fmt.Errorf("route %q: %w", ch.Name, err)
		}
		in, ierr := s.routeInput(workspaceID, ch.Name, app.ID, spec)
		if ierr != nil {
			return ierr
		}
		in.Metadata = tagSource(ctx, models.SetBuiltin(models.Metadata{}, models.MetaManagedBy, managedBy(ctx)))
		_, err = s.routes.Create(ctx, workspaceID, in)
		return err
	case declarative.ActionUpdate:
		rt, err := s.findRoute(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		app, err := s.findApp(workspaceID, spec.App)
		if err != nil {
			return fmt.Errorf("route %q: %w", ch.Name, err)
		}
		in, ierr := s.routeInput(workspaceID, ch.Name, app.ID, spec)
		if ierr != nil {
			return ierr
		}
		in.Metadata = tagSource(ctx, models.SetBuiltin(models.Metadata{}, models.MetaManagedBy, managedBy(ctx)))
		_, err = s.routes.Update(ctx, workspaceID, rt.ID, in)
		return err
	case declarative.ActionDelete:
		rt, err := s.findRoute(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		return s.routes.Delete(ctx, workspaceID, rt.ID)
	}
	return nil
}

func (s *Service) routeInput(workspaceID uint, name string, appID uint, spec *declarative.RouteSpec) (route.Input, error) {
	path := spec.Path
	if path == "" {
		path = "/"
	}
	exploit := spec.Security != nil && spec.Security.ExploitProtection
	mt := models.RouteMaintenance{}
	if spec.Maintenance != nil {
		mt = models.RouteMaintenance{
			Enabled: spec.Maintenance.Enabled, StatusCode: spec.Maintenance.StatusCode, Message: spec.Maintenance.Message,
		}
	}
	certID, err := s.resolveCertificate(workspaceID, name, spec)
	if err != nil {
		return route.Input{}, err
	}
	provider := spec.TLSProvider()
	return route.Input{
		Name: name, ApplicationID: appID, Hosts: spec.Hosts,
		Path: path, TargetPort: spec.Port.Number, TLSMode: tlsFromSpec(spec.TLSMode()),
		CertificateID: certID,
		// Always non-nil: clearing the field in the manifest must clear it on the route.
		TLSProvider: &provider,
		Middlewares: spec.Middlewares,
		// route.Update assigns these unconditionally, so omitting them would erase what is stored.
		Rewrite:           spec.Rewrite,
		Methods:           spec.Methods,
		AdvancedConfig:    spec.AdvancedConfig,
		ExploitProtection: &exploit,
		// Always non-nil: dropping the maintenance block must resume traffic.
		Maintenance: &mt,
	}, nil
}

// An unknown name fails the change rather than creating a custom-TLS route that serves no TLS.
func (s *Service) resolveCertificate(workspaceID uint, routeName string, spec *declarative.RouteSpec) (*uint, error) {
	cert := spec.TLSCertificate()
	if cert == "" {
		return nil, nil
	}
	if s.certificates == nil {
		return nil, fmt.Errorf("%w: route %q: certificates are not available on this install", ErrUnsupportedKind, routeName)
	}
	list, err := s.certificates.List(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Name == cert {
			id := list[i].ID
			return &id, nil
		}
	}
	return nil, fmt.Errorf("%w: route %q references certificate %q, which does not exist in this workspace — "+
		"import it first (certificates hold a private key, so they are not declarable in a manifest)",
		ErrInvalidManifest, routeName, cert)
}

// Maintenance is emitted only while parked so exports match a manifest that omits it.
func routeSpecOf(r models.Route, app, path string, certNameByID map[uint]string) *declarative.RouteSpec {
	cert := ""
	// A deleted certificate leaves the FK nulled, so emit nothing rather than invent a name.
	if r.CertificateID != nil {
		cert = certNameByID[*r.CertificateID]
	}
	spec := &declarative.RouteSpec{
		Hosts: append([]string(nil), r.Hosts...), App: app, Port: declarative.RoutePort{Number: r.TargetPort},
		Path: path, TLS: declarative.RouteTLSOf(tlsToSpec(r.TLSMode), r.TLSProvider, cert),
		Middlewares:    append([]string(nil), r.Middlewares...),
		Rewrite:        r.Rewrite,
		Methods:        append([]string(nil), r.Methods...),
		AdvancedConfig: r.AdvancedConfig,
	}
	if r.ExploitProtection {
		spec.Security = &declarative.RouteSecuritySpec{ExploitProtection: true}
	}
	if r.Maintenance.Enabled {
		spec.Maintenance = &declarative.RouteMaintenanceSpec{
			Enabled: true, StatusCode: r.Maintenance.StatusCode, Message: r.Maintenance.Message,
		}
	}
	return spec
}

func (s *Service) applyVolume(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource, set *declarative.ResourceSet) error {
	switch ch.Action {
	case declarative.ActionCreate:
		meta := tagSource(ctx, models.SetBuiltin(models.Metadata{}, models.MetaManagedBy, managedBy(ctx)))
		target, err := s.placeVolume(ctx, workspaceID, ch, desired.Volume, set)
		if err != nil {
			return err
		}
		size, err := desired.Volume.SizeBytes()
		if err != nil {
			return fmt.Errorf("%w: volume %q: %s", ErrInvalidManifest, ch.Name, err)
		}
		_, err = s.storage.CreateOn(ctx, workspaceID, target.ServerID, storage.CreateInput{
			Name:         ch.Name,
			SizeBytes:    size,
			StorageClass: desired.Volume.StorageClass,
			Meta:         meta,
			Annotations:  desired.Metadata.Annotations,
		})
		return err
	case declarative.ActionUpdate:
		if err := refuseImmutable(ch); err != nil {
			return err
		}
		return s.resizeVolume(workspaceID, ch)
	case declarative.ActionDelete:
		v, err := s.findVolume(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		return s.storage.Delete(ctx, v)
	}
	return nil
}

func (s *Service) applyDatabase(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	switch ch.Action {
	case declarative.ActionCreate:
		spec := desired.Database
		meta := tagSource(ctx, models.SetBuiltin(models.Metadata{}, models.MetaManagedBy, managedBy(ctx)))
		// Stamped with the manifest name so databaseViews resolves this exact database; DDL waits for a new instance.
		target, err := s.place(ctx, workspaceID, ch, spec.Location(), false)
		if err != nil {
			return err
		}
		_, db, _, _, err := s.dbs.ResolveDependency(
			ctx, workspaceID, target.ServerID, 0, ch.Name, ch.Name,
			models.DBEngine(spec.Engine), spec.Version, database.Placement(spec.Instance), databaseResources(spec), meta,
		)
		if err != nil {
			return err
		}
		if spec.ReadOnlyUser != nil && *spec.ReadOnlyUser {
			if db == nil {
				return fmt.Errorf("database %q: readOnlyUser needs a logical PostgreSQL database", ch.Name)
			}
			return s.dbs.EnableReadOnly(ctx, db)
		}
		return nil
	case declarative.ActionDelete:
		// Tear down only what we own: the logical database when shared, the instance only when dedicated.
		if db, inst, ok := s.dbs.FindDatabaseByDeclName(workspaceID, ch.Name); ok {
			remaining, _ := s.dbs.ListDatabases(workspaceID, inst.ID)
			shared := len(remaining) > 1 || inst.Name != ch.Name
			if shared {
				return s.dbs.DeleteDatabase(ctx, workspaceID, db.ID)
			}
			return s.deleteInstance(ctx, inst)
		}
		// Dedicated instances (Redis, libSQL) or legacy rows created before logical databases were tagged.
		inst, err := s.findInstance(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		return s.deleteInstance(ctx, inst)
	case declarative.ActionUpdate:
		if err := refuseMove(ch); err != nil {
			return err
		}
		resize := false
		for _, f := range ch.Fields {
			if strings.HasPrefix(f.Field, "resources.") {
				resize = true
				continue
			}
			if f.Field == "readOnlyUser" {
				if err := s.setReadOnly(ctx, workspaceID, ch.Name, f.To == "true"); err != nil {
					return err
				}
				continue
			}
			// Engine/version changes are not converged in place: the data would have to be migrated.
			if f.Field == "version" {
				return fmt.Errorf("database %q runs %s %s, but the manifest declares %s; its data is not migrated automatically. "+
					"Set version: %s, or delete the database to re-create it on a %s instance",
					ch.Name, desired.Database.Engine, f.From, f.To, f.From, f.To)
			}
			return fmt.Errorf("database %q: in-place engine/version change is not supported", ch.Name)
		}
		if !resize {
			return nil
		}
		return s.resizeDatabase(ctx, workspaceID, ch.Name, desired.Database)
	}
	return nil
}

// setReadOnly adds or removes a manifest database's read-only user in place.
func (s *Service) setReadOnly(ctx context.Context, workspaceID uint, name string, on bool) error {
	db, _, ok := s.dbs.FindDatabaseByDeclName(workspaceID, name)
	if !ok {
		return fmt.Errorf("database %q: readOnlyUser needs a logical PostgreSQL database", name)
	}
	if on {
		return s.dbs.EnableReadOnly(ctx, db)
	}
	return s.dbs.DisableReadOnly(ctx, db)
}

// A shared instance's limits are not one manifest's to set.
func (s *Service) resizeDatabase(ctx context.Context, workspaceID uint, name string, spec *declarative.DatabaseSpec) error {
	inst, err := s.ownedInstance(workspaceID, name)
	if err != nil {
		return err
	}
	res := databaseResources(spec)
	// A named size carries both limits; with plain numbers, one left unstated keeps the instance's.
	if res.Size == "" {
		if spec == nil || spec.Resources == nil || spec.Resources.Memory == "" {
			res.MemoryBytes = inst.MemoryBytes
		}
		if spec == nil || spec.Resources == nil || spec.Resources.CPU == "" {
			res.NanoCPUs = inst.NanoCPUs
		}
	}
	if _, err := s.dbs.Resize(ctx, inst, res); err != nil {
		return fmt.Errorf("database %q: %w", name, err)
	}
	return nil
}

func (s *Service) ownedInstance(workspaceID uint, name string) (*models.DatabaseInstance, error) {
	if _, inst, ok := s.dbs.FindDatabaseByDeclName(workspaceID, name); ok {
		remaining, _ := s.dbs.ListDatabases(workspaceID, inst.ID)
		if len(remaining) > 1 || inst.Name != name {
			return nil, fmt.Errorf("%w: database %q shares instance %q with other databases, so its resources are that "+
				"instance's. Change them on the instance", ErrInvalidManifest, name, inst.Name)
		}
		return inst, nil
	}
	return s.findInstance(workspaceID, name)
}

// Prune is automated, so stop first or the UI's stop-before-delete guard blocks teardown.
func (s *Service) deleteInstance(ctx context.Context, inst *models.DatabaseInstance) error {
	if inst.Status == models.DBStatusRunning {
		if err := s.dbs.Stop(ctx, inst); err != nil {
			return err
		}
	}
	return s.dbs.Delete(ctx, inst)
}

func (s *Service) applyApplication(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource, set *declarative.ResourceSet) error {
	spec := desired.Application
	refs := refsOf(spec).withStackRefs(spec, set)
	// Databases run before applications, so their refs are live now; the plan-time render was lenient.
	if err := s.renderAppEnv(workspaceID, spec, set); err != nil {
		return fmt.Errorf("%w: application %q: %v", ErrInvalidManifest, ch.Name, err)
	}
	// A cross-location or swarmless cross-node reference renders a hostname that never resolves.
	if err := s.checkAppRefPlacement(workspaceID, ch.Name, refs, nil); err != nil {
		return err
	}
	if err := s.refuseWeakerThanProfile(workspaceID, ch.Name, spec); err != nil {
		return err
	}
	// Resolve before creating so a typo fails the change instead of deploying an app that can't pull.
	regID, err := s.resolveRegistry(workspaceID, ch.Name, spec)
	if err != nil {
		return err
	}
	stackID, err := s.resolveStack(workspaceID, ch.Name, spec)
	if err != nil {
		return err
	}
	switch ch.Action {
	case declarative.ActionCreate:
		in := s.createInput(ctx, desired.Metadata, spec)
		in.RegistryID = regID
		in.StackID = stackID
		target, err := s.placeApp(ctx, workspaceID, ch.Name, spec, refs)
		if err != nil {
			return err
		}
		in.ServerID = target.ServerID
		if s.placer != nil {
			self := s.placementOf(target.ServerID, "", target.ClusterID)
			if err := s.checkAppRefPlacement(workspaceID, ch.Name, refs, &self); err != nil {
				return err
			}
		}
		app, err := s.apps.Create(workspaceID, in)
		if err != nil {
			return err
		}
		if err := s.reconcileEnv(app.ID, spec); err != nil {
			return err
		}
		// Before deploy so host-port bindings publish in the first container.
		if err := s.reconcileExposure(ctx, workspaceID, app, spec); err != nil {
			return err
		}
		// Before the first deploy so the container mounts them from the start.
		if err := s.reconcileMounts(workspaceID, app, spec); err != nil {
			return err
		}
		if _, err = s.apps.Deploy(app, nil, "", ""); err == nil {
			appDeployed(ctx, app.ID)
		}
		return err
	case declarative.ActionUpdate:
		if err := refuseMove(ch); err != nil {
			return err
		}
		app, err := s.findApp(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		app.Image = spec.Image
		app.Tag = spec.Tag
		app.Command = spec.Command
		// Both directions are written because switching between pulling and building is a real manifest change.
		if src := spec.Source; src != nil {
			app.SourceType = models.AppSourceGit
			app.Image, app.Tag = "", ""
			app.GitRepo, app.GitRef = src.Git, src.Ref
			app.BuildMethod = models.AppBuildMethod(src.BuildMethod)
			app.Builder, app.Buildpacks, app.BuildEnv = src.Builder, src.Buildpacks, src.BuildEnv
		} else if app.SourceType == models.AppSourceGit {
			app.SourceType = models.AppSourceImage
			app.GitRepo, app.GitRef, app.Builder, app.Buildpacks, app.BuildEnv = "", "", "", nil, nil
		}
		// Dropping spec.registry clears the credential instead of silently keeping the old one.
		app.RegistryID = regID
		if spec.Resources != nil {
			mb, _ := spec.Resources.MemoryBytes()
			nc, _ := spec.Resources.NanoCPUs()
			app.MemoryBytes, app.NanoCPUs = mb, nc
			app.GPUCount, app.GPUKind = spec.Resources.GPU, spec.Resources.GPUKind
		}
		app.Metadata = claim(ctx, models.SetBuiltin(
			models.MergeUserMetadata(app.Metadata, desired.Metadata.Labels),
			MetaDigest, spec.Digest,
		))
		app.Annotations = desired.Metadata.Annotations
		// The app service sanitizes reserved keys so a manifest can't spoof platform labels.
		app.ContainerLabels = spec.ContainerLabels
		// The app service refuses a run-as user that would escape a non-root mandate.
		app.RunAsUser = spec.RunAsUser()
		// Absent means "no grants" and "no hardening", not "unchanged", or GitOps could never revoke.
		app.AddCapabilities = spec.AddCapabilities()
		app.DropCapabilities = spec.DropCapabilities()
		app.Devices = spec.Devices()
		app.ReadOnlyRootFilesystem = spec.ReadOnlyRootFilesystem()
		app.NoNewPrivileges = spec.NoNewPrivileges()
		// A manifest silent on strategy must not reset the console setting to rolling.
		if s := spec.Strategy(); s != "" {
			app.DeployStrategy = models.DeployStrategy(s)
		}
		applyHealthcheck(app, spec.Healthcheck)
		if rt := spec.Runtime(); rt != "" {
			app.RuntimeKind = models.RuntimeKind(rt)
		}
		if n := spec.Replicas(); n > 0 {
			app.Replicas = n
		}
		if c := spec.Constraints(); c != nil {
			app.PlacementConstraints = c
		}
		if spec.Update() != nil {
			app.UpdateConfig = updateConfigOf(spec)
		}
		// A manifest silent on stack leaves a console-set membership alone.
		if stackID != nil {
			app.StackID = stackID
		}
		app.Stack = nil // a preloaded association would be saved back over StackID
		if err := s.apps.Update(app); err != nil {
			return err
		}
		if err := s.reconcileEnv(app.ID, spec); err != nil {
			return err
		}
		// Before exposure, which binds and routes only ports the app declares.
		if len(spec.Ports) > 0 {
			if err := s.apps.ReconcilePorts(app, portSpecsOf(spec)); err != nil {
				return fmt.Errorf("ports: %w", err)
			}
		}
		if err := s.reconcileExposure(ctx, workspaceID, app, spec); err != nil {
			return err
		}
		if err := s.reconcileMounts(workspaceID, app, spec); err != nil {
			return err
		}
		if _, err = s.apps.Deploy(app, nil, "", ""); err == nil {
			appDeployed(ctx, app.ID)
		}
		return err
	case declarative.ActionDelete:
		app, err := s.findApp(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		// Prune is automated, so stop first or the stop-before-delete guard blocks teardown.
		if s.apps.LiveStatus(ctx, app).Running {
			if err := s.apps.Stop(ctx, app); err != nil {
				return err
			}
		}
		return s.apps.Delete(ctx, app)
	}
	return nil
}

// Validation requires the Stack in the bundle and it ranks before applications, so it exists by now.
func (s *Service) resolveStack(workspaceID uint, appName string, spec *declarative.ApplicationSpec) (*uint, error) {
	if spec == nil || spec.Stack == "" {
		return nil, nil
	}
	st, err := s.findStack(workspaceID, spec.Stack)
	if err != nil {
		return nil, fmt.Errorf("%w: application %q: %v", ErrInvalidManifest, appName, err)
	}
	return &st.ID, nil
}

// Resolved against the live workspace; an unknown name is a manifest error, not an opaque deploy failure.
func (s *Service) resolveRegistry(workspaceID uint, appName string, spec *declarative.ApplicationSpec) (*uint, error) {
	// A pruned resource has no desired spec, so nil is normal here.
	if spec == nil || spec.Registry == "" {
		return nil, nil
	}
	reg, err := s.registries.FindByName(workspaceID, spec.Registry)
	if err != nil {
		return nil, fmt.Errorf("%w: application %q: unknown registry %q (declare it as a Registry resource, or create the credential in the workspace first)",
			ErrInvalidManifest, appName, spec.Registry)
	}
	return &reg.ID, nil
}

func (s *Service) reconcileExposure(ctx context.Context, workspaceID uint, app *models.Application, spec *declarative.ApplicationSpec) error {
	// A label claimed by another app is ignored in favour of a generated one rather than failing the apply.
	if spec.ExternalLabel != "" && app.ExternalLabel != spec.ExternalLabel {
		taken, err := s.apps.ExternalLabelTaken(spec.ExternalLabel, app.ID)
		if err != nil {
			return err
		}
		if taken {
			logger.Warn("externalLabel already in use; generating a new one",
				"app", app.ID, "requested", spec.ExternalLabel)
		} else {
			app.ExternalLabel = spec.ExternalLabel
			if err := s.apps.Update(app); err != nil {
				return err
			}
		}
	}
	if s.bindings != nil {
		if err := s.reconcileBindings(workspaceID, app.ID, spec); err != nil {
			return err
		}
	}
	// SetExternalAccess is idempotent and tears down ports no longer flagged.
	if s.extAccess {
		var ext []int
		for _, p := range spec.Ports {
			if p.ExternalAccess {
				ext = append(ext, p.Container)
			}
		}
		if _, err := s.routes.SetExternalAccess(ctx, workspaceID, app.ID, ext); err != nil {
			return fmt.Errorf("external access: %w", err)
		}
	}
	return nil
}

// Presence-based so an auto-allocated host port isn't churned.
func (s *Service) reconcileBindings(workspaceID, appID uint, spec *declarative.ApplicationSpec) error {
	// Keyed by port and protocol: DNS-style services publish one port over both udp and tcp.
	type key struct {
		port  int
		proto string
	}
	desired := map[key]declarative.PortSpec{}
	for _, p := range spec.Ports {
		if p.Publish || p.HostPort > 0 {
			desired[key{p.Container, bindingProto(p.Protocol)}] = p
		}
	}
	existing, err := s.bindings.ListByApp(workspaceID, appID)
	if err != nil {
		return err
	}
	have := map[key]*models.PortBinding{}
	for i := range existing {
		have[key{existing[i].ContainerPort, bindingProto(existing[i].Protocol)}] = &existing[i]
	}
	for k, p := range desired {
		if _, ok := have[k]; ok {
			continue // already bound — keep the live host port
		}
		if _, err := s.bindings.Request(workspaceID, 0, portbinding.RequestInput{
			ApplicationID: appID, ContainerPort: k.port, Protocol: k.proto, HostPort: p.HostPort,
		}); err != nil {
			return fmt.Errorf("bind port %d/%s: %w", k.port, k.proto, err)
		}
	}
	for k, b := range have {
		if _, ok := desired[k]; !ok {
			if err := s.bindings.Cancel(workspaceID, b.ID); err != nil {
				return fmt.Errorf("unbind port %d/%s: %w", k.port, k.proto, err)
			}
		}
	}
	return nil
}

func bindingProto(p string) string {
	if p == "udp" {
		return "udp"
	}
	return "tcp"
}

func (s *Service) exposedPorts(workspaceID, appID uint) (ext, pub map[int]bool) {
	ext, pub = map[int]bool{}, map[int]bool{}
	if routes, err := s.routes.ListByApp(workspaceID, appID); err == nil {
		for i := range routes {
			if routes[i].Generated {
				ext[routes[i].TargetPort] = true
			}
		}
	}
	if s.bindings != nil {
		if bs, err := s.bindings.ListByApp(workspaceID, appID); err == nil {
			for i := range bs {
				pub[bs[i].ContainerPort] = true
			}
		}
	}
	return ext, pub
}

func (s *Service) createInput(ctx context.Context, m declarative.Meta, spec *declarative.ApplicationSpec) application.CreateInput {
	// Reserved keys are stripped so a manifest can't spoof provenance; annotations are stored verbatim.
	md := tagSource(ctx, models.SetBuiltin(models.SanitizeUserMetadata(m.Labels),
		models.MetaManagedBy, managedBy(ctx),
		MetaDigest, spec.Digest,
	))
	in := application.CreateInput{
		DisplayName:     m.Name,
		Handle:          m.Name,
		SourceType:      models.AppSourceImage,
		Image:           spec.Image,
		Tag:             spec.Tag,
		Command:         spec.Command,
		Metadata:        md,
		Annotations:     m.Annotations,
		ContainerLabels: spec.ContainerLabels,   // sanitized in the app service Create
		RunAsUser:       spec.RunAsUser(),       // validated in the app service Create
		AddCapabilities: spec.AddCapabilities(), // allow-listed + gated in the app service
		Devices:         spec.Devices(),
		DeployStrategy:  models.DeployStrategy(spec.Strategy()),
		Healthcheck:     healthcheckInput(spec.Healthcheck),

		DropCapabilities:       spec.DropCapabilities(),
		ReadOnlyRootFilesystem: spec.ReadOnlyRootFilesystem(),
		NoNewPrivileges:        spec.NoNewPrivileges(),

		RuntimeKind:          models.RuntimeKind(spec.Runtime()),
		Replicas:             spec.Replicas(),
		PlacementConstraints: spec.Constraints(),
		UpdateConfig:         updateConfigOf(spec),
	}
	// Validation already refuses a manifest declaring both source and image.
	if src := spec.Source; src != nil {
		in.SourceType = models.AppSourceGit
		in.Image, in.Tag = "", ""
		in.GitRepo, in.GitRef = src.Git, src.Ref
		in.BuildMethod = models.AppBuildMethod(src.BuildMethod)
		in.Builder, in.Buildpacks, in.BuildEnv = src.Builder, src.Buildpacks, src.BuildEnv
	}
	if spec.Resources != nil {
		in.MemoryBytes, _ = spec.Resources.MemoryBytes()
		in.NanoCPUs, _ = spec.Resources.NanoCPUs()
		in.GPUCount, in.GPUKind = spec.Resources.GPU, spec.Resources.GPUKind
	}
	in.Ports = portSpecsOf(spec)
	if len(spec.Ports) > 0 {
		in.Port = spec.Ports[0].Container
	}
	return in
}

func portSpecsOf(spec *declarative.ApplicationSpec) []application.PortSpec {
	out := make([]application.PortSpec, 0, len(spec.Ports))
	for _, p := range spec.Ports {
		out = append(out, application.PortSpec{ContainerPort: p.Container, Scheme: p.Scheme, Protocol: p.Protocol, Name: p.Name})
	}
	return out
}

func updateConfigOf(spec *declarative.ApplicationSpec) *models.ServiceUpdateConfig {
	u := spec.Update()
	if u == nil {
		return nil
	}
	return &models.ServiceUpdateConfig{Parallelism: u.Parallelism, DelaySeconds: u.DelaySeconds}
}

// The container would never run as described, so it is refused like runAsUser: root.
func (s *Service) refuseWeakerThanProfile(workspaceID uint, name string, spec *declarative.ApplicationSpec) error {
	if spec == nil || spec.Security == nil {
		return nil
	}
	sec := spec.Security
	if sec.NoNewPrivileges == nil || *sec.NoNewPrivileges || !s.apps.EnforcesNoNewPrivileges(workspaceID) {
		return nil
	}
	return fmt.Errorf("%w: application %q sets security.noNewPrivileges: false, but this workspace's restricted security "+
		"profile always sets it. Remove the field", ErrInvalidManifest, name)
}

// reconcileEnv sets desired env vars and removes ones no longer declared.
func (s *Service) reconcileEnv(appID uint, spec *declarative.ApplicationSpec) error {
	secret := map[string]bool{}
	for _, k := range spec.SecretEnv {
		secret[k] = true
	}
	for k, v := range spec.Env {
		if err := s.apps.SetEnvVar(appID, k, v, secret[k]); err != nil {
			return err
		}
	}
	current, err := s.apps.ListEnvVars(appID)
	if err != nil {
		return nil // best effort: keep what we set
	}
	for _, ev := range current {
		if _, keep := spec.Env[ev.Key]; !keep {
			_ = s.apps.DeleteEnvVar(appID, ev.Key)
		}
	}
	return nil
}

// renderStackEnv resolves a stack's shared env templates at execution time, strictly, like renderAppEnv.
func (s *Service) renderStackEnv(workspaceID uint, spec *declarative.StackSpec, set *declarative.ResourceSet) error {
	if spec == nil || len(spec.Env) == 0 {
		return nil
	}
	r := declarative.NewRenderer(declarative.RenderContext{
		Databases: s.databaseViews(workspaceID),
		Secrets:   s.secretViews(workspaceID),
		Apps:      s.appViews(workspaceID, set),
	})
	rendered, err := r.RenderEnv("stack", spec.Env)
	if err != nil {
		return err
	}
	spec.Env = rendered
	spec.SecretEnv = withKeys(spec.SecretEnv, r.CredentialKeys())
	return nil
}

// A manifest silent on env leaves the stack alone; unchanged values aren't rewritten, as writes trigger redeploys.
func (s *Service) reconcileStackEnv(workspaceID, stackID uint, spec *declarative.StackSpec) (bool, error) {
	if spec == nil || spec.Env == nil {
		return false, nil
	}
	secret := map[string]bool{}
	for _, k := range spec.SecretEnv {
		secret[k] = true
	}
	current, err := s.stacks.ListEnvVars(workspaceID, stackID)
	if err != nil {
		return false, fmt.Errorf("list stack env: %w", err)
	}
	changed := false
	live := make(map[string]models.StackEnvVar, len(current))
	for _, ev := range current {
		live[ev.Key] = ev
	}
	for k, v := range spec.Env {
		if ev, ok := live[k]; ok && ev.IsSecret == secret[k] {
			value := ev.Value
			if ev.IsSecret {
				// Listed masked; compare against the stored plaintext.
				if value, err = s.stacks.RevealEnvVar(workspaceID, stackID, k); err != nil {
					value = ""
				}
			}
			if value == v {
				continue
			}
		}
		if _, err := s.stacks.SetEnvVar(workspaceID, stackID, k, v, secret[k]); err != nil {
			return changed, fmt.Errorf("set stack env %s: %w", k, err)
		}
		changed = true
	}
	for _, ev := range current {
		if _, keep := spec.Env[ev.Key]; !keep {
			if _, err := s.stacks.DeleteEnvVar(workspaceID, stackID, ev.Key); err != nil {
				return changed, fmt.Errorf("unset stack env %s: %w", ev.Key, err)
			}
			changed = true
		}
	}
	return changed, nil
}

func toRedeploy(apps []models.Application, deployed, seen map[uint]bool) []*models.Application {
	out := make([]*models.Application, 0, len(apps))
	for i := range apps {
		if !deployed[apps[i].ID] && !seen[apps[i].ID] {
			seen[apps[i].ID] = true
			out = append(out, &apps[i])
		}
	}
	return out
}

// Stack env and vault secrets are injected at deploy, so each app they reach is redeployed once; apps this run deployed already have the new values.
func (s *Service) redeployChanged(workspaceID uint, run *runState) []Failure {
	var out []Failure
	seen := map[uint]bool{}
	redeploy := func(apps []models.Application, reason string) {
		for _, app := range toRedeploy(apps, run.deployedApps, seen) {
			// Leaves a never-deployed app alone and a stopped one flagged.
			if _, err := s.apps.AutoRedeploy(app); err != nil {
				out = append(out, Failure{
					Kind: string(declarative.KindApplication), Name: app.Name, Action: "redeploy",
					Error: fmt.Sprintf("%s: %v", reason, err),
				})
			}
		}
	}
	for stackID := range run.changedStacks {
		if st, err := s.stacks.Get(workspaceID, stackID); err == nil {
			redeploy(st.Apps, fmt.Sprintf("stack %q env changed", st.Name))
		}
	}
	for name := range run.rotatedSecrets {
		apps, err := s.apps.AppsReferencingSecret(workspaceID, name)
		if err != nil {
			out = append(out, Failure{Kind: string(declarative.KindSealedSecret), Name: name, Action: "redeploy", Error: err.Error()})
			continue
		}
		redeploy(apps, fmt.Sprintf("secret %q changed", name))
	}
	return out
}

// Secret values are blanked because the diff masks them, comparing only presence and secrecy.
func (s *Service) stackEnvOf(workspaceID, stackID uint) (map[string]string, []string) {
	vars, err := s.stacks.ListEnvVars(workspaceID, stackID)
	if err != nil || len(vars) == 0 {
		return nil, nil
	}
	env := make(map[string]string, len(vars))
	var secretEnv []string
	for _, v := range vars {
		if v.IsSecret {
			secretEnv = append(secretEnv, v.Key)
			env[v.Key] = ""
			continue
		}
		env[v.Key] = v.Value
	}
	return env, secretEnv
}

// Host-preset binds aren't manifest-expressible and are left untouched.
func (s *Service) reconcileMounts(workspaceID uint, app *models.Application, spec *declarative.ApplicationSpec) error {
	keep := make(map[uint]bool, len(spec.Mounts))
	// The same config may be mounted once per file it projects.
	type configMountKey struct {
		id  uint
		key string
	}
	keepConfig := map[configMountKey]bool{}

	for _, mt := range spec.Mounts {
		// A mount declares a volume or a config, never both (enforced by validation).
		if mt.Config != "" {
			cfg, err := s.findConfig(workspaceID, mt.Config)
			if err != nil {
				return fmt.Errorf("%w: application %q: %v", ErrInvalidManifest, app.Name, err)
			}
			if err := s.apps.AttachConfig(app, cfg.ID, mt.Key, mt.Path, mt.Mode); err != nil {
				return fmt.Errorf("mount config %q on %q: %w", mt.Config, app.Name, err)
			}
			keepConfig[configMountKey{cfg.ID, mt.Key}] = true
			continue
		}
		vol, err := s.findVolume(workspaceID, mt.Volume)
		if err != nil {
			return fmt.Errorf("%w: application %q: %v", ErrInvalidManifest, app.Name, err)
		}
		if err := s.apps.AttachVolume(app, vol.ID, mt.Path); err != nil {
			return fmt.Errorf("attach volume %q to %q: %w", mt.Volume, app.Name, err)
		}
		keep[vol.ID] = true
	}
	// Detach managed volume mounts no longer declared (skip host-preset binds).
	var stale []uint
	for _, m := range app.Mounts {
		if m.VolumeID != 0 && !keep[m.VolumeID] {
			stale = append(stale, m.VolumeID)
		}
	}
	for _, id := range stale {
		if err := s.apps.DetachVolume(app, id); err != nil {
			return fmt.Errorf("detach volume from %q: %w", app.Name, err)
		}
	}
	// Same for config mounts the manifest no longer declares.
	var staleConfigs []configMountKey
	for _, m := range app.Mounts {
		if m.ConfigID != 0 && !keepConfig[configMountKey{m.ConfigID, m.ConfigKey}] {
			staleConfigs = append(staleConfigs, configMountKey{m.ConfigID, m.ConfigKey})
		}
	}
	for _, k := range staleConfigs {
		if err := s.apps.DetachConfig(app, k.id, k.key); err != nil {
			return fmt.Errorf("remove config mount from %q: %w", app.Name, err)
		}
	}
	return nil
}

// findConfig resolves a config by its manifest name within the workspace.
func (s *Service) findConfig(workspaceID uint, name string) (*models.Config, error) {
	if s.configs == nil {
		return nil, fmt.Errorf("config %q not found", name)
	}
	cfg, err := s.configs.GetByName(workspaceID, name)
	if err != nil {
		return nil, fmt.Errorf("config %q not found", name)
	}
	return cfg, nil
}

func (s *Service) findApp(workspaceID uint, slug string) (*models.Application, error) {
	if s.apps == nil {
		return nil, fmt.Errorf("application %q not found", slug)
	}
	apps, err := s.apps.List(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range apps {
		if apps[i].Name == slug || strings.EqualFold(apps[i].Name, slug) {
			return s.apps.Get(workspaceID, apps[i].ID)
		}
	}
	return nil, fmt.Errorf("application %q not found", slug)
}

func (s *Service) findVolume(workspaceID uint, name string) (*models.Volume, error) {
	if s.storage == nil {
		return nil, fmt.Errorf("volume %q not found", name)
	}
	vols, err := s.storage.List(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range vols {
		if vols[i].Name == name {
			return &vols[i], nil
		}
	}
	return nil, fmt.Errorf("volume %q not found", name)
}

func (s *Service) findInstance(workspaceID uint, name string) (*models.DatabaseInstance, error) {
	instances, err := s.dbs.List(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range instances {
		if instances[i].Name == name {
			return &instances[i], nil
		}
	}
	return nil, fmt.Errorf("database %q not found", name)
}

func (s *Service) findStack(workspaceID uint, name string) (*models.Stack, error) {
	if s.stacks == nil {
		return nil, fmt.Errorf("stack %q not found", name)
	}
	stacks, err := s.stacks.List(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range stacks {
		if stacks[i].Name == name {
			return &stacks[i], nil
		}
	}
	return nil, fmt.Errorf("stack %q not found", name)
}

func (s *Service) findSecret(workspaceID uint, name string) (*models.Secret, error) {
	secrets, err := s.secrets.List(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range secrets {
		if secrets[i].Name == name {
			return &secrets[i], nil
		}
	}
	return nil, fmt.Errorf("secret %q not found", name)
}

func (s *Service) findRoute(workspaceID uint, name string) (*models.Route, error) {
	routes, err := s.routes.List(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range routes {
		if routes[i].Name == name {
			return &routes[i], nil
		}
	}
	return nil, fmt.Errorf("route %q not found", name)
}

func securitySpecOf(app *models.Application) *declarative.SecuritySpec {
	sec := &declarative.SecuritySpec{RunAsUser: app.RunAsUser, ReadOnlyRootFilesystem: app.ReadOnlyRootFilesystem}
	if app.NoNewPrivileges {
		on := true
		sec.NoNewPrivileges = &on
	}
	if len(app.AddCapabilities) > 0 || len(app.DropCapabilities) > 0 {
		sec.Capabilities = &declarative.CapabilitiesSpec{Add: app.AddCapabilities, Drop: app.DropCapabilities}
	}
	if len(app.Devices) > 0 {
		sec.Devices = app.Devices
	}
	if sec.RunAsUser == "" && !sec.ReadOnlyRootFilesystem && sec.NoNewPrivileges == nil && sec.Capabilities == nil && sec.Devices == nil {
		return nil
	}
	return sec
}

// healthcheckSpecOf states every field so a manifest diffs against the live value; export trims defaults.
func healthcheckSpecOf(app *models.Application) *declarative.HealthcheckSpec {
	t := app.HealthcheckType
	if t == "" {
		t = models.HealthcheckImage
	}
	return &declarative.HealthcheckSpec{
		Type: string(t), Path: app.HealthcheckHTTPPath, Port: app.HealthcheckPort, Command: app.HealthcheckCommand,
		IntervalSeconds: app.HealthcheckIntervalSeconds, TimeoutSeconds: app.HealthcheckTimeoutSeconds,
		Retries: app.HealthcheckRetries, StartPeriodSeconds: app.HealthcheckStartPeriodSeconds,
	}
}

// healthcheckInput is a new app's probe from its manifest; nil when the manifest states none.
func healthcheckInput(hc *declarative.HealthcheckSpec) *application.Healthcheck {
	if hc == nil {
		return nil
	}
	return &application.Healthcheck{
		Type: models.HealthcheckType(hc.Type), Path: hc.Path, Port: hc.Port, Command: hc.Command,
		IntervalSeconds: hc.IntervalSeconds, TimeoutSeconds: hc.TimeoutSeconds,
		Retries: hc.Retries, StartPeriodSeconds: hc.StartPeriodSeconds,
	}
}

// Only stated fields are written so console-set timing survives a partial manifest.
func applyHealthcheck(app *models.Application, hc *declarative.HealthcheckSpec) {
	if hc == nil {
		return
	}
	app.HealthcheckType = models.HealthcheckType(hc.Type)
	if hc.Path != "" {
		app.HealthcheckHTTPPath = hc.Path
	}
	if hc.Port > 0 {
		app.HealthcheckPort = hc.Port
	}
	if hc.Command != "" {
		app.HealthcheckCommand = hc.Command
	}
	if hc.IntervalSeconds > 0 {
		app.HealthcheckIntervalSeconds = hc.IntervalSeconds
	}
	if hc.TimeoutSeconds > 0 {
		app.HealthcheckTimeoutSeconds = hc.TimeoutSeconds
	}
	if hc.Retries > 0 {
		app.HealthcheckRetries = hc.Retries
	}
	if hc.StartPeriodSeconds > 0 {
		app.HealthcheckStartPeriodSeconds = hc.StartPeriodSeconds
	}
}

func trimDefault(v, def int) int {
	if v == def {
		return 0
	}
	return v
}

// Always states runtime and strategy so a manifest naming the default still diffs; export trims them.
func deploymentSpecOf(app *models.Application) *declarative.DeploymentSpec {
	d := &declarative.DeploymentSpec{Runtime: string(models.RuntimeContainer), Strategy: string(models.DeployRolling)}
	if app.DeployStrategy != "" {
		d.Strategy = string(app.DeployStrategy)
	}
	if app.RuntimeKind == models.RuntimeService {
		d.Runtime = string(models.RuntimeService)
		d.Replicas = max(app.Replicas, 1)
		if u := app.UpdateConfig; u != nil {
			d.Update = &declarative.UpdateSpec{Parallelism: u.Parallelism, DelaySeconds: u.DelaySeconds}
		}
	}
	return d
}
