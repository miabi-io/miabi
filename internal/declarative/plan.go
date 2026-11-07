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

package declarative

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// Action is the operation a plan entry performs.
type Action string

const (
	ActionCreate Action = "create"
	ActionUpdate Action = "update"
	ActionDelete Action = "delete"
	ActionNoop   Action = "noop"
)

// FieldDiff is a single changed field in an update.
type FieldDiff struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// Change is one entry in a plan: what happens to one resource.
type Change struct {
	Action Action      `json:"action"`
	Kind   Kind        `json:"kind"`
	Name   string      `json:"name"`
	Reason string      `json:"reason,omitempty"`
	Fields []FieldDiff `json:"fields,omitempty"`
}

type Plan struct {
	Changes []Change `json:"changes"`
}

// LabelManagedBy keeps prune from ever deleting a resource the apply engine did not create.
const LabelManagedBy = "miabi.io/managed-by"

// LabelGitOpsSource lets one GitOps project's resources be torn down without touching another's.
const LabelGitOpsSource = "miabi.io/gitops-source"

// PlanOptions tunes plan generation.
type PlanOptions struct {
	// Prune emits deletes for actual resources absent from desired.
	Prune bool
	// PruneManagedBy keeps a GitOps prune from deleting hand-created resources; empty prunes any orphan.
	PruneManagedBy string
	// PruneGitOpsSource scopes prune to one project; resources with no source label are never matched.
	PruneGitOpsSource string
	// IncludeNoop keeps in-sync resources in the plan (useful for status views).
	IncludeNoop bool
}

// HasChanges reports whether the plan mutates anything.
func (p *Plan) HasChanges() bool {
	for _, c := range p.Changes {
		if c.Action != ActionNoop {
			return true
		}
	}
	return false
}

// Counts tallies the plan by action.
func (p *Plan) Counts() (create, update, del, noop int) {
	for _, c := range p.Changes {
		switch c.Action {
		case ActionCreate:
			create++
		case ActionUpdate:
			update++
		case ActionDelete:
			del++
		case ActionNoop:
			noop++
		}
	}
	return
}

func applyRank(k Kind) int {
	switch k {
	case KindSecret, KindSealedSecret, KindVolume, KindDomain, KindConfig, KindMiddleware:
		// Middlewares come first because a route naming a chain is refused until every middleware in it exists.
		return 0
	case KindDatabase, KindStack, KindRegistry, KindGitRepository:
		// Above Secret: its password is usually a secret ref, and same-rank ties order by kind name.
		return 1
	case KindApplication:
		return 2
	case KindRoute, KindCronJob, KindJob:
		// Jobs run in an existing app and may pull through a declared Registry.
		return 3
	case KindGitSource:
		// Last, so a parent's own resources are in place before a nested project starts syncing.
		return 4
	default:
		return 9
	}
}

func planned(k Kind) bool { return k != KindProject }

func matchActual(actual *ResourceSet, byUID map[string]Resource, d Resource) (Resource, bool) {
	if d.Metadata.UID != "" {
		if r, ok := byUID[string(d.Kind)+"/"+d.Metadata.UID]; ok {
			return r, true
		}
	}
	if r, ok := actual.Get(d.Key()); ok {
		return r, true
	}
	// Switching between Secret and SealedSecret updates one vault entry; a second would violate the unique name.
	if alias, ok := vaultAlias(d.Kind); ok {
		return actual.Get(string(alias) + "/" + d.Metadata.Name)
	}
	return Resource{}, false
}

func vaultAlias(k Kind) (Kind, bool) {
	switch k {
	case KindSecret:
		return KindSealedSecret, true
	case KindSealedSecret:
		return KindSecret, true
	}
	return "", false
}

