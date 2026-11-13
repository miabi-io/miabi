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

package models

import (
	"strconv"
	"strings"
)

// Keys under MetadataReservedPrefix are platform-managed and protected from user modification.
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
	// One-shot: the first deploy re-evaluates (container if the app holds node-local state), then clears it.
	MetaRuntimeAutoService = "miabi.io/runtime-auto-service"
	// Maps the manifest name to the exact logical database, since a manifest Database may share an instance.
	MetaDeclarativeName = "miabi.io/declarative-name"
	// MetaDeclarativeKind on an instance names the manifest kind that owns it, with MetaDeclarativeName.
	MetaDeclarativeKind = "miabi.io/declarative-kind"
	// MetaAdopted marks an instance a manifest took over rather than created, so removing the manifest releases it.
	MetaAdopted = "miabi.io/adopted"
	// Deploys pull image@digest while set; see Application.ImageRef.
	MetaDigest = "miabi.io/digest"
	// Set on a database instance provisioned for one dependency, so auto/shared placement never reuses it.
	MetaPlacement           = "miabi.io/placement"
	PlacementValueDedicated = "dedicated"

	// Owner is the parent whose lifecycle this follows, distinct from managed-by; stored with name to avoid a join.
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
	ManagedByGitOps      = "gitops"       // reconciled from a Git repository by a GitOps project
	ManagedByApply       = "apply"        // applied as a manifest through the CLI or API (miabi apply)
)

// SourceOwnedElsewhere is a handler-level rule; guarding in the service would block GitOps and the marketplace installer.
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
	OwnerUser            = "user"     // a person created it for their own use (owner id = user id)
	OwnerApp             = "app"      // it backs an application
	OwnerDatabase        = "database" // it backs a database instance
	OwnerStack           = "stack"    // it belongs to a stack
	OwnerTemplateInstall = "template-install"
)

// OwnerRef is the parsed owner reference attached to a resource's metadata.
type OwnerRef struct {
	Kind string `json:"kind"`           // one of OwnerKind*
	ID   uint   `json:"id,omitempty"`   // owning resource id (0 = none/not applicable)
	Name string `json:"name,omitempty"` // display name
}

// SetOwner omits id when 0 and name when empty, so callers can record a partial reference.
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

// DefaultOwner sets the owner only when absent, so callers that record a richer owner win.
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

// SanitizeUserMetadata strips reserved keys so user input can never spoof platform-managed keys.
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

// MergeUserMetadata keeps reserved keys from current and replaces the non-reserved ones wholesale.
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

func DefaultManagedBy(m Metadata, value string) Metadata {
	if m == nil {
		m = Metadata{}
	}
	if m[MetaManagedBy] == "" {
		m[MetaManagedBy] = value
	}
	return m
}

// SetBuiltin always wins since reserved keys are authoritative; pairs are key, value, key, value...
func SetBuiltin(m Metadata, kv ...string) Metadata {
	if m == nil {
		m = Metadata{}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}
