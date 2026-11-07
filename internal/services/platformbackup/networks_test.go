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

package platformbackup

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

// netDocker records which networks the helper container was told to join.
type netDocker struct {
	docker.Client
	ensured []string
	fail    string // a network name whose creation blows up
}

func (n *netDocker) EnsureNetwork(_ context.Context, name string) (string, error) {
	if name == n.fail {
		return "", errors.New("no such network")
	}
	n.ensured = append(n.ensured, name)
	return name, nil
}

// The DB is only on the private network and a self-hosted MinIO only on the proxy one, so both are needed.
func TestBackupHelperJoinsBothNetworks(t *testing.T) {
	s := &Service{network: "miabi", internalNetwork: "miabi-internal"}
	dc := &netDocker{}

	nets, err := s.backupNetworks(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"miabi", "miabi-internal"} {
		if !slices.Contains(nets, want) {
			t.Errorf("helper networks = %v, missing %s", nets, want)
		}
	}
	// Docker picks the default route from attachment order, and S3 egress is only on the proxy network.
	if len(nets) > 0 && nets[0] != "miabi" {
		t.Errorf("helper networks = %v, want the egress-carrying network first", nets)
	}
}

// Compose has no private network; unset must behave exactly as before the split.
func TestBackupHelperOnComposeIsUnchanged(t *testing.T) {
	s := &Service{network: "miabi"}
	nets, err := s.backupNetworks(context.Background(), &netDocker{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 1 || nets[0] != "miabi" {
		t.Errorf("helper networks = %v, want only the proxy network", nets)
	}
}

// Docker refuses duplicate attachments, so the helper would not start.
func TestBackupHelperDoesNotAttachTheSameNetworkTwice(t *testing.T) {
	s := &Service{network: "miabi", internalNetwork: "miabi"}
	nets, err := s.backupNetworks(context.Background(), &netDocker{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 1 {
		t.Errorf("helper networks = %v, want one", nets)
	}
}

// With two networks, the error must say which one failed.
func TestBackupHelperNetworkFailureNamesTheNetwork(t *testing.T) {
	s := &Service{network: "miabi", internalNetwork: "miabi-internal"}
	_, err := s.backupNetworks(context.Background(), &netDocker{fail: "miabi-internal"})
	if err == nil {
		t.Fatal("a network that could not be created was accepted")
	}
	if !strings.Contains(err.Error(), "miabi-internal") {
		t.Errorf("error does not name the failing network: %v", err)
	}
}

// Naming the proxy network Postgres deliberately left would send the operator down the wrong path.
func TestUnreachableDBAdviceNamesThePrivateNetwork(t *testing.T) {
	s := &Service{db: DBConn{Host: "localhost"}, network: "miabi", internalNetwork: "miabi-internal"}
	err := s.assertDBReachable()
	if err == nil {
		t.Fatal("loopback was accepted")
	}
	if !strings.Contains(err.Error(), "miabi-internal") {
		t.Errorf("advice names the wrong network: %v", err)
	}

	// ...and on Compose, where there is no private network, it still names the proxy one.
	compose := &Service{db: DBConn{Host: "localhost"}, network: "miabi"}
	if err := compose.assertDBReachable(); err == nil || !strings.Contains(err.Error(), "miabi") {
		t.Errorf("compose advice = %v, want the proxy network named", err)
	}
}