// BuildPlan expects actual to hold only Miabi-managed resources when Prune is set.
func BuildPlan(desired, actual *ResourceSet, opts PlanOptions) *Plan {
	plan := &Plan{}

	// Match by uid so a rename converges in place instead of a destructive delete+create.
	actualByUID := map[string]Resource{}
	for _, a := range actual.All() {
		if a.Metadata.UID != "" {
			actualByUID[string(a.Kind)+"/"+a.Metadata.UID] = a
		}
	}
	desiredUIDs := map[string]bool{}
	for _, d := range desired.All() {
		if d.Metadata.UID != "" {
			desiredUIDs[string(d.Kind)+"/"+d.Metadata.UID] = true
		}
	}

	for _, d := range desired.All() {
		if !planned(d.Kind) {
			continue
		}
		cur, exists := matchActual(actual, actualByUID, d)
		if !exists {
			plan.Changes = append(plan.Changes, Change{
				Action: ActionCreate, Kind: d.Kind, Name: d.Metadata.Name,
				Reason: "not present in workspace",
			})
			continue
		}
		fields := diffFields(cur, d)
		if len(fields) == 0 {
			if opts.IncludeNoop {
				plan.Changes = append(plan.Changes, Change{Action: ActionNoop, Kind: d.Kind, Name: d.Metadata.Name})
			}
			continue
		}
		plan.Changes = append(plan.Changes, Change{
			Action: ActionUpdate, Kind: d.Kind, Name: d.Metadata.Name,
			Reason: "live state differs from desired", Fields: fields,
		})
	}

	if opts.Prune {
		for _, a := range actual.All() {
			if !planned(a.Kind) {
				continue
			}
			if _, keep := desired.Get(a.Key()); keep {
				continue
			}
			if alias, ok := vaultAlias(a.Kind); ok && desired.Has(alias, a.Metadata.Name) {
				continue // the same vault entry, now declared under the other kind
			}
			if a.Metadata.UID != "" && desiredUIDs[string(a.Kind)+"/"+a.Metadata.UID] {
				continue // claimed by a desired resource via uid (a rename, not a delete)
			}
			if opts.PruneManagedBy != "" && a.Metadata.Labels[LabelManagedBy] != opts.PruneManagedBy {
				continue // never prune a resource this engine doesn't own
			}
			if opts.PruneGitOpsSource != "" && a.Metadata.Labels[LabelGitOpsSource] != opts.PruneGitOpsSource {
				continue // never prune a resource owned by a different GitOps project
			}
			plan.Changes = append(plan.Changes, Change{
				Action: ActionDelete, Kind: a.Kind, Name: a.Metadata.Name,
				Reason: "removed from desired state",
			})
		}
	}

	sortChanges(plan.Changes)
	return plan
}

func sortChanges(cs []Change) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		ga, gb := group(a.Action), group(b.Action)
		if ga != gb {
			return ga < gb
		}
		ra, rb := applyRank(a.Kind), applyRank(b.Kind)
		if a.Action == ActionDelete {
			ra, rb = -ra, -rb // reverse order for deletes
		}
		if ra != rb {
			return ra < rb
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
}

// A removed dependent is torn down before a renamed dependency is created.
func group(a Action) int {
	if a == ActionDelete {
		return 0
	}
	return 1
}

