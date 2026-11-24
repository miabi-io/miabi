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
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

func newGatewayTokenService(t *testing.T) *Service {
	t.Helper()
	crypto.Init("test-master-key-for-cluster-gateway")
	t.Cleanup(func() { crypto.Init("") })
	s := NewService(fakeClients{local: &fakeDocker{}}, &fakeNodes{})
	s.SetStore(&memStore{
		def:    models.Cluster{ID: 1, IsDefault: true, Name: "default"},
		others: map[uint]models.Cluster{4: {ID: 4, Name: "paris", Mode: models.ClusterModeSwarm}},
	})
	return s
}

func TestGatewayTokenIsMintedOnceAndAuthenticatesItsClusterOnly(t *testing.T) {
	s := newGatewayTokenService(t)

	tok, name, err := s.GatewayToken(4)
	if err != nil || name != "paris" || !strings.HasPrefix(tok, gatewayTokenPrefix) {
		t.Fatalf("GatewayToken = %q, %q, %v; want an mbgw_ token for paris", tok, name, err)
	}
	if again, _, _ := s.GatewayToken(4); again != tok {
		t.Fatal("a second reveal minted a new token")
	}
	if c, err := s.AuthenticateGateway("paris", tok); err != nil || c.ID != 4 {
		t.Fatalf("AuthenticateGateway = %v, %v; want cluster 4", c, err)
	}
	for _, tc := range []struct{ name, token string }{
		{"paris", tok + "x"},
		{"paris", ""},
		{"default", tok},
		{"unknown", tok},
	} {
		if _, err := s.AuthenticateGateway(tc.name, tc.token); !errors.Is(err, ErrBadGatewayToken) {
			t.Errorf("AuthenticateGateway(%q, …) = %v, want ErrBadGatewayToken", tc.name, err)
		}
	}
}

func TestRotatedGatewayTokenReplacesTheOldOne(t *testing.T) {
	s := newGatewayTokenService(t)
	old, _, _ := s.GatewayToken(4)
	fresh, _, err := s.RotateGatewayToken(4)
	if err != nil || fresh == old {
		t.Fatalf("rotate = %q, %v; want a different token", fresh, err)
	}
	if _, err := s.AuthenticateGateway("paris", old); !errors.Is(err, ErrBadGatewayToken) {
		t.Error("the rotated-out token still authenticates")
	}
	if _, err := s.AuthenticateGateway("paris", fresh); err != nil {
		t.Errorf("the rotated token does not authenticate: %v", err)
	}
}

func TestDefaultClusterHasNoGatewayToken(t *testing.T) {
	s := newGatewayTokenService(t)
	if _, _, err := s.GatewayToken(1); !errors.Is(err, ErrDefaultClusterGateway) {
		t.Errorf("GatewayToken(default) = %v, want ErrDefaultClusterGateway", err)
	}
	if _, _, err := s.RotateGatewayToken(99); !errors.Is(err, ErrClusterNotFound) {
		t.Errorf("RotateGatewayToken(unknown) = %v, want ErrClusterNotFound", err)
	}
}
