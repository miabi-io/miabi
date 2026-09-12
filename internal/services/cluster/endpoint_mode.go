// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"
	"errors"

	"github.com/miabi-io/miabi/internal/models"
)

// ErrInvalidEndpointMode is returned for a service endpoint mode other than "vip" or "dnsrr".
var ErrInvalidEndpointMode = errors.New("service load balancing must be vip or dnsrr")

// ServiceEndpointMode is how a cluster's service apps are reached by name; id 0 is the default cluster.
func (s *Service) ServiceEndpointMode(clusterID uint) models.ServiceEndpointMode {
	if s.store == nil {
		return models.ServiceEndpointVIP
	}
	c, err := s.store.FindByID(clusterID)
	if err != nil {
		return models.ServiceEndpointVIP
	}
	return c.EndpointMode()
}

// SetEndpointModeListener is told when a cluster's service endpoint mode changed, so its running services follow.
func (s *Service) SetEndpointModeListener(fn func(ctx context.Context, clusterID uint, mode models.ServiceEndpointMode)) {
	s.endpointModeListener = fn
}

func (s *Service) endpointModeChanged(clusterID uint, mode models.ServiceEndpointMode) {
	if s.endpointModeListener != nil {
		go s.endpointModeListener(context.Background(), clusterID, mode)
	}
}
