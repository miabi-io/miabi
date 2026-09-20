// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// The platform label contract lives in the stack module so the CLI applies exactly the labels
// the control plane recognises. These aliases keep the server's existing import path.

package docker

import stackdocker "github.com/miabi-io/miabi/pkg/stack/docker"

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
	RoleRegistry           = stackdocker.RoleRegistry
	RoleRegistryGC         = stackdocker.RoleRegistryGC
	ManagedByCompose       = stackdocker.ManagedByCompose
	ManagedByMiabi         = stackdocker.ManagedByMiabi
	ManagedByExternal      = stackdocker.ManagedByExternal
	PartOfMiabi            = stackdocker.PartOfMiabi
	ManagedLabel           = stackdocker.ManagedLabel
)

// SwarmServiceNameLabel is Docker's own label on a swarm task's container, naming the service that
// owns it. Swarm recreates such a container the moment it is removed, so it is the handle onto the
// only thing that can actually be reclaimed: the service.
const SwarmServiceNameLabel = "com.docker.swarm.service.name"

var (
	PlatformLabels     = stackdocker.PlatformLabels
	IsPlatformStack    = stackdocker.IsPlatformStack
	IsProtected        = stackdocker.IsProtected
	ManagedBy          = stackdocker.ManagedBy
	LabelValue         = stackdocker.LabelValue
	IsManaged          = stackdocker.IsManaged
	IsPlatformInfra    = stackdocker.IsPlatformInfra
	IsReservedLabelKey = stackdocker.IsReservedLabelKey
	SanitizeUserLabels = stackdocker.SanitizeUserLabels
	WorkspaceID        = stackdocker.WorkspaceID
)
