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

import "time"

type AppPort struct {
	ID            uint   `json:"id" gorm:"primaryKey"`
	ApplicationID uint   `json:"application_id" gorm:"index:idx_appport_unique,unique;not null"`
	ContainerPort int    `json:"container_port" gorm:"index:idx_appport_unique,unique;not null"`
	Protocol      string `json:"protocol" gorm:"index:idx_appport_unique,unique;not null;default:tcp"` // tcp | udp
	// L7 protocol used for the Gateway backend URL; transport stays in Protocol.
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

// PortBinding is published only after admin approval, since host ports are a node-wide shared resource.
type PortBinding struct {
	ID            uint              `json:"id" gorm:"primaryKey"`
	WorkspaceID   uint              `json:"workspace_id" gorm:"index;not null"`
	ApplicationID uint              `json:"application_id" gorm:"index;not null"`
	ContainerPort int               `json:"container_port" gorm:"not null"`
	Protocol      string            `json:"protocol" gorm:"not null;default:tcp"`
	HostPort      int               `json:"host_port" gorm:"not null"`
	Status        PortBindingStatus `json:"status" gorm:"not null;default:pending;index"`
	// Conflict checks and allocation are scoped per host (0 = local/manager node).
	ServerID    uint   `json:"server_id" gorm:"index;not null;default:0"`
	RequestedBy uint   `json:"requested_by"`
	ReviewedBy  *uint  `json:"reviewed_by,omitempty"`
	ReviewNote  string `json:"review_note,omitempty"`
	// The host-port policy exempts these: the admin chose to adopt the port.
	AdminAdopted bool      `json:"admin_adopted" gorm:"not null;default:false"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
