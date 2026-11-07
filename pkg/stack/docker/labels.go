// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"strconv"
	"strings"
)

// The single source of truth for io.miabi.* label keys; never hand-write the literals elsewhere.
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

	// The stack is deployed from outside Miabi, so without these Miabi cannot recognize its own components.

	// One exact-match key, so the whole stack is a single Docker label filter.
	LabelPartOf = "io.miabi.part-of"
	// Recreating a compose-owned container out-of-band is silently reverted by the next `docker compose up -d`.
	LabelManagedBy = "io.miabi.managed-by"
	// Declared, never inferred: not all infra is undeletable, and a role allowlist would drift.
	LabelProtected = "io.miabi.protected"
	// Lets `miabi install` detect changes without re-deriving Docker's normalization.
	LabelSpecHash = "io.miabi.spec-hash"
)

const PartOfMiabi = "miabi"

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
	// RoleMetadataGuard is the per-node helper that keeps containers off the cloud metadata service.
	RoleMetadataGuard = "metadata-guard"
	// The label, not the name, guards it, so renaming the network can't make it attachable from app APIs.
	RolePlatformInternal = "platform-internal"
	// Reuses the control-plane value because networks were labelled that way first and Docker can't relabel.
	RoleProxyNetwork = RoleControlPlane
	RoleRegistry     = "registry"
	RoleRegistryGC   = "registry-gc" // transient: deliberately NOT protected
)

// Lifecycle owners for LabelManagedBy.
const (
	// Version changes must also be written to the compose env, or `docker compose up -d` reverts them.
	ManagedByCompose = "compose"
	// ManagedByMiabi: Miabi created it and may freely recreate it.
	ManagedByMiabi    = "miabi"
	ManagedByExternal = "external"
)

const ManagedLabel = LabelManaged

// Use it so a component can never be half-labeled; extra may not override the four keys set here.
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

func IsPlatformStack(labels map[string]string) bool {
	v, _ := LabelValue(labels, LabelPartOf)
	return v == PartOfMiabi
}

// The labels are the test, never the name; name-only callers must resolve and match the id too.
func IsPlatformNetwork(labels map[string]string) bool {
	return IsPlatformStack(labels) || IsPlatformInfra(labels)
}

// Infra means "do not reclaim as an orphan"; protected means "do not let a human break it by accident".
func IsProtected(labels map[string]string) bool {
	v, _ := LabelValue(labels, LabelProtected)
	return v == "true"
}

// "" when unlabeled, e.g. a stack installed before platform labels existed.
func ManagedBy(labels map[string]string) string {
	v, _ := LabelValue(labels, LabelManagedBy)
	return v
}

func LabelValue(labels map[string]string, key string) (value string, ok bool) {
	if labels == nil {
		return "", false
	}
	v, ok := labels[key]
	return v, ok
}

func IsManaged(labels map[string]string) bool {
	for k := range labels {
		if strings.HasPrefix(k, LabelPrefix) {
			return true
		}
	}
	return false
}

func IsPlatformInfra(labels map[string]string) bool {
	_, ok := LabelValue(labels, LabelRole)
	return ok
}

// A spoofed io.miabi.workspace could break isolation; com.docker. keys are managed by stackLabels.
var reservedUserLabelPrefixes = []string{LabelPrefix, "com.docker."}

func IsReservedLabelKey(key string) bool {
	for _, p := range reservedUserLabelPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

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
