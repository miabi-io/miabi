// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"strconv"
	"strings"
)

// Platform Docker label keys. Every container / volume / service Miabi creates
// carries a back-reference label to its owning record under the io.miabi.*
// namespace (reverse-DNS of miabi.io). These are the single source of truth —
// do not hand-write the string literals elsewhere.
const (
	// LabelPrefix namespaces every platform-applied Docker label.
	LabelPrefix = "io.miabi."

	LabelApp         = "io.miabi.app"          // application id
	LabelDeployment  = "io.miabi.deployment"   // deployment id
	LabelDatabase    = "io.miabi.database"     // database instance id
	LabelStack       = "io.miabi.stack"        // stack id
	LabelVolume      = "io.miabi.volume"       // volume id
	LabelJob         = "io.miabi.job"          // one-shot job container (transient)
	LabelRole        = "io.miabi.role"         // platform-infra role (node-gateway, …)
	LabelNode        = "io.miabi.node"         // node slug
	LabelWorkspace   = "io.miabi.workspace"    // owning workspace id
	LabelManaged     = "io.miabi.managed"      // "true" on managed raw resources/services
	LabelPipelineRun = "io.miabi.pipeline-run" // pipeline run id (transient)
	LabelSizeBytes   = "io.miabi.size_bytes"   // volume size hint

	// --- the platform stack itself ---------------------------------------------
	//
	// The labels above describe what Miabi CREATES. The three below describe what
	// Miabi IS: the control plane, its Postgres, its Redis, the central gateway and
	// the node agents. That stack is deployed by examples/compose/compose.yaml — from OUTSIDE
	// Miabi — so without these it carries no platform identity at all, and Miabi
	// cannot recognize its own components when it enumerates the engine.

	// LabelPartOf marks a resource as part of the Miabi platform stack
	// (PartOfMiabi). One exact-match key, so the whole stack is a single Docker
	// label filter rather than a scan against a list of roles.
	LabelPartOf = "io.miabi.part-of"
	// LabelManagedBy names who owns the resource's LIFECYCLE — which is not the same
	// question as who it belongs to. A compose-owned container may be observed and
	// updated in place, but recreating it out-of-band is silently reverted by the
	// next `docker compose up -d`.
	LabelManagedBy = "io.miabi.managed-by"
	// LabelProtected ("true") refuses ad-hoc destructive operations from the generic
	// containers list. Declared, never inferred: platform infrastructure is not
	// automatically undeletable (a transient GC container is infra too), and a role
	// allowlist in the guard would drift from the roles themselves.
	LabelProtected = "io.miabi.protected"
	// LabelSpecHash fingerprints the run spec a platform component was created from,
	// so `miabi install` can tell "already what the manifest asks for" from "changed"
	// without re-deriving Docker's own normalization of the spec. See
	// services/platformstack.
	LabelSpecHash = "io.miabi.spec-hash"
)

// PartOfMiabi is the only value LabelPartOf takes today. Named so call sites read
// as an identity check rather than a string compare.
const PartOfMiabi = "miabi"

// Roles carried by LabelRole. The platform-stack roles are new; the infra roles
// were already in use as string literals at their call sites and are named here so
// there is one spelling of each.
const (
	// Platform stack (examples/compose/compose.yaml + the agent).
	RoleControlPlane       = "control-plane"
	RolePlatformDB         = "platform-db"
	RolePlatformCache      = "platform-cache"
	RoleGateway            = "gateway" // the central, compose-managed gateway
	RoleAgent              = "agent"
	RoleControlPlaneWorker = "worker" // a split-out `command: ["worker"]` service

	// Infrastructure Miabi provisions itself.
	RoleNodeGateway      = "node-gateway"
	RoleNodeGatewayRedis = "node-gateway-redis"
	RoleRegistry         = "registry"
	RoleRegistryGC       = "registry-gc" // transient: deliberately NOT protected
)

// Lifecycle owners for LabelManagedBy.
const (
	// ManagedByCompose: created by examples/compose/compose.yaml. Miabi may read it and update
	// it in place, but must write any version change back to the compose env too —
	// otherwise `docker compose up -d` reverts the upgrade.
	ManagedByCompose = "compose"
	// ManagedByMiabi: Miabi created it and may freely recreate it.
	ManagedByMiabi = "miabi"
	// ManagedByExternal: installed out-of-band (install-agent.sh on a node Miabi
	// does not otherwise control).
	ManagedByExternal = "external"
)

