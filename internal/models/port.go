// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// AppPort is a container port an application exposes. Developer-managed; no
// approval is required to declare one.
type AppPort struct {
	ID            uint   `json:"id" gorm:"primaryKey"`
	ApplicationID uint   `json:"application_id" gorm:"index:idx_appport_unique,unique;not null"`
	ContainerPort int    `json:"container_port" gorm:"index:idx_appport_unique,unique;not null"`
	Protocol      string `json:"protocol" gorm:"index:idx_appport_unique,unique;not null;default:tcp"` // tcp | udp
	// Scheme is the application protocol the container speaks on this port
	// (http | https), used to build the Gateway backend URL. Transport stays in
	// Protocol; this is L7. Defaults to http.
	Scheme string `json:"scheme" gorm:"not null;default:http"`
	Name   string `json:"name"`
}

// PortBindingStatus is the review state of a host port binding request.
type PortBindingStatus string

const (
	PortBindingPending  PortBindingStatus = "pending"
	PortBindingApproved PortBindingStatus = "approved"
	PortBindingRejected PortBindingStatus = "rejected"
)

// PortBinding is a request to publish a container port on a host port. Because
// host ports are a node-wide shared resource, a binding is only published once a
// platform admin approves it.
type PortBinding struct {
	ID            uint              `json:"id" gorm:"primaryKey"`
	WorkspaceID   uint              `json:"workspace_id" gorm:"index;not null"`
	ApplicationID uint              `json:"application_id" gorm:"index;not null"`
	ContainerPort int               `json:"container_port" gorm:"not null"`
	Protocol      string            `json:"protocol" gorm:"not null;default:tcp"`
	HostPort      int               `json:"host_port" gorm:"not null"`
	Status        PortBindingStatus `json:"status" gorm:"not null;default:pending;index"`
	// ServerID is the node the host port is published on. Host ports are a
	// per-host resource, so conflict checks + allocation are scoped to it
	// (0 = the local/manager node). Backfilled from the owning app.
	ServerID    uint      `json:"server_id" gorm:"index;not null;default:0"`
	RequestedBy uint      `json:"requested_by"`
	ReviewedBy  *uint     `json:"reviewed_by,omitempty"`
	ReviewNote  string    `json:"review_note,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
