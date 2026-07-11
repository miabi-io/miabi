// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"fmt"
	"sort"
	"strings"
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

// Plan is an ordered set of changes that converges actual state to desired.
// Creates/updates run in dependency order (volumes/dbs before apps before
// domains); deletes run in reverse.
type Plan struct {
	Changes []Change `json:"changes"`
}

// LabelManagedBy is the Meta.Labels key that records which subsystem owns an
// actual resource. The plan engine uses it to keep prune from ever deleting a
// resource the apply engine did not create.
const LabelManagedBy = "miabi.io/managed-by"

// LabelGitOpsSource is the Meta.Labels key recording which GitOps project (source
// id) created an actual resource, so a single project's resources can be listed
// or torn down without touching another project's.
const LabelGitOpsSource = "miabi.io/gitops-source"

// PlanOptions tunes plan generation.
type PlanOptions struct {
	// Prune emits deletes for actual resources absent from desired.
	Prune bool
	// PruneManagedBy, when non-empty, restricts prune to actual resources whose
	// LabelManagedBy label equals it — so a GitOps prune never deletes a
	// hand-created or otherwise-owned resource. Empty prunes any orphan.
	PruneManagedBy string
	// PruneGitOpsSource, when non-empty, restricts prune to actual resources whose
	// LabelGitOpsSource label equals it — so one GitOps project's reconcile only
	// prunes its own resources and never another project's (which live in the same
	// workspace snapshot). Empty leaves prune unscoped by source. Resources without
	// a source label (e.g. hand-created or pre-labeling) are never matched, so a
	// scoped prune never deletes them.
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

// applyRank orders kinds by dependency: a resource of lower rank is created
// before one that may depend on it. Projects are organizational and excluded
// from plans entirely.
func applyRank(k Kind) int {
	switch k {
	case KindSecret, KindVolume, KindDomain:
		// Independent, foundational resources. Domains come before the routes that
		// bind hostnames under them.
		return 0
	case KindDatabase, KindStack:
		return 1
	case KindApplication:
		return 2
	case KindRoute:
		return 3
	default:
		return 9
	}
}

func planned(k Kind) bool { return k != KindProject }

// matchActual finds the live resource a desired one refers to: by uid when the
// desired carries metadata.uid (rename-safe), else by kind+name. Returns the
// match and whether it exists.
func matchActual(actual *ResourceSet, byUID map[string]Resource, d Resource) (Resource, bool) {
	if d.Metadata.UID != "" {
		if r, ok := byUID[string(d.Kind)+"/"+d.Metadata.UID]; ok {
			return r, true
		}
	}
	return actual.Get(d.Key())
}

// BuildPlan diffs desired against actual and returns an ordered convergence
// plan. actual should contain only Miabi-managed resources when Prune is
// set, so unmanaged resources are never deleted.
func BuildPlan(desired, actual *ResourceSet, opts PlanOptions) *Plan {
	plan := &Plan{}

	// Index actual resources by uid so a desired resource carrying metadata.uid
	// matches its live counterpart even if the name changed (rename-safe), instead
	// of a destructive delete+create. Hand-authored manifests have no uid and fall
	// back to name matching, so their behavior is unchanged.
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

// sortChanges orders creates/updates by ascending dependency rank and deletes by
// descending rank, so dependencies exist before dependents and are torn down
// after them. Ties break by kind then name for a stable, readable plan.
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

// group keeps deletes ahead of creates/updates in the overall ordering so a
// removed dependent is torn down before a renamed dependency is created.
func group(a Action) int {
	if a == ActionDelete {
		return 0
	}
	return 1
}

// diffFields compares the actual and desired forms of a resource and returns the
// changed fields. Secret values are write-only and never diffed (presence only).
func diffFields(actual, desired Resource) []FieldDiff {
	if desired.Kind == KindSecret {
		return nil // opaque: an existing secret is treated as in-sync
	}
	av := specFields(actual)
	dv := specFields(desired)
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

// specFields flattens a resource's comparable spec into field->value. Fields
// named in an application's secretEnv are masked so secret values never appear
// in a plan, while still letting presence be compared.
func specFields(r Resource) map[string]string {
	f := map[string]string{}
	switch {
	case r.Application != nil:
		// Apply v1 converges the fields it can map back from live state without
		// ambiguity: the image identity, command, resource caps, and non-secret
		// env. Structural create-time attributes (ports, mounts, stack) are not
		// diffed, so a converged app never shows phantom drift.
		a := r.Application
		f["image"] = a.Image
		f["tag"] = a.Tag
		f["digest"] = a.Digest
		f["command"] = strings.Join(a.Command, " ")
		if a.Resources != nil {
			// Compare resource caps by their canonical numeric value so "512Mi"
			// and the live byte count (or "0.5" vs nano-CPUs) don't read as drift.
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
		// Port exposure is presence-based so it converges without phantom drift:
		// the exact auto-allocated host port / generated subdomain is create-time
		// state that the snapshot can't pin back to the manifest.
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
	case r.Volume != nil:
		// Volumes are presence-only: size is fixed at create time.
	case r.Route != nil:
		hosts := append([]string(nil), r.Route.Hosts...)
		sort.Strings(hosts) // order-independent so reordering hosts isn't a change
		f["hosts"] = strings.Join(hosts, ",")
		f["app"] = r.Route.App
		f["path"] = r.Route.Path
		f["tls"] = r.Route.TLS
		f["port"] = fmt.Sprintf("%d", r.Route.Port)
	case r.Stack != nil:
		f["description"] = r.Stack.Description
	case r.Domain != nil:
		f["tls"] = r.Domain.TLS
		f["wildcard"] = fmt.Sprintf("%t", r.Domain.Wildcard)
	}
	return f
}
