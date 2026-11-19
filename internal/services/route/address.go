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

package route

import (
	"github.com/miabi-io/miabi/internal/appref"
	"github.com/miabi-io/miabi/internal/models"
)

// SetAddressListener is told when an app's public address changes, so apps that reference it can be marked.
func (s *Service) SetAddressListener(fn func(app *models.Application)) { s.onAddress = fn }

// PublicAddress is the address ${{ applications.<name>.public_url }} resolves to at the app's next deploy.
func (s *Service) PublicAddress(workspaceID, appID uint) (appref.Address, error) {
	app, err := s.apps.FindInWorkspace(workspaceID, appID)
	if err != nil {
		return appref.Address{}, ErrAppRequired
	}
	routes, err := s.routes.ListByApp(app.ID)
	if err != nil {
		return appref.Address{}, err
	}
	return appref.AddressOf(app, routes), nil
}

// trackAddress is deferred around a change; an unchanged address notifies nobody, so edits like middlewares stay quiet.
func (s *Service) trackAddress(appID uint) func() {
	if s.onAddress == nil || appID == 0 {
		return func() {}
	}
	before, ok := s.currentAddress(appID)
	if !ok {
		return func() {}
	}
	return func() {
		app, err := s.apps.FindByID(appID)
		if err != nil {
			return
		}
		if after, ok := s.currentAddress(appID); ok && after != before {
			s.onAddress(app)
		}
	}
}

func (s *Service) currentAddress(appID uint) (appref.Address, bool) {
	app, err := s.apps.FindByID(appID)
	if err != nil {
		return appref.Address{}, false
	}
	routes, err := s.routes.ListByApp(appID)
	if err != nil {
		return appref.Address{}, false
	}
	return appref.AddressOf(app, routes), true
}
