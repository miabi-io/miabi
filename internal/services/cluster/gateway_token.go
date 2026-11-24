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

package cluster

import (
	"crypto/subtle"
	"errors"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

const gatewayTokenPrefix = "mbgw_"

var (
	// The central gateway reads Miabi's file provider, so the default cluster has no provider endpoint.
	ErrDefaultClusterGateway = errors.New("the default cluster is served by the central gateway and has no gateway token")
	ErrBadGatewayToken       = errors.New("invalid cluster gateway token")
)

// GatewayToken mints on first use; it only reads the config of the cluster whose name is returned with it.
func (s *Service) GatewayToken(clusterID uint) (token, name string, err error) {
	c, err := s.gatewayCluster(clusterID)
	if err != nil {
		return "", "", err
	}
	if c.GatewayTokenEnc != "" {
		if tok, derr := crypto.Decrypt(c.GatewayTokenEnc); derr == nil && tok != "" {
			return tok, c.Name, nil
		}
	}
	token, err = s.storeGatewayToken(c.ID)
	return token, c.Name, err
}

// RotateGatewayToken invalidates the previous token at once: gateways still holding it fail until redeployed.
func (s *Service) RotateGatewayToken(clusterID uint) (token, name string, err error) {
	c, err := s.gatewayCluster(clusterID)
	if err != nil {
		return "", "", err
	}
	token, err = s.storeGatewayToken(c.ID)
	return token, c.Name, err
}

// AuthenticateGateway resolves the cluster by its stable name; every failure looks the same to the caller.
func (s *Service) AuthenticateGateway(name, token string) (*models.Cluster, error) {
	if s.store == nil || name == "" || token == "" {
		return nil, ErrBadGatewayToken
	}
	c, err := s.store.FindByName(name)
	if err != nil || c.IsDefault || c.GatewayTokenEnc == "" {
		return nil, ErrBadGatewayToken
	}
	want, err := crypto.Decrypt(c.GatewayTokenEnc)
	if err != nil || want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(token)) != 1 {
		return nil, ErrBadGatewayToken
	}
	return c, nil
}

func (s *Service) gatewayCluster(clusterID uint) (*models.Cluster, error) {
	if s.store == nil {
		return nil, ErrClusterNotFound
	}
	c, err := s.store.FindByID(clusterID)
	if err != nil {
		return nil, ErrClusterNotFound
	}
	if c.IsDefault {
		return nil, ErrDefaultClusterGateway
	}
	return c, nil
}

func (s *Service) storeGatewayToken(clusterID uint) (string, error) {
	tok := gatewayTokenPrefix + randHex(32)
	enc, err := crypto.Encrypt(tok)
	if err != nil {
		return "", err
	}
	if err := s.store.UpdateColumns(clusterID, map[string]any{"gateway_token_enc": enc}); err != nil {
		return "", err
	}
	return tok, nil
}
