// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apply is the imperative, one-shot sibling of GitOps. It turns a bundle
// of miabi.io/v1 manifests into a plan (dry run) or converges the workspace
// to them, by diffing desired state against a live snapshot and driving the
// existing application/storage/database services. GitOps reuses this same
// engine, so "apply once" and "stay in sync" share one code path.
package apply

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/database"
	"github.com/miabi-io/miabi/internal/services/domain"
	"github.com/miabi-io/miabi/internal/services/portbinding"
	"github.com/miabi-io/miabi/internal/services/route"
	"github.com/miabi-io/miabi/internal/services/secret"
	"github.com/miabi-io/miabi/internal/services/stack"
	"github.com/miabi-io/miabi/internal/services/storage"
)

// MetaDigest records, on a converged application, the image digest the apply
// engine last reconciled to. It lets the live snapshot report the running
// digest so a digest-pinned manifest converges instead of perpetually drifting.
const MetaDigest = "miabi.io/digest"

// ManagedByGitOps marks resources created/managed by the declarative apply
// engine (GitOps or one-shot apply). Prune only ever deletes these.
const ManagedByGitOps = "gitops"

var (
	// ErrInvalidManifest signals a parse/validation failure (HTTP 400).
	ErrInvalidManifest = errors.New("invalid manifest")
	// ErrUnsupportedKind is returned when a plan needs a mutation the v1 executor
	// does not perform. The plan still lists the change.
	ErrUnsupportedKind = errors.New("kind not yet supported by apply")
	// ErrResourceNotFound is returned by ApplyResource when the named resource is
	// not present in the desired manifest bundle (HTTP 404).
	ErrResourceNotFound = errors.New("resource not found in manifests")
)

// Service computes and executes declarative plans for a workspace.
type Service struct {
	apps    *application.Service
	storage *storage.Service
	dbs     *database.Service
	stacks  *stack.Service
	secrets *secret.Service
	routes  *route.Service
	domains *domain.Service

	// Port-exposure reconcilers (optional; wired via SetPortExposure). extConfig
	// resolves the platform external-access base domain at apply time; bindings
	// manages host-port bindings.
	extConfig func() route.ExternalConfig
	bindings  *portbinding.Service
}

// NewService wires the apply engine over the existing resource services.
func NewService(apps *application.Service, storage *storage.Service, dbs *database.Service, stacks *stack.Service, secrets *secret.Service, routes *route.Service, domains *domain.Service) *Service {
	return &Service{apps: apps, storage: storage, dbs: dbs, stacks: stacks, secrets: secrets, routes: routes, domains: domains}
}

// SetPortExposure wires the reconcilers for an Application's per-port exposure:
// externalAccess (reverse-proxy URLs) and publish/hostPort (host-port bindings).
// Nil-safe: when unset, those manifest fields are ignored.
func (s *Service) SetPortExposure(extConfig func() route.ExternalConfig, bindings *portbinding.Service) {
	s.extConfig, s.bindings = extConfig, bindings
}

// Options tunes a plan/apply.
type Options struct {
	Prune bool
	// OwnerSource, when set, is the GitOps project id that owns this apply. Every
	// resource created/updated in the run is labeled with it (miabi.io/gitops-source)
	// so the project's resources can later be listed or torn down on their own.
	OwnerSource string
}

// ownerSourceCtxKey carries the owning GitOps source id through execute → applyX
// without widening every signature. Set per-apply, so concurrent applies don't
// share state.
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

// tagSource adds the owning GitOps source label to m when the apply runs for a
// source. A no-op for a one-shot apply (no source), keeping that metadata clean.
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
}

// Failure records one change that could not be applied.
type Failure struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"`
	Error  string `json:"error"`
}