// Secret values are write-only and only compared by presence.
func diffFields(actual, desired Resource) []FieldDiff {
	if desired.Kind == KindSecret {
		return nil // opaque: an existing secret is treated as in-sync
	}
	if desired.Kind == KindSealedSecret {
		return diffSealedSecret(actual, desired)
	}
	if desired.Kind == KindRegistry {
		return diffRegistry(actual, desired)
	}
	if desired.Kind == KindConfig {
		return diffConfig(actual, desired)
	}
	if desired.Kind == KindMiddleware {
		return diffMiddleware(actual, desired)
	}
	if desired.Kind == KindJob {
		return diffJob(actual, desired)
	}
	av := specFields(actual)
	dv := specFields(desired)

	for f := range optionalWhenUnset {
		if _, stated := dv[f]; !stated {
			delete(av, f)
		}
	}
	for f := range optionalWhenUnsetByKind[desired.Kind] {
		if _, stated := dv[f]; !stated {
			delete(av, f)
		}
	}
	// Stack env is compared only when stated, so console-set variables survive an older manifest.
	if desired.Stack != nil && desired.Stack.Env == nil {
		for f := range av {
			if strings.HasPrefix(f, "env.") {
				delete(av, f)
			}
		}
	}
	keys := map[string]bool{}
	for k := range av {
		keys[k] = true
	}
	for k := range dv {
		keys[k] = true
	}
	var out []FieldDiff
	for k := range keys {
		if av[k] != dv[k] {
			out = append(out, FieldDiff{Field: k, From: av[k], To: dv[k]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

var optionalWhenUnset = map[string]bool{
	"placement.location": true, "placement.constraints": true,
	"deployment.strategy": true, "deployment.runtime": true, "deployment.replicas": true,
	"deployment.update.parallelism": true, "deployment.update.delaySeconds": true,
	"healthcheck.type": true, "healthcheck.path": true, "healthcheck.port": true, "healthcheck.command": true,
	"healthcheck.intervalSeconds": true, "healthcheck.timeoutSeconds": true, "healthcheck.retries": true,
	"healthcheck.startPeriodSeconds": true, "stack": true,
}

// A database's limits may come from the console or plan defaults, so a silent manifest leaves them be.
var optionalWhenUnsetByKind = map[Kind]map[string]bool{
	KindDatabase: {"resources.memory": true, "resources.cpu": true, "resources.size": true, "readOnlyUser": true},
	// A silent storage class keeps repos portable; a named one is immutable, so it is refused rather than converged.
	KindVolume: {"storage.class": true, "size": true},
	// A pause made in the console survives a manifest that doesn't state suspend.
	KindCronJob: {"suspend": true},
}

// Normalized so CAP_NET_ADMIN in a manifest does not drift against a stored NET_ADMIN.
func normalizedList(in []string, normalize func([]string) ([]string, error)) string {
	if out, err := normalize(in); err == nil {
		return strings.Join(out, ",")
	}
	return strings.Join(in, ",")
}

// Compared by fingerprint; the live side may be a plain Secret being adopted.
func diffSealedSecret(actual, desired Resource) []FieldDiff {
	d := desired.SealedSecret
	if d == nil || d.SealedFP == "" {
		return nil
	}
	from := "(current)"
	if actual.SealedSecret == nil || actual.SealedSecret.SealedFP == "" {
		from = "(not from a sealed value)"
	} else if actual.SealedSecret.SealedFP == d.SealedFP {
		return nil
	}
	return []FieldDiff{{Field: "value", From: from, To: "(sealed)"}}
}

func diffRegistry(actual, desired Resource) []FieldDiff {
	a, d := actual.Registry, desired.Registry
	if d == nil {
		return nil
	}
	var out []FieldDiff
	if a == nil {
		a = &RegistrySpec{}
	}
	if a.Server != d.Server {
		out = append(out, FieldDiff{Field: "server", From: a.Server, To: d.Server})
	}
	if a.Username != d.Username {
		out = append(out, FieldDiff{Field: "username", From: a.Username, To: d.Username})
	}
	if d.Password != "" && d.PasswordFP != "" && a.PasswordFP != "" && a.PasswordFP != d.PasswordFP {
		out = append(out, FieldDiff{Field: "password", From: "(current)", To: "(rotated)"})
	}
	return out
}

// The rule may carry a password, so it diffs by fingerprint of the rendered rule, which still catches rotation.
func diffMiddleware(actual, desired Resource) []FieldDiff {
	a, d := actual.Middleware, desired.Middleware
	if d == nil {
		return nil
	}
	if a == nil {
		a = &MiddlewareSpec{}
	}
	var out []FieldDiff
	if a.Type != d.Type {
		out = append(out, FieldDiff{Field: "type", From: a.Type, To: d.Type})
	}
	ap, dp := strings.Join(a.Paths, ","), strings.Join(d.Paths, ",")
	if ap != dp {
		out = append(out, FieldDiff{Field: "paths", From: ap, To: dp})
	}
	// An install whose engine did not stamp a fingerprint must not read as drift on every plan.
	if a.RuleFP != "" && d.RuleFP != "" && a.RuleFP != d.RuleFP {
		out = append(out, FieldDiff{Field: "rule", From: "(current)", To: "(changed)"})
	}
	return out
}

// A sensitive config diffs by fingerprint only, so no file content ever reaches a plan.
func diffConfig(actual, desired Resource) []FieldDiff {
	a, d := actual.Config, desired.Config
	if d == nil {
		return nil
	}
	if a == nil {
		a = &ConfigSpec{}
	}
	var out []FieldDiff
	if a.DigestFP != d.DigestFP {
		out = append(out, FieldDiff{Field: "data", From: a.DigestFP, To: d.DigestFP})
	}
	if a.Mode != d.Mode {
		out = append(out, FieldDiff{Field: "mode", From: a.Mode, To: d.Mode})
	}
	if !d.Sensitive {
		keys := map[string]bool{}
		for k := range a.Data {
			keys[k] = true
		}
		for k := range d.Data {
			keys[k] = true
		}
		for k := range keys {
			if a.Data[k] != d.Data[k] {
				out = append(out, FieldDiff{Field: "data." + k, From: present(a.Data, k), To: present(d.Data, k)})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

func present(m map[string]string, k string) string {
	if _, ok := m[k]; !ok {
		return "(absent)"
	}
	return "(present)"
}

// The app service strips these on write, so they must be excluded on both sides or always drift.
var reservedLabelPrefixes = []string{"io.miabi.", "com.docker."}

func reservedLabelKey(key string) bool {
	for _, p := range reservedLabelPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// Fields named in secretEnv are masked so secret values never appear in a plan.
func specFields(r Resource) map[string]string {
	f := map[string]string{}
	switch {
	case r.Application != nil:

		a := r.Application
		f["image"] = a.Image
		f["tag"] = a.Tag
		f["digest"] = a.Digest
		// Repointing the build source at another repo or ref must converge, not sit as invisible drift.
		if src := a.Source; src != nil {
			f["source.git"] = src.Git
			f["source.ref"] = src.Ref
			f["source.buildMethod"] = src.BuildMethod
			f["source.builder"] = src.Builder
			f["source.buildpacks"] = strings.Join(src.Buildpacks, ",")
			f["source.repository"] = src.Repository
			for k, v := range src.BuildEnv {
				f["source.buildEnv."+k] = v
			}
		}
		f["command"] = strings.Join(a.Command, " ")
		f["registry"] = a.Registry
		if a.Stack != "" {
			f["stack"] = a.Stack
		}
		// Absent means the container default, so removing one from a manifest converges too.
		f["security.runAsUser"] = a.RunAsUser()
		f["security.readOnlyRootFilesystem"] = strconv.FormatBool(a.ReadOnlyRootFilesystem())
		f["security.noNewPrivileges"] = strconv.FormatBool(a.NoNewPrivileges())
		f["security.capabilities.add"] = normalizedList(a.AddCapabilities(), models.NormalizeCapabilities)
		f["security.capabilities.drop"] = normalizedList(a.DropCapabilities(), models.NormalizeDropCapabilities)
		f["security.devices"] = normalizedList(a.Devices(), models.NormalizeDevices)
		if loc := a.Location(); loc != "" {
			f["placement.location"] = loc
		}
		// Only compared when stated, so console-configured rollout settings don't diff.
		if s := a.Strategy(); s != "" {
			f["deployment.strategy"] = s
		}
		if rt := a.Runtime(); rt != "" {
			f["deployment.runtime"] = rt
		}
		if n := a.Replicas(); n > 0 {
			f["deployment.replicas"] = strconv.Itoa(n)
		}
		if c := a.Constraints(); c != nil {
			f["placement.constraints"] = strings.Join(c, ",")
		}
		if u := a.Update(); u != nil {
			f["deployment.update.parallelism"] = strconv.Itoa(u.Parallelism)
			f["deployment.update.delaySeconds"] = strconv.Itoa(u.DelaySeconds)
		}
		// Only stated fields compared, so console-set checks and timing survive a silent manifest.
		if hc := a.Healthcheck; hc != nil {
			f["healthcheck.type"] = hc.Type
			setIf := func(k, v string, stated bool) {
				if stated {
					f["healthcheck."+k] = v
				}
			}
			setIf("path", hc.Path, hc.Path != "")
			setIf("port", strconv.Itoa(hc.Port), hc.Port > 0)
			setIf("command", hc.Command, hc.Command != "")
			setIf("intervalSeconds", strconv.Itoa(hc.IntervalSeconds), hc.IntervalSeconds > 0)
			setIf("timeoutSeconds", strconv.Itoa(hc.TimeoutSeconds), hc.TimeoutSeconds > 0)
			setIf("retries", strconv.Itoa(hc.Retries), hc.Retries > 0)
			setIf("startPeriodSeconds", strconv.Itoa(hc.StartPeriodSeconds), hc.StartPeriodSeconds > 0)
		}
		// Config content is in no diffed field, so its fingerprint makes an edit converge.
		if a.ConfigFP != "" {
			f["configFP"] = a.ConfigFP
		}
		// Compared per key so a plan names the label that changed.
		for k, v := range a.ContainerLabels {
			if reservedLabelKey(k) {
				continue // stripped on write, so it can never be live: skip both sides
			}
			f["containerLabels."+k] = v
		}
		if a.Resources != nil {
			// Compare canonical numeric values so "512Mi" vs bytes or "0.5" vs nano-CPUs don't read as drift.
			mb, _ := a.Resources.MemoryBytes()
			nc, _ := a.Resources.NanoCPUs()
			if mb != 0 {
				f["resources.memory"] = fmt.Sprintf("%d", mb)
			}
			if nc != 0 {
				f["resources.cpu"] = fmt.Sprintf("%d", nc)
			}
			if a.Resources.GPU != 0 {
				f["resources.gpu"] = fmt.Sprintf("%d", a.Resources.GPU)
			}
			if a.Resources.GPUKind != "" {
				f["resources.gpuKind"] = a.Resources.GPUKind
			}
		}
		secret := map[string]bool{}
		for _, k := range a.SecretEnv {
			secret[k] = true
		}
		for k, v := range a.Env {
			if secret[k] {
				f["env."+k] = "(secret)" // never surface secret values in a plan
				continue
			}
			f["env."+k] = v
		}
		for _, p := range a.Ports {
			if p.ExternalAccess {
				f[fmt.Sprintf("port.%d.external", p.Container)] = "true"
			}
			if p.Publish || p.HostPort > 0 {
				f[fmt.Sprintf("port.%d.published", p.Container)] = "true"
			}
		}
	case r.Database != nil:
		f["engine"] = r.Database.Engine
		f["version"] = r.Database.Version
		if loc := r.Database.Location(); loc != "" {
			f["placement.location"] = loc
		}
		if ro := r.Database.ReadOnlyUser; ro != nil {
			f["readOnlyUser"] = strconv.FormatBool(*ro)
		}
		// "0" is stated too: it removes a limit.
		if res := r.Database.Resources; res != nil {
			if res.Memory != "" {
				mb, _ := res.MemoryBytes()
				f["resources.memory"] = strconv.FormatInt(mb, 10)
			}
			if res.CPU != "" {
				nc, _ := res.NanoCPUs()
				f["resources.cpu"] = strconv.FormatInt(nc, 10)
			}
			if res.Size != "" {
				f["resources.size"] = res.Size
			}
		}
	case r.Volume != nil:
		if loc := r.Volume.Location(); loc != "" {
			f["placement.location"] = loc
		}
		// Must be on both sides: desired-only makes phantom updates, actual-only disables the immutability refusal.
		if sc := strings.TrimSpace(r.Volume.StorageClass); sc != "" {
			f["storage.class"] = sc
		}
		// Canonical bytes, so "20Gi" matches the byte count the snapshot reports.
		if r.Volume.Size != "" {
			b, _ := r.Volume.SizeBytes()
			f["size"] = strconv.FormatInt(b, 10)
		}

	case r.Route != nil:
		hosts := append([]string(nil), r.Route.Hosts...)
		sort.Strings(hosts) // order-independent so reordering hosts isn't a change
		f["hosts"] = strings.Join(hosts, ",")
		f["app"] = r.Route.App
		f["path"] = r.Route.Path
		f["tls"] = r.Route.TLS
		f["certificate"] = r.Route.Certificate
		f["tlsProvider"] = r.Route.TLSProvider
		f["port"] = fmt.Sprintf("%d", r.Route.Port)
		f["rewrite"] = r.Route.Rewrite
		// The gateway matches methods by membership, so order is not a change.
		methods := append([]string(nil), r.Route.Methods...)
		sort.Strings(methods)
		f["methods"] = strings.Join(methods, ",")
		f["advancedConfig"] = r.Route.AdvancedConfig
		sec := r.Route.Security
		if sec == nil {
			sec = &RouteSecuritySpec{}
		}
		f["security.exploitProtection"] = fmt.Sprintf("%t", sec.ExploitProtection)
		// An omitted block and an explicitly disabled one must compare equal.
		mt := r.Route.Maintenance
		if mt == nil {
			mt = &RouteMaintenanceSpec{}
		}
		f["maintenance.enabled"] = fmt.Sprintf("%t", mt.Enabled)
		f["maintenance.statusCode"] = fmt.Sprintf("%d", mt.StatusCode)
		f["maintenance.message"] = mt.Message
		// Chain order is behaviour at the gateway, so it compares as an ordered list.
		f["middlewares"] = strings.Join(r.Route.Middlewares, ",")
	case r.Stack != nil:
		f["description"] = r.Stack.Description
		if loc := r.Stack.Location(); loc != "" {
			f["placement.location"] = loc
		}
		secret := map[string]bool{}
		for _, k := range r.Stack.SecretEnv {
			secret[k] = true
		}
		for k, v := range r.Stack.Env {
			if secret[k] {
				f["env."+k] = "(secret)" // never surface secret values in a plan
				continue
			}
			f["env."+k] = v
		}
	case r.Domain != nil:
		f["tls"] = r.Domain.TLS
		f["wildcard"] = fmt.Sprintf("%t", r.Domain.Wildcard)
	case r.CronJob != nil:
		cronJobFields(r.CronJob, f)
	case r.GitRepository != nil:
		g := r.GitRepository
		f["url"] = g.URL
		f["auth"] = g.Auth
		f["username"] = g.Username
		f["secretRef"] = g.SecretRef
	case r.GitSource != nil:
		g := r.GitSource
		f["repo"] = g.Repo
		f["repository"] = g.Repository
		f["ref"] = g.Ref
		f["path"] = g.Path
		f["syncPolicy"] = g.SyncPolicy
		f["prune"] = strconv.FormatBool(g.Prune)
		f["selfHeal"] = strconv.FormatBool(g.SelfHeal)
		f["allowEmpty"] = strconv.FormatBool(g.AllowEmpty)
		f["requireSealedSecrets"] = strconv.FormatBool(g.RequireSealedSecrets)
		f["deletePolicy"] = g.DeletePolicy
	}
	return f
}
