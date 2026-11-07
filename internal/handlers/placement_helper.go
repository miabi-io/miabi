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

package handlers

import (
	"errors"
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/services/placement"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// A nil Placer keeps the request's server_id, as before locations existed.
type Placer struct {
	svc   *placement.Service
	users *repositories.UserRepository
}

func NewPlacer(svc *placement.Service, users *repositories.UserRepository) *Placer {
	return &Placer{svc: svc, users: users}
}

func (p *Placer) admin(c *okapi.Context) bool {
	if p == nil || p.users == nil {
		return false
	}
	u, err := p.users.FindByID(middlewares.UserID(c))
	return err == nil && u.IsAdmin()
}

// place returns the node a create lands on, from its location or an admin's node pin.
func (p *Placer) place(c *okapi.Context, req placement.Request) (placement.Result, error) {
	if p == nil || p.svc == nil {
		return placement.Result{ServerID: req.ServerID}, nil
	}
	req.WorkspaceID = middlewares.WorkspaceID(c)
	req.Admin = p.admin(c)
	return p.svc.Place(req)
}

// reach refuses a private link that cannot resolve; without a Placer, locations go unnamed.
func (p *Placer) reach(a, b placement.Site, swarm func(clusterID uint) bool) error {
	if p == nil || p.svc == nil {
		return placement.Reach(a, b, swarm, nil)
	}
	return p.svc.Reach(a, b, swarm)
}

// placementAbort answers a placement refusal, or returns nil for any other error.
func placementAbort(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, placement.ErrNodePinAdminOnly), errors.Is(err, placement.ErrLocationNotAllowed):
		return c.AbortForbidden(err.Error())
	case errors.Is(err, placement.ErrLocationNotFound):
		return c.AbortNotFound(err.Error())
	case errors.Is(err, node.ErrNodeNotFound):
		return c.AbortNotFound("node not found")
	case errors.Is(err, placement.ErrLocationMismatch):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, placement.ErrLocationCordoned), errors.Is(err, placement.ErrNoSchedulableNode),
		errors.Is(err, placement.ErrNoLocation):
		return c.AbortWithError(http.StatusConflict, err)
	}
	return nil
}