// Plan parses the manifests and diffs them against live state, returning the
// convergence plan without mutating anything.
func (s *Service) Plan(ctx context.Context, workspaceID uint, manifests []byte, opts Options) (*declarative.Plan, *declarative.ResourceSet, error) {
	desired, err := declarative.Parse(manifests)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	s.render(workspaceID, desired)
	actual, err := s.snapshot(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	// Prune only ever deletes resources this engine owns, so a GitOps prune can
	// never remove a hand-created or database-owned resource. When the apply runs
	// for a GitOps project (OwnerSource set), prune is further scoped to that
	// project's own resources, so two projects backed by the same repo (different
	// subpaths) don't see each other's apps as orphans and tear them down.
	plan := declarative.BuildPlan(desired, actual, declarative.PlanOptions{
		Prune: opts.Prune, PruneManagedBy: ManagedByGitOps, PruneGitOpsSource: opts.OwnerSource,
	})
	return plan, desired, nil
}

// Apply computes the plan and executes it. A change that fails is recorded in
// Result.Failures; the run continues so independent resources still converge.
func (s *Service) Apply(ctx context.Context, workspaceID uint, manifests []byte, opts Options) (*Result, error) {
	plan, desired, err := s.Plan(ctx, workspaceID, manifests, opts)
	if err != nil {
		return nil, err
	}
	// Carry the owning source into execution so created resources get labeled.
	ctx = withOwnerSource(ctx, opts.OwnerSource)
	res := &Result{Plan: plan, WorkspaceID: workspaceID}
	for _, ch := range plan.Changes {
		if ch.Action == declarative.ActionNoop {
			continue
		}
		dr, _ := desired.Get(string(ch.Kind) + "/" + ch.Name)
		if err := s.execute(ctx, workspaceID, ch, dr); err != nil {
			res.Failures = append(res.Failures, Failure{
				Kind: string(ch.Kind), Name: ch.Name, Action: string(ch.Action), Error: err.Error(),
			})
			continue
		}
		res.Applied++
	}
	// Link each manifest database to the application that consumes it, so the
	// database becomes a first-class dependency of the app (listed under it, its
	// scoped connection revealable there) rather than only an env-template
	// reference — mirroring the marketplace install. Derived from the raw manifest
	// because the change loop has since resolved the apps' env templates.
	if raw, perr := declarative.Parse(manifests); perr == nil {
		s.linkDatabasesToApps(workspaceID, raw)
	}
	return res, nil
}

func (s *Service) ApplyResource(ctx context.Context, workspaceID uint, manifests []byte, opts Options, kind, name string) (*Result, error) {
	plan, desired, err := s.Plan(ctx, workspaceID, manifests, opts)
	if err != nil {
		return nil, err
	}
	key := string(declarative.Kind(kind)) + "/" + name
	ctx = withOwnerSource(ctx, opts.OwnerSource)
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
		if err := s.execute(ctx, workspaceID, ch, dr); err != nil {
			res.Failures = append(res.Failures, Failure{Kind: string(ch.Kind), Name: ch.Name, Action: string(ch.Action), Error: err.Error()})
			return res, nil
		}
		res.Applied++
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
	if err := s.execute(ctx, workspaceID, ch, declarative.Resource{}); err != nil {
		res.Failures = append(res.Failures, Failure{Kind: kind, Name: name, Action: string(ch.Action), Error: err.Error()})
		return res, err
	}
	res.Applied++
	return res, nil
}

// linkDatabasesToApps attaches each manifest database to the application that
// consumes it (the app referencing {{ .databases.<name>.* }} in its env) so the
// database appears under the app's Databases and its scoped connection is
// revealable there — the same first-class attachment the marketplace install makes.
//
// Best-effort and idempotent: never steals a logical database already attached to
// an app, and skips engines (e.g. Redis) with no logical database. The consumer is
// read from the RAW manifest because the apps' env templates are already resolved
// to values by this point.
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
		// Resolve the dependency to its specific logical database (the one stamped
		// with this manifest name), so we attach the app's own database and never
		// another app's database that shares the same instance.
		db, _, ok := s.dbs.FindDatabaseByDeclName(workspaceID, dep)
		if !ok {
			// Fallback for instances without a declaratively-named database (legacy
			// rows): the sole logical database on the instance named after dep.
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
		_, _ = s.dbs.AttachToApp(workspaceID, db.ID, app.ID, "")
	}
}

// rawConsumerApp returns the name of the application that consumes database dep:
// the first app whose env references {{ .databases.<dep>.* }}, or — when nothing
// references it and exactly one application is declared — that sole app. Empty
// when the consumer is ambiguous (no reference and more than one app). Mirrors the
// marketplace consumerApp heuristic.
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
	if apps == 1 {
		return sole
	}
	return ""
}

