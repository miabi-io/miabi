// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// The helper needs BOTH: the control-plane database is only on the private network once the stack is
// split, and an S3 endpoint that is really a self-hosted MinIO app is only on the proxy one. Attaching
// to either alone fails a real backup — on connect, or on upload after the archive is already made.
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
	// The proxy network first: Docker picks the helper's default route from its attachments, and the
	// archive still has to reach S3 on a stack whose private network has no egress.
	if len(nets) > 0 && nets[0] != "miabi" {
		t.Errorf("helper networks = %v, want the egress-carrying network first", nets)
	}
}

// A Compose install has no private network. Unset must mean "exactly what it did before the split",
// or every Compose stack's platform backup changes behaviour for a feature it does not have.
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

// A hand-edited manifest can point both settings at one network. Docker refuses a duplicate
// attachment, so the helper would fail to start at all.
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

// The error has to name the network that could not be created — with two of them, "attach the backup
// container to network" and nothing else sends the operator looking at the wrong one.
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

// The advice for an unreachable database must point at the network Postgres is actually on. Naming the
// proxy network it deliberately left would send an operator to verify a connection that is supposed to
// be impossible.
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
