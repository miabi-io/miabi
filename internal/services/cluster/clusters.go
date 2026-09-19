// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"

	"github.com/jkaninda/logger"
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
	// ExternalBaseDomain and ExternalCertProvider set where generated app URLs live; a change re-hosts them.
	ExternalBaseDomain   *string
	ExternalCertProvider *string
	// ServiceEndpointMode switches how the cluster's service apps are reached by name; running services follow.
	ServiceEndpointMode *models.ServiceEndpointMode
	// IngressIP and IngressHostname are where the cluster's public DNS records point.
	IngressIP       *string
	IngressHostname *string
	// OrganizationID dedicates the cluster to one organization; 0 releases it back to shared. Set it
	// together with Visibility "organization" — the handler keeps the two in step.
	OrganizationID *uint
}

// clearStaleDefaults drops workspace default locations that dedicating a cluster just invalidated.
// Best-effort: placement already skips a default it may not use, so a failure here leaves a stale
// row rather than a broken deploy.
func (s *Service) clearStaleDefaults() {
	if s.store == nil {
		return
	}
	n, err := s.store.ClearUnusableWorkspaceDefaults()
	if err != nil {
		logger.Warn("could not clear workspace default locations invalidated by the dedication", "error", err)
		return
	}
	if n > 0 {
		logger.Info("cleared workspace default locations the organization can no longer place in", "workspaces", n)
	}
}

// foreignWorkloads counts what other organizations still run in a cluster.
func (s *Service) foreignWorkloads(clusterID, orgID uint) (int64, error) {
	if s.store == nil {
		return 0, nil
	}
	return s.store.CountForeignWorkloads(clusterID, orgID)
}

// Clusters lists every cluster, the default first, with its node count.
func (s *Service) Clusters() ([]models.Cluster, error) {
	if s.store == nil {
		return []models.Cluster{}, nil
	}
	list, err := s.store.List()
	if err != nil {
		return nil, err
	}
	s.markExternalPins(list)
	return list, nil
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

// UpdateCluster renames a cluster, changes its location code, who may place into it, or its external access.
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
	if p.OrganizationID != nil {
		if *p.OrganizationID != 0 && c.OrganizationID == nil {
			// Dedicating hides the cluster from every other tenant. Their workloads would keep
			// running here while their workspace could no longer see, scale or place beside them —
			// so refuse rather than strand them.
			if n, err := s.foreignWorkloads(c.ID, *p.OrganizationID); err != nil {
				return nil, err
			} else if n > 0 {
				return nil, fmt.Errorf("%w: %d still here", ErrClusterHasForeignWorkloads, n)
			}
		}
		if *p.OrganizationID == 0 {
			cols["organization_id"] = nil
			// A cluster left on "organization" visibility with no owner would be visible to nobody,
			// so releasing it always returns it to shared.
			if p.Visibility == nil || *p.Visibility == models.ClusterVisibilityOrganization {
				cols["visibility"] = models.ClusterVisibilityAll
			}
		} else {
			cols["organization_id"] = *p.OrganizationID
			cols["visibility"] = models.ClusterVisibilityOrganization
		}
	}
	if p.Cordoned != nil {
		cols["cordoned"] = *p.Cordoned
	}
	external, err := s.externalColumns(c, p)
	if err != nil {
		return nil, err
	}
	maps.Copy(cols, external)
	modeChanged := false
	if p.ServiceEndpointMode != nil {
		if !models.ValidServiceEndpointMode(*p.ServiceEndpointMode) {
			return nil, ErrInvalidEndpointMode
		}
		if *p.ServiceEndpointMode != c.EndpointMode() {
			cols["service_endpoint_mode"] = *p.ServiceEndpointMode
			modeChanged = true
		}
	}
	ingressChanged := false
	if p.IngressIP != nil || p.IngressHostname != nil {
		ip, host := c.IngressIP, c.IngressHostname
		if p.IngressIP != nil {
			ip = *p.IngressIP
		}
		if p.IngressHostname != nil {
			host = *p.IngressHostname
		}
		if ip, host, err = normalizeIngress(ip, host); err != nil {
			return nil, err
		}
		if ip != c.IngressIP || host != c.IngressHostname {
			cols["ingress_ip"], cols["ingress_hostname"] = ip, host
			ingressChanged = true
		}
	}
	if len(cols) > 0 {
		if err := s.store.UpdateColumns(c.ID, cols); err != nil {
			return nil, err
		}
	}
	if p.OrganizationID != nil {
		// Dedicating a location invalidates the default location of every workspace that may no
		// longer place there — the ones outside the organization, and the organization's own if it
		// was pointing at shared hardware. Left alone they are skipped silently at every deploy.
		s.clearStaleDefaults()
	}
	if len(external) > 0 {
		s.externalChanged(c.ID)
	}
	if modeChanged {
		s.endpointModeChanged(c.ID, *p.ServiceEndpointMode)
	}
	if ingressChanged {
		s.ResyncRoutes(context.Background(), c.ID)
	}
	return s.Cluster(c.ID)
}