// ManagedLabel marks resources created by Miabi (kept as a named alias for the
// many call sites that tag raw containers/volumes/services).
const ManagedLabel = LabelManaged

// PlatformLabels is the label set every Miabi platform component carries. Use it
// rather than hand-writing the keys, so a component can never end up half-labeled
// — e.g. discoverable but not protected.
//
// extra is merged last and may add component-specific keys (a node slug, an owning
// workspace); it may not override the four keys set here.
func PlatformLabels(role, managedBy string, extra map[string]string) map[string]string {
	l := make(map[string]string, len(extra)+4)
	for k, v := range extra {
		l[k] = v
	}
	l[LabelPartOf] = PartOfMiabi
	l[LabelRole] = role
	l[LabelManagedBy] = managedBy
	l[LabelProtected] = "true"
	return l
}

// IsPlatformStack reports whether a resource is part of Miabi's own stack — the
// control plane, its database/cache, the central gateway, an agent. Such resources
// are never offered for import (Miabi would end up "managing" its own database) and
// are never treated as a user's container.
func IsPlatformStack(labels map[string]string) bool {
	v, _ := LabelValue(labels, LabelPartOf)
	return v == PartOfMiabi
}

// IsProtected reports whether destructive operations (stop/restart/remove) must be
// refused on a resource. Distinct from IsPlatformInfra: infra is "do not reclaim as
// an orphan", protected is "do not let a human break the platform by accident".
func IsProtected(labels map[string]string) bool {
	v, _ := LabelValue(labels, LabelProtected)
	return v == "true"
}

// ManagedBy returns the resource's lifecycle owner (compose / miabi / external), or
// "" when unlabeled — e.g. a stack installed before platform labels existed.
func ManagedBy(labels map[string]string) string {
	v, _ := LabelValue(labels, LabelManagedBy)
	return v
}

// LabelValue reads a platform label by its io.miabi.* key. ok is false when the
// label is absent.
func LabelValue(labels map[string]string, key string) (value string, ok bool) {
	if labels == nil {
		return "", false
	}
	v, ok := labels[key]
	return v, ok
}

// IsManaged reports whether a resource carries any platform label — i.e. it is
// owned by Miabi and must not be a blanket prune/delete target.
func IsManaged(labels map[string]string) bool {
	for k := range labels {
		if strings.HasPrefix(k, LabelPrefix) {
			return true
		}
	}
	return false
}

// IsPlatformInfra reports whether a resource is platform infrastructure (carries
// a role label) — the node's edge gateway / its Redis. Such resources are
// managed through their own pages, never reclaimed or hidden as "someone else's".
func IsPlatformInfra(labels map[string]string) bool {
	_, ok := LabelValue(labels, LabelRole)
	return ok
}

// reservedUserLabelPrefixes are label namespaces a user may never write on their
// own containers: the platform's own keys (ownership / workspace scoping /
// housekeeping all read them, so a spoofed io.miabi.workspace could break
// isolation) and the Docker Compose grouping keys stackLabels manages.
var reservedUserLabelPrefixes = []string{LabelPrefix, "com.docker."}

// IsReservedLabelKey reports whether key is platform-reserved — i.e. a user is
// not allowed to set it as a custom container label.
func IsReservedLabelKey(key string) bool {
	for _, p := range reservedUserLabelPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// SanitizeUserLabels returns a copy of in with every reserved key removed, so
// user-supplied container labels can never set or spoof a platform label. Keys
// with an empty name are dropped. nil in → nil out.
func SanitizeUserLabels(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if k == "" || IsReservedLabelKey(k) {
			continue
		}
		out[k] = v
	}
	return out
}

// WorkspaceID returns the owning workspace id encoded in a resource's labels.
// ok is false when there is no (valid) workspace label — e.g. raw/system
// containers or platform infrastructure.
func WorkspaceID(labels map[string]string) (id uint, ok bool) {
	v, present := LabelValue(labels, LabelWorkspace)
	if !present {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return uint(n), true
}
