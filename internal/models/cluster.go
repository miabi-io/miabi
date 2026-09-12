// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

// DefaultClusterID stands for the default cluster wherever a cluster id is 0, the way ServerID 0
// stands for the local node.
const DefaultClusterID uint = 0

// ClusterMode is how a cluster runs its workloads.
type ClusterMode string

const (
	// ClusterModeStandalone is a single plain-Docker node.
	ClusterModeStandalone ClusterMode = "standalone"
	// ClusterModeSwarm is one Docker Swarm of one or more nodes.
	ClusterModeSwarm ClusterMode = "swarm"
)

// ClusterVisibility decides which workspaces may place resources in a cluster.
type ClusterVisibility string

const (
	ClusterVisibilityAll        ClusterVisibility = "all"
	ClusterVisibilityRestricted ClusterVisibility = "restricted"
)

// ValidClusterVisibility reports whether v is a known visibility.
func ValidClusterVisibility(v ClusterVisibility) bool {
	return v == ClusterVisibilityAll || v == ClusterVisibilityRestricted
}

// Cluster is a deploy target: nodes that share private networking and one ingress. Tenants see it
// as a location. Every node belongs to exactly one cluster; the default one holds the control plane.
type Cluster struct {
	UIDModel
	ID           uint        `json:"id" gorm:"primaryKey"`
	Name         string      `json:"name" gorm:"uniqueIndex;not null"`
	DisplayName  string      `json:"display_name"`
	LocationCode string      `json:"location_code,omitempty"`
	Mode         ClusterMode `json:"mode" gorm:"not null;default:standalone"`
	IsDefault    bool        `json:"is_default" gorm:"not null;default:false"`
	// ManagerServerID is the node Swarm and engine calls go through; 0 is the local socket.
	ManagerServerID uint   `json:"manager_server_id"`
	AgentTokenHash  string `json:"-"`
	// IngressServerID is the node running the cluster gateway; 0 is the local node.
	IngressServerID uint              `json:"ingress_server_id"`
	IngressIP       string            `json:"ingress_ip,omitempty"`
	IngressHostname string            `json:"ingress_hostname,omitempty"`
	Visibility      ClusterVisibility `json:"visibility" gorm:"not null;default:all"`
	Cordoned        bool              `json:"cordoned" gorm:"not null;default:false"`
	// LegacyIngress marks a cluster converted from a port-forward node: the central gateway still
	// reaches its apps by host port until it gets a gateway of its own or joins a swarm.
	LegacyIngress bool      `json:"legacy_ingress" gorm:"not null;default:false"`
	NodeCount     int64     `json:"node_count" gorm:"-"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Label is the cluster's display name, falling back to its handle.
func (c *Cluster) Label() string {
	if c == nil {
		return ""
	}
	if l := strings.TrimSpace(c.DisplayName); l != "" {
		return l
	}
	return c.Name
}

// IngressNode is the node whose gateway serves the cluster, falling back to its manager.
func (c *Cluster) IngressNode() uint {
	if c.IngressServerID != 0 {
		return c.IngressServerID
	}
	return c.ManagerServerID
}

// clusterOfServer resolves the cluster of the node a new resource lands on (0 = the local node). A
// failed lookup leaves DefaultClusterID, which resolves to the default cluster anyway.
func clusterOfServer(tx *gorm.DB, serverID uint) uint {
	q := tx.Session(&gorm.Session{NewDB: true}).Table("servers").Select("cluster_id")
	if serverID == 0 {
		q = q.Where("is_local = ?", true)
	} else {
		q = q.Where("id = ?", serverID)
	}
	var id uint
	if err := q.Limit(1).Scan(&id).Error; err != nil {
		return DefaultClusterID
	}
	return id
}

func defaultClusterID(tx *gorm.DB) uint {
	var id uint
	err := tx.Session(&gorm.Session{NewDB: true}).Table("clusters").Select("id").
		Where("is_default = ?", true).Limit(1).Scan(&id).Error
	if err != nil {
		return DefaultClusterID
	}
	return id
}

func (a *Application) BeforeCreate(tx *gorm.DB) error {
	if a.ClusterID == DefaultClusterID {
		a.ClusterID = clusterOfServer(tx, a.ServerID)
	}
	return a.UIDModel.BeforeCreate(tx)
}

func (i *DatabaseInstance) BeforeCreate(tx *gorm.DB) error {
	if i.ClusterID == DefaultClusterID {
		i.ClusterID = clusterOfServer(tx, i.ServerID)
	}
	return i.UIDModel.BeforeCreate(tx)
}

func (v *Volume) BeforeCreate(tx *gorm.DB) error {
	if v.ClusterID == DefaultClusterID {
		v.ClusterID = clusterOfServer(tx, v.ServerID)
	}
	return v.UIDModel.BeforeCreate(tx)
}

func (j *Job) BeforeCreate(tx *gorm.DB) error {
	if j.ClusterID == DefaultClusterID {
		j.ClusterID = clusterOfServer(tx, j.ServerID)
	}
	return nil
}

func (s *Stack) BeforeCreate(tx *gorm.DB) error {
	if s.ClusterID == DefaultClusterID {
		s.ClusterID = defaultClusterID(tx)
	}
	return s.UIDModel.BeforeCreate(tx)
}