// Teardown deletes every resource owned by a single GitOps source — those tagged
// with its gitops-source label — in dependency-safe order. Used when a project is
// deleted with the cascade option, so one project's resources can be removed
// without touching another's (unlike a workspace-wide prune). Resources created
// before per-source labeling existed carry no label and are left untouched.
func (s *Service) Teardown(ctx context.Context, workspaceID uint, sourceID string) (*Result, error) {
	if sourceID == "" {
		return &Result{}, nil
	}
	actual, err := s.snapshot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	// Keep only this source's resources, then diff against an empty desired set so
	// BuildPlan emits deletes for exactly them, ordered (dependents before deps).
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
		if err := s.execute(ctx, workspaceID, ch, declarative.Resource{}); err != nil {
			res.Failures = append(res.Failures, Failure{
				Kind: string(ch.Kind), Name: ch.Name, Action: string(ch.Action), Error: err.Error(),
			})
			continue
		}
		res.Applied++
	}
	return res, nil
}

// Delete removes exactly the resources a manifest bundle names — the inverse of
// Apply (`delete -f`). It matches each manifest entry against the live workspace
// by kind+name and deletes the matches in dependency-safe order (dependents
// before their dependencies); entries that do not exist are skipped. Unlike a
// pruning Apply, it deletes a matched resource regardless of which subsystem owns
// it, since the caller named it explicitly. dryRun returns the plan only.
func (s *Service) Delete(ctx context.Context, workspaceID uint, manifests []byte, dryRun bool) (*Result, error) {
	desired, err := declarative.Parse(manifests)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	actual, err := s.snapshot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	// Keep only the live resources the manifest names, then diff against an empty
	// desired set so BuildPlan emits ordered deletes for exactly them.
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
		if err := s.execute(ctx, workspaceID, ch, declarative.Resource{}); err != nil {
			res.Failures = append(res.Failures, Failure{
				Kind: string(ch.Kind), Name: ch.Name, Action: string(ch.Action), Error: err.Error(),
			})
			continue
		}
		res.Applied++
	}
	return res, nil
}

// render interpolates each application's env against the workspace's resolvable
// databases, so {{ .databases.<name>.uri }} resolves in the plan/diff just as in
// templates. It is lenient: a reference to a database declared in the same
// bundle but not yet provisioned is left as its template rather than aborting
// the plan — the apply path re-renders that app strictly once the database
// exists (see renderAppEnv). This is what lets a fresh bundle that creates a
// database and an app referencing it converge in a single apply.
func (s *Service) render(workspaceID uint, desired *declarative.ResourceSet) {
	ctx := declarative.RenderContext{
		Databases: s.databaseViews(workspaceID),
		Secrets:   s.secretViews(workspaceID),
	}
	r := declarative.NewRenderer(ctx)
	for i := range desired.All() {
		res := desired.All()[i]
		if res.Application == nil || len(res.Application.Env) == 0 {
			continue
		}
		res.Application.Env = r.RenderEnvLenient(res.Metadata.Name, res.Application.Env)
		desired.Add(res) // write back the rendered copy
	}
}

// renderAppEnv resolves an application's env templates at execution time, against
// the databases that are live right now — including any declared earlier in the
// same bundle and just created. Strict: a reference that still cannot resolve
// (a typo, or a database that genuinely does not exist) is a real error.
func (s *Service) renderAppEnv(workspaceID uint, spec *declarative.ApplicationSpec) error {
	if spec == nil || len(spec.Env) == 0 {
		return nil
	}
	r := declarative.NewRenderer(declarative.RenderContext{
		Databases: s.databaseViews(workspaceID),
		Secrets:   s.secretViews(workspaceID),
	})
	rendered, err := r.RenderEnv("application", spec.Env)
	if err != nil {
		return err
	}
	spec.Env = rendered
	return nil
}

// databaseViews resolves connection details for {{ .databases.<name>.* }},
// keyed by the declarative Database name.
//
// A manifest Database is resolved to the *specific* logical database stamped with
// its declarative name (created in applyDatabase), so it yields that database's
// dedicated user/credentials — never another app's database that happens to share
// the same instance. Instances with no declaratively-named logical database
// (Redis, libSQL, marketplace/manual, legacy rows) keep the instance-name keying
// and fall back to the scoped-first or admin/direct connection via appConnection.
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

	// Declaratively-named logical databases map by their manifest name to their
	// own exact connection.
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
			out[declName] = connView(conn)
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

