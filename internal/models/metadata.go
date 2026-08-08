// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"strconv"
	"strings"
)

// Metadata is a free-form set of key/value labels on a resource, backing provenance, grouping
// and the declarative/GitOps features. Keys under MetadataReservedPrefix are platform-managed
// and protected from user modification. Persisted as JSON.
type Metadata = map[string]string

// MetadataReservedPrefix marks built-in, platform-managed metadata keys.
const MetadataReservedPrefix = "miabi.io/"

// Built-in metadata keys (all under MetadataReservedPrefix).
const (
	MetaManagedBy       = "miabi.io/managed-by"       // origin: see ManagedBy* values
	MetaTemplate        = "miabi.io/template"         // marketplace template slug
	MetaTemplateVersion = "miabi.io/template-version" // marketplace template version
	MetaTemplateInstall = "miabi.io/template-install" // template install id
	MetaStack           = "miabi.io/stack"            // owning stack docker name
	MetaGitOpsSource    = "miabi.io/gitops-source"    // id of the GitOps project that created the resource
	// MetaRuntimeAutoService marks an app whose service runtime was auto-defaulted by cluster
	// mode rather than chosen. One-shot: the first deploy re-evaluates (downgrading to a
	// container if the app holds node-local state) then clears it. Value: "true".
	MetaRuntimeAutoService = "miabi.io/runtime-auto-service"
	// MetaDeclarativeName records the manifest resource name a logical database was provisioned
	// for. A manifest Database may be a dedicated instance or share one, so this maps the name
	// back to the exact logical database it owns instead of guessing.
	MetaDeclarativeName = "miabi.io/declarative-name"

	// Owner reference: the entity this resource belongs to, whose lifecycle it follows. Distinct
	// from managed-by (the mechanism of creation); owner is the parent, stored as kind+id+name so
	// the UI can link to it without a join. See OwnerKind* and SetOwner/Owner.
	MetaOwnerKind = "miabi.io/owner-kind" // one of OwnerKind* values
	MetaOwnerID   = "miabi.io/owner-id"   // numeric id of the owner (omitted when 0)
	MetaOwnerName = "miabi.io/owner-name" // owner display name (for rendering + linking)
)

// ManagedBy values record how a resource came to exist.
const (
	ManagedByUser        = "user"         // created by hand
	ManagedByMarketplace = "marketplace"  // installed from a marketplace template
	ManagedByStack       = "stack"        // created as part of a stack
	ManagedByStackImport = "stack-import" // created by importing a compose file
	ManagedByGitOps      = "gitops"       // created/reconciled by the declarative apply engine
)

// SourceOwnedElsewhere reports whether an application's source (its image or repository) is owned
// by an external source of truth, and by which. Editing it interactively would be overwritten — by
// the next GitOps sync — or would break the upgrade path a marketplace template depends on, so the
// interactive API refuses and points at the right place instead.
//
// This is a HANDLER-level rule, not a service one: the GitOps engine and the marketplace installer
// both write through the same service, and guarding there would block them from managing the very
// resources they own.
func SourceOwnedElsewhere(m Metadata) (owner string, owned bool) {
	switch m[MetaManagedBy] {
	case ManagedByMarketplace:
		return ManagedByMarketplace, true
	case ManagedByGitOps:
		return ManagedByGitOps, true
	}
	return "", false
}

// OwnerKind values classify what a resource belongs to (the MetaOwnerKind value).
const (
	OwnerUser     = "user"     // a person created it for their own use (owner id = user id)
	OwnerApp      = "app"      // it backs an application
	OwnerDatabase = "database" // it backs a database instance
	OwnerStack    = "stack"    // it belongs to a stack
)

// OwnerRef is the parsed owner reference attached to a resource's metadata.
type OwnerRef struct {
	Kind string `json:"kind"`           // one of OwnerKind*
	ID   uint   `json:"id,omitempty"`   // owning resource id (0 = none/not applicable)
	Name string `json:"name,omitempty"` // display name
}

// SetOwner records the owner reference on m (creating the map if needed) and
// returns it. id is omitted when 0 and name when empty, so callers can record a
// partial reference (e.g. kind+id with the name resolved later).
func SetOwner(m Metadata, kind string, id uint, name string) Metadata {
	if m == nil {
		m = Metadata{}
	}
	m[MetaOwnerKind] = kind
	if id > 0 {
		m[MetaOwnerID] = strconv.FormatUint(uint64(id), 10)
	} else {
		delete(m, MetaOwnerID)
	}
	if name != "" {
		m[MetaOwnerName] = name
	} else {
		delete(m, MetaOwnerName)
	}
	return m
}

// Owner parses the owner reference from m. ok is false when no owner kind is set.
func Owner(m Metadata) (OwnerRef, bool) {
	if m == nil || m[MetaOwnerKind] == "" {
		return OwnerRef{}, false
	}
	id, _ := strconv.ParseUint(m[MetaOwnerID], 10, 64)
	return OwnerRef{Kind: m[MetaOwnerKind], ID: uint(id), Name: m[MetaOwnerName]}, true
}

// DefaultOwner sets the owner reference only when one is not already present, so
// higher-level callers (marketplace, stacks, apply) that record a richer owner
// win over a creation-path default. Returns the (possibly updated) map.
func DefaultOwner(m Metadata, kind string, id uint, name string) Metadata {
	if _, ok := Owner(m); ok {
		return m
	}
	return SetOwner(m, kind, id, name)
}

// IsReservedMetadataKey reports whether key is platform-managed (built-in).
func IsReservedMetadataKey(key string) bool {
	return strings.HasPrefix(key, MetadataReservedPrefix)
}

// SanitizeUserMetadata returns a copy of in with all reserved (built-in) keys
// removed, so user-supplied metadata can never set or spoof platform-managed
// keys. nil in → nil out.
func SanitizeUserMetadata(in Metadata) Metadata {
	if in == nil {
		return nil
	}
	out := make(Metadata, len(in))
	for k, v := range in {
		if IsReservedMetadataKey(k) {
			continue
		}
		out[k] = v
	}
	return out
}

// MergeUserMetadata applies a user overlay onto current metadata while protecting built-in
// keys: reserved keys from current are preserved, non-reserved keys are replaced wholesale so
// users can add or remove their own labels.
func MergeUserMetadata(current, overlay Metadata) Metadata {
	out := make(Metadata, len(current)+len(overlay))
	for k, v := range current {
		if IsReservedMetadataKey(k) {
			out[k] = v
		}
	}
	for k, v := range overlay {
		if IsReservedMetadataKey(k) {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// DefaultManagedBy ensures the managed-by built-in is set, defaulting to value
// when absent, and returns the (possibly newly created) map. Used at creation so
// every resource records its origin.
func DefaultManagedBy(m Metadata, value string) Metadata {
	if m == nil {
		m = Metadata{}
	}
	if m[MetaManagedBy] == "" {
		m[MetaManagedBy] = value
	}
	return m
}

// SetBuiltin sets one or more reserved (built-in) key/value pairs on m, creating
// the map if needed, and returns it. Reserved keys are authoritative, so this
// always wins. Pairs are passed as key, value, key, value, …
func SetBuiltin(m Metadata, kv ...string) Metadata {
	if m == nil {
		m = Metadata{}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}
