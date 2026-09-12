// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"errors"
	"regexp"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// ErrInvalidLocationCode is returned for a location code that is not a short lowercase slug.
var ErrInvalidLocationCode = errors.New("the location code must be lowercase letters, digits and hyphens (max 32), e.g. eu-central")

// ErrInvalidVisibility is returned for a visibility other than "all" or "restricted".
var ErrInvalidVisibility = errors.New("visibility must be all or restricted")

var locationCodePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// ClusterPatch edits a cluster's tenant-facing identity and who may place into it. Nil fields are left unchanged.
type ClusterPatch struct {
	DisplayName  *string
	LocationCode *string
	Visibility   *models.ClusterVisibility
	Cordoned     *bool
}

// Clusters lists every cluster, the default first, with its node count.
func (s *Service) Clusters() ([]models.Cluster, error) {
	if s.store == nil {
		return []models.Cluster{}, nil
	}
	return s.store.List()
}

// Cluster returns one cluster with its node count; id 0 is the default cluster.
func (s *Service) Cluster(id uint) (*models.Cluster, error) {
	list, err := s.Clusters()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id || (id == models.DefaultClusterID && list[i].IsDefault) {
			return &list[i], nil
		}
	}
	return nil, ErrClusterNotFound
}

// ClusterIDByUID resolves a cluster's uid to its id.
func (s *Service) ClusterIDByUID(uid string) (uint, error) {
	if s.store == nil {
		return 0, ErrClusterNotFound
	}
	return s.store.IDByUID(uid)
}

// UpdateCluster renames a cluster, changes its location code, or changes who may place into it.
func (s *Service) UpdateCluster(id uint, p ClusterPatch) (*models.Cluster, error) {
	c, err := s.Cluster(id)
	if err != nil {
		return nil, err
	}
	cols := map[string]any{}
	if p.DisplayName != nil {
		name := strings.TrimSpace(*p.DisplayName)
		if len(name) > maxClusterNameLen {
			return nil, ErrNameTooLong
		}
		cols["display_name"] = name
	}
	if p.LocationCode != nil {
		code := strings.TrimSpace(*p.LocationCode)
		if code != "" && !locationCodePattern.MatchString(code) {
			return nil, ErrInvalidLocationCode
		}
		cols["location_code"] = code
	}
	if p.Visibility != nil {
		if !models.ValidClusterVisibility(*p.Visibility) {
			return nil, ErrInvalidVisibility
		}
		cols["visibility"] = *p.Visibility
	}
	if p.Cordoned != nil {
		cols["cordoned"] = *p.Cordoned
	}
	if len(cols) > 0 {
		if err := s.store.UpdateColumns(c.ID, cols); err != nil {
			return nil, err
		}
	}
	return s.Cluster(c.ID)
}