// appConnection returns the connection an application should use for an instance:
// the scoped logical-database credentials when one exists, else the admin/direct
// connection (Redis, or a legacy instance with no logical database).
func (s *Service) appConnection(workspaceID uint, inst *models.DatabaseInstance) (declarative.ConnView, error) {
	if inst.SupportsLogicalDatabases() {
		if dbs, err := s.dbs.ListDatabases(workspaceID, inst.ID); err == nil && len(dbs) > 0 {
			if conn, cErr := s.dbs.DatabaseConnection(inst, &dbs[0]); cErr == nil {
				return connView(conn), nil
			}
		}
	}
	conn, err := s.dbs.InstanceConnection(inst)
	if err != nil {
		return declarative.ConnView{}, err
	}
	return connView(conn), nil
}

func connView(conn database.ConnectionInfo) declarative.ConnView {
	return declarative.ConnView{
		Host: conn.Host, Port: strconv.Itoa(conn.Port), User: conn.Username,
		Password: conn.Password, Name: conn.Database, URI: conn.URI,
	}
}

// secretViews resolves the plaintext value of every secret in the workspace,
// keyed by name, so {{ .secrets.<name> }} resolves during render — in both the
// lenient plan-time pass and the strict execute-time re-render. A secret declared
// earlier in the same bundle exists by the time the app is applied (Secret has a
// lower plan rank than Application). Without it, any env referencing a secret fails
// the strict render in renderAppEnv, so the app update errors out and never
// redeploys.
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

// snapshot builds the live ResourceSet for the workspace across every kind the
// executor manages. Each resource is labeled with its owner so prune stays safe.
func (s *Service) snapshot(ctx context.Context, workspaceID uint) (*declarative.ResourceSet, error) {
	set := declarative.NewResourceSet()
	appSlugByID := map[uint]string{}

	// Volumes first: apps reference them by name in their mounts, so build the
	// id -> name map before emitting apps.
	vols, err := s.storage.List(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot volumes: %w", err)
	}
	volNameByID := make(map[uint]string, len(vols))
	for i := range vols {
		volNameByID[vols[i].ID] = vols[i].Name
		set.Add(declarative.Resource{
			APIVersion: declarative.APIVersion, Kind: declarative.KindVolume,
			Metadata: metaA(vols[i].UID, vols[i].Name, vols[i].Metadata, vols[i].Annotations), Volume: &declarative.VolumeSpec{},
		})
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
		set.Add(appResource(full, ext, pub, volNameByID))
	}

	instances, err := s.dbs.List(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot databases: %w", err)
	}
	// A manifest Database is identified by name. When it was provisioned as a
	// logical database (dedicated or shared), that database carries the manifest
	// name as its declarative-name label; surface it under that name (and its own
	// uid/provenance) so the diff matches the manifest and a prune removes exactly
	// that database — not the whole instance it may share with other apps.
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
			// Represented by its declaratively-named logical database(s), not by the
			// instance host (which has no manifest identity of its own).
			for j := range decls {
				d := &decls[j]
				set.Add(declarative.Resource{
					APIVersion: declarative.APIVersion, Kind: declarative.KindDatabase,
					Metadata: metaA(d.UID, d.Metadata[models.MetaDeclarativeName], d.Metadata, nil),
					Database: &declarative.DatabaseSpec{
						Engine: string(inst.Engine), Version: inst.Version, Placement: "auto",
					},
				})
			}
			continue
		}
		set.Add(declarative.Resource{
			APIVersion: declarative.APIVersion, Kind: declarative.KindDatabase,
			Metadata: metaA(inst.UID, inst.Name, inst.Metadata, inst.Annotations),
			Database: &declarative.DatabaseSpec{
				Engine: string(inst.Engine), Version: inst.Version, Placement: "auto",
			},
		})
	}

	stacks, err := s.stacks.List(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot stacks: %w", err)
	}
	for i := range stacks {
		set.Add(declarative.Resource{
			APIVersion: declarative.APIVersion, Kind: declarative.KindStack,
			Metadata: metaA(stacks[i].UID, stacks[i].Name, stacks[i].Metadata, stacks[i].Annotations),
			Stack:    &declarative.StackSpec{Description: stacks[i].Description},
		})
	}

	secrets, err := s.secrets.List(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot secrets: %w", err)
	}
	for i := range secrets {
		set.Add(declarative.Resource{
			APIVersion: declarative.APIVersion, Kind: declarative.KindSecret,
			Metadata: meta(secrets[i].UID, secrets[i].Name, secrets[i].Metadata), Secret: &declarative.SecretSpec{},
		})
	}

	routes, err := s.routes.List(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot routes: %w", err)
	}
	for i := range routes {
		r := routes[i]
		// Generated external-access routes are owned by the externalAccess port
		// flag, not the Route kind — skip them so they aren't phantom Routes.
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
			Route: &declarative.RouteSpec{
				Hosts: append([]string(nil), r.Hosts...), App: appSlugByID[r.ApplicationID], Port: r.TargetPort,
				Path: path, TLS: tlsToSpec(r.TLSMode),
			},
		})
	}

	domains, err := s.domains.List(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("snapshot domains: %w", err)
	}
	for i := range domains {
		d := domains[i]
		// Domains carry no managed-by label (the model has no metadata), so a
		// GitOps prune treats them as user-owned and never deletes them — a safe
		// default. Apply still creates/updates them from the manifest.
		set.Add(declarative.Resource{
			APIVersion: declarative.APIVersion, Kind: declarative.KindDomain,
			Metadata: declarative.Meta{Name: d.Name},
			Domain:   &declarative.DomainSpec{TLS: string(d.TLSMode), Wildcard: d.Wildcard},
		})
	}
	return set, nil
}

