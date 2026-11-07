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

// The label contract lives in the stack module so the CLI applies exactly the labels the control plane recognises.

package docker

import (
	"strings"

	stackdocker "github.com/miabi-io/miabi/pkg/stack/docker"
)

const (
	LabelPrefix            = stackdocker.LabelPrefix
	LabelApp               = stackdocker.LabelApp
	LabelDeployment        = stackdocker.LabelDeployment
	LabelDatabase          = stackdocker.LabelDatabase
	LabelStack             = stackdocker.LabelStack
	LabelVolume            = stackdocker.LabelVolume
	LabelJob               = stackdocker.LabelJob
	LabelRole              = stackdocker.LabelRole
	LabelNode              = stackdocker.LabelNode
	LabelWorkspace         = stackdocker.LabelWorkspace
	LabelManaged           = stackdocker.LabelManaged
	LabelPipelineRun       = stackdocker.LabelPipelineRun
	LabelSizeBytes         = stackdocker.LabelSizeBytes
	LabelPartOf            = stackdocker.LabelPartOf
	LabelManagedBy         = stackdocker.LabelManagedBy
	LabelProtected         = stackdocker.LabelProtected
	LabelSpecHash          = stackdocker.LabelSpecHash
	RoleControlPlane       = stackdocker.RoleControlPlane
	RolePlatformDB         = stackdocker.RolePlatformDB
	RolePlatformCache      = stackdocker.RolePlatformCache
	RoleGateway            = stackdocker.RoleGateway
	RoleAgent              = stackdocker.RoleAgent
	RoleControlPlaneWorker = stackdocker.RoleControlPlaneWorker
	RoleNodeGateway        = stackdocker.RoleNodeGateway
	RoleNodeGatewayRedis   = stackdocker.RoleNodeGatewayRedis
	RolePlatformInternal   = stackdocker.RolePlatformInternal
	RoleProxyNetwork       = stackdocker.RoleProxyNetwork
	RoleMetadataGuard      = stackdocker.RoleMetadataGuard
	RoleRegistry           = stackdocker.RoleRegistry
	RoleRegistryGC         = stackdocker.RoleRegistryGC
	ManagedByCompose       = stackdocker.ManagedByCompose
	ManagedByMiabi         = stackdocker.ManagedByMiabi
	ManagedByExternal      = stackdocker.ManagedByExternal
	PartOfMiabi            = stackdocker.PartOfMiabi
	ManagedLabel           = stackdocker.ManagedLabel
)

// Swarm recreates a task container the moment it is removed, so the service is what can be reclaimed.
const SwarmServiceNameLabel = "com.docker.swarm.service.name"

var (
	PlatformLabels     = stackdocker.PlatformLabels
	IsPlatformStack    = stackdocker.IsPlatformStack
	IsProtected        = stackdocker.IsProtected
	ManagedBy          = stackdocker.ManagedBy
	LabelValue         = stackdocker.LabelValue
	IsManaged          = stackdocker.IsManaged
	IsPlatformInfra    = stackdocker.IsPlatformInfra
	IsPlatformNetwork  = stackdocker.IsPlatformNetwork
	IsReservedLabelKey = stackdocker.IsReservedLabelKey
	SanitizeUserLabels = stackdocker.SanitizeUserLabels
	WorkspaceID        = stackdocker.WorkspaceID
)

// NetworkRefMatches accepts name, full id or short id, since a name-only guard is bypassed by passing the id.
func NetworkRefMatches(n Network, ref string) bool {
	if ref == "" {
		return false
	}
	return n.Name == ref || n.ID == ref || (len(n.ID) >= 12 && len(ref) >= 12 && strings.HasPrefix(n.ID, ref))
}