// meta builds a declarative Meta carrying the resource name plus its owner label
// (from the live resource's managed-by metadata) so prune can stay scoped.
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

// metaA is meta() with the resource's annotations attached when present, for the
// live→manifest reverse mapping.
func metaA(uid, name string, m, annotations models.Metadata) declarative.Meta {
	out := meta(uid, name, m)
	if len(annotations) > 0 {
		out.Annotations = annotations
	}
	return out
}

// appResource maps a live application to its declarative form for diffing. ext
// and pub are the container ports currently exposed externally (generated route)
// and published (host-port binding), so per-port exposure converges idempotently.
func appResource(app *models.Application, ext, pub map[int]bool, volNameByID map[uint]string) declarative.Resource {
	spec := &declarative.ApplicationSpec{
		Image:           app.Image,
		Tag:             app.Tag,
		Digest:          app.Metadata[MetaDigest],
		Command:         app.Command,
		ContainerLabels: app.ContainerLabels,
		ExternalLabel:   app.ExternalLabel,
	}
	// Managed volume mounts, by the volume's manifest name. Privileged host-preset
	// binds (VolumeID 0) aren't manifest-expressible, so they're omitted.
	for _, m := range app.Mounts {
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

// execute performs one change by driving the relevant service.
func (s *Service) execute(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	switch ch.Kind {
	case declarative.KindApplication:
		return s.applyApplication(ctx, workspaceID, ch, desired)
	case declarative.KindVolume:
		return s.applyVolume(ctx, workspaceID, ch, desired)
	case declarative.KindDatabase:
		return s.applyDatabase(ctx, workspaceID, ch, desired)
	case declarative.KindStack:
		return s.applyStack(ctx, workspaceID, ch, desired)
	case declarative.KindSecret:
		return s.applySecret(workspaceID, ch, desired)
	case declarative.KindRoute:
		return s.applyRoute(ctx, workspaceID, ch, desired)
	case declarative.KindDomain:
		return s.applyDomain(workspaceID, ch, desired)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedKind, ch.Kind)
	}
}

// applyDomain converges an owned domain (name = the FQDN). Verification is a
// runtime action, so a freshly-applied domain starts unverified.
func (s *Service) applyDomain(workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	switch ch.Action {
	case declarative.ActionCreate:
		_, err := s.domains.Create(workspaceID, domain.Input{
			Name:     ch.Name,
			TLSMode:  models.DomainTLSMode(desired.Domain.TLS),
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
			TLSMode:  models.DomainTLSMode(desired.Domain.TLS),
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

func (s *Service) applyStack(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	switch ch.Action {
	case declarative.ActionCreate:
		_, err := s.stacks.Create(ctx, workspaceID, stack.Input{
			Name:        ch.Name,
			Description: desired.Stack.Description,
			Metadata:    tagSource(ctx, models.SetBuiltin(models.Metadata{}, models.MetaManagedBy, ManagedByGitOps)),
			Annotations: desired.Metadata.Annotations,
		})
		return err
	case declarative.ActionUpdate:
		st, err := s.findStack(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		desc := desired.Stack.Description
		_, err = s.stacks.Update(workspaceID, st.ID, stack.UpdateInput{
			Description: &desc, Annotations: desired.Metadata.Annotations,
		})
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
			n := spec.Length
			if n <= 0 {
				n = 32
			}
			value = declarative.RandAlphaNum(n)
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

func (s *Service) applyRoute(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	spec := desired.Route
	switch ch.Action {
	case declarative.ActionCreate:
		app, err := s.findApp(workspaceID, spec.App)
		if err != nil {
			return fmt.Errorf("route %q: %w", ch.Name, err)
		}
		in := s.routeInput(ch.Name, app.ID, spec)
		in.Metadata = tagSource(ctx, models.SetBuiltin(models.Metadata{}, models.MetaManagedBy, ManagedByGitOps))
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
		in := s.routeInput(ch.Name, app.ID, spec)
		in.Metadata = tagSource(ctx, models.SetBuiltin(models.Metadata{}, models.MetaManagedBy, ManagedByGitOps))
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

// routeInput maps a declarative RouteSpec to the route service's input.
func (s *Service) routeInput(name string, appID uint, spec *declarative.RouteSpec) route.Input {
	path := spec.Path
	if path == "" {
		path = "/"
	}
	return route.Input{
		Name: name, ApplicationID: appID, Hosts: spec.Hosts,
		Path: path, TargetPort: spec.Port, TLSMode: tlsFromSpec(spec.TLS),
	}
}

func (s *Service) applyVolume(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	switch ch.Action {
	case declarative.ActionCreate:
		meta := tagSource(ctx, models.SetBuiltin(models.Metadata{}, models.MetaManagedBy, ManagedByGitOps))
		_, err := s.storage.Create(ctx, workspaceID, 0, ch.Name, 0, meta, desired.Metadata.Annotations)
		return err
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
		meta := tagSource(ctx, models.SetBuiltin(models.Metadata{}, models.MetaManagedBy, ManagedByGitOps))
		// Honor the manifest's placement (auto/shared/dedicated) via the shared
		// resolver, mirroring the marketplace install. The logical database is
		// stamped with the manifest name (ch.Name) so databaseViews resolves
		// {{ .databases.<name>.* }} to this exact database — not "the first database
		// on the instance" (which on a shared instance is some other app's DB). The
		// CREATE DDL is deferred for a freshly provisioned instance and runs when it
		// comes up (applyPendingDatabases); a reused running instance gets it now.
		_, _, _, _, err := s.dbs.ResolveDependency(
			ctx, workspaceID, 0, 0, ch.Name, ch.Name,
			models.DBEngine(spec.Engine), spec.Version, database.Placement(spec.Placement), meta,
		)
		return err
	case declarative.ActionDelete:
		// A manifest database may be a dedicated instance or a logical database
		// sharing another instance. Resolve which by its declarative name and tear
		// down only what we own: drop just the logical database when the instance
		// hosts others (never destroy a shared instance and the apps on it); remove
		// the whole instance only when it was provisioned for this dependency alone.
		if db, inst, ok := s.dbs.FindDatabaseByDeclName(workspaceID, ch.Name); ok {
			remaining, _ := s.dbs.ListDatabases(workspaceID, inst.ID)
			shared := len(remaining) > 1 || inst.Name != ch.Name
			if shared {
				return s.dbs.DeleteDatabase(ctx, workspaceID, db.ID)
			}
			return s.deleteInstance(ctx, inst)
		}
		// Fallback: a dedicated instance named after the dependency (Redis, libSQL,
		// or a legacy row created before logical databases were tagged).
		inst, err := s.findInstance(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		return s.deleteInstance(ctx, inst)
	case declarative.ActionUpdate:
		// Engine/version changes are not converged in place (would recreate data).
		return fmt.Errorf("database %q: in-place engine/version change is not supported", ch.Name)
	}
	return nil
}

// deleteInstance tears down a database instance during a prune. Prune is an
// automated reconcile, not an interactive action, so a running instance is
// stopped first — the "stop the database before deleting it" guard (meant for the
// UI) would otherwise block teardown.
func (s *Service) deleteInstance(ctx context.Context, inst *models.DatabaseInstance) error {
	if inst.Status == models.DBStatusRunning {
		if err := s.dbs.Stop(ctx, inst); err != nil {
			return err
		}
	}
	return s.dbs.Delete(ctx, inst)
}

func (s *Service) applyApplication(ctx context.Context, workspaceID uint, ch declarative.Change, desired declarative.Resource) error {
	spec := desired.Application
	// Resolve env templates now: databases declared in the same bundle run before
	// applications (plan order), so {{ .databases.x.host }} references are live by
	// this point even on a first apply. The plan-time render was lenient and may
	// have left them as templates.
	if err := s.renderAppEnv(workspaceID, spec); err != nil {
		return fmt.Errorf("%w: application %q: %v", ErrInvalidManifest, ch.Name, err)
	}
	switch ch.Action {
	case declarative.ActionCreate:
		app, err := s.apps.Create(workspaceID, s.createInput(ctx, desired.Metadata, spec))
		if err != nil {
			return err
		}
		if err := s.reconcileEnv(app.ID, spec); err != nil {
			return err
		}
		// Reconcile exposure before deploy so host-port bindings publish in the
		// first container.
		if err := s.reconcileExposure(ctx, workspaceID, app, spec); err != nil {
			return err
		}
		// Attach declared volumes before the first deploy so the container mounts
		// them from the start.
		if err := s.reconcileMounts(workspaceID, app, spec); err != nil {
			return err
		}
		_, err = s.apps.Deploy(app, nil, "", "")
		return err
	case declarative.ActionUpdate:
		app, err := s.findApp(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		app.Image = spec.Image
		app.Tag = spec.Tag
		app.Command = spec.Command
		if spec.Resources != nil {
			mb, _ := spec.Resources.MemoryBytes()
			nc, _ := spec.Resources.NanoCPUs()
			app.MemoryBytes, app.NanoCPUs = mb, nc
			app.GPUCount, app.GPUKind = spec.Resources.GPU, spec.Resources.GPUKind
		}
		// Reconcile user labels (built-in keys preserved) + annotations from the
		// manifest, then re-stamp the digest built-in.
		app.Metadata = models.SetBuiltin(
			models.MergeUserMetadata(app.Metadata, desired.Metadata.Labels),
			MetaDigest, spec.Digest,
		)
		app.Annotations = desired.Metadata.Annotations
		// Container labels round-trip through the manifest; the app service
		// sanitizes reserved keys (fail-soft) so a manifest can never spoof
		// platform labels.
		app.ContainerLabels = spec.ContainerLabels
		if err := s.apps.Update(app); err != nil {
			return err
		}
		if err := s.reconcileEnv(app.ID, spec); err != nil {
			return err
		}
		if err := s.reconcileExposure(ctx, workspaceID, app, spec); err != nil {
			return err
		}
		if err := s.reconcileMounts(workspaceID, app, spec); err != nil {
			return err
		}
		_, err = s.apps.Deploy(app, nil, "", "")
		return err
	case declarative.ActionDelete:
		app, err := s.findApp(workspaceID, ch.Name)
		if err != nil {
			return err
		}
		// Prune is automated: stop a running app first so the interactive "stop
		// before deleting" guard doesn't block teardown.
		if s.apps.LiveStatus(ctx, app).Running {
			if err := s.apps.Stop(ctx, app); err != nil {
				return err
			}
		}
		return s.apps.Delete(ctx, app)
	}
	return nil
}

// reconcileExposure converges an app's per-port exposure to the manifest: pins
// the external-access label, reconciles host-port bindings, and reconciles the
// reverse-proxy external-access routes. Idempotent.
func (s *Service) reconcileExposure(ctx context.Context, workspaceID uint, app *models.Application, spec *declarative.ApplicationSpec) error {
	// Pin the external-access subdomain so the public URL is deterministic. The
	// label maps to a platform-wide host, so a value already claimed by another
	// app can't be honored — ignore it and fall back to a generated label (keeping
	// any label this app already has) instead of failing the apply.
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
	// Reverse-proxy external access: the set of ports flagged externalAccess.
	// SetExternalAccess is idempotent and also tears down ports no longer flagged.
	if s.extConfig != nil {
		var ext []int
		for _, p := range spec.Ports {
			if p.ExternalAccess {
				ext = append(ext, p.Container)
			}
		}
		if _, err := s.routes.SetExternalAccess(ctx, workspaceID, app.ID, ext, s.extConfig()); err != nil {
			return fmt.Errorf("external access: %w", err)
		}
	}
	return nil
}

// reconcileBindings converges an app's host-port bindings to the manifest's
// publish/hostPort ports. Presence-based: an existing binding for a still-desired
// port is left as-is (the live host port — possibly auto-allocated — is not
// churned); bindings for ports no longer published are cancelled. Control-plane
// (managed) bindings are never touched.
func (s *Service) reconcileBindings(workspaceID, appID uint, spec *declarative.ApplicationSpec) error {
	desired := map[int]declarative.PortSpec{}
	for _, p := range spec.Ports {
		if p.Publish || p.HostPort > 0 {
			desired[p.Container] = p
		}
	}
	existing, err := s.bindings.ListByApp(workspaceID, appID)
	if err != nil {
		return err
	}
	have := map[int]*models.PortBinding{}
	for i := range existing {
		if existing[i].Managed {
			continue
		}
		have[existing[i].ContainerPort] = &existing[i]
	}
	for cport, p := range desired {
		if _, ok := have[cport]; ok {
			continue // already bound — keep the live host port
		}
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		if _, err := s.bindings.Request(workspaceID, 0, portbinding.RequestInput{
			ApplicationID: appID, ContainerPort: cport, Protocol: proto, HostPort: p.HostPort,
		}); err != nil {
			return fmt.Errorf("bind port %d: %w", cport, err)
		}
	}
	for cport, b := range have {
		if _, ok := desired[cport]; !ok {
			if err := s.bindings.Cancel(workspaceID, b.ID); err != nil {
				return fmt.Errorf("unbind port %d: %w", cport, err)
			}
		}
	}
	return nil
}

// exposedPorts derives an app's live exposure: container ports that have a
// generated external-access route, and container ports with a (non-managed)
// host-port binding. Powers idempotent diffing.
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
				if !bs[i].Managed {
					pub[bs[i].ContainerPort] = true
				}
			}
		}
	}
	return ext, pub
}

// createInput maps a declarative ApplicationSpec to the application service's
// create input.
func (s *Service) createInput(ctx context.Context, m declarative.Meta, spec *declarative.ApplicationSpec) application.CreateInput {
	// Manifest labels become user metadata (reserved keys are stripped so a
	// manifest can never spoof provenance); annotations are stored verbatim.
	md := tagSource(ctx, models.SetBuiltin(models.SanitizeUserMetadata(m.Labels),
		models.MetaManagedBy, ManagedByGitOps,
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
		ContainerLabels: spec.ContainerLabels, // sanitized in the app service Create
	}
	if spec.Resources != nil {
		in.MemoryBytes, _ = spec.Resources.MemoryBytes()
		in.NanoCPUs, _ = spec.Resources.NanoCPUs()
		in.GPUCount, in.GPUKind = spec.Resources.GPU, spec.Resources.GPUKind
	}
	for _, p := range spec.Ports {
		in.Ports = append(in.Ports, application.PortSpec{ContainerPort: p.Container, Scheme: p.Scheme, Protocol: p.Protocol})
		if in.Port == 0 {
			in.Port = p.Container
		}
	}
	return in
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

// reconcileMounts converges the app's volume mounts to the manifest: attaches (or
// re-paths) each declared Volume and detaches managed volume mounts no longer
// present. Privileged host-preset binds (VolumeID 0) aren't manifest-expressible
// and are left untouched. Volumes apply before their app (EdgeMount), so they
// resolve here even on a first apply. Takes effect on the next deploy.
func (s *Service) reconcileMounts(workspaceID uint, app *models.Application, spec *declarative.ApplicationSpec) error {
	keep := make(map[uint]bool, len(spec.Mounts))
	for _, mt := range spec.Mounts {
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
	return nil
}

func (s *Service) findApp(workspaceID uint, slug string) (*models.Application, error) {
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
