// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package placement

import (
	"errors"
	"strings"
	"testing"
)

func TestReach(t *testing.T) {
	swarm := func(id uint) bool { return id == 2 }
	label := func(id uint) string { return map[uint]string{1: "eu-central", 2: "eu-east"}[id] }
	app := func(cluster, server uint) Site {
		return Site{Kind: "application", Name: "api", ClusterID: cluster, ServerID: server}
	}
	db := func(cluster, server uint) Site {
		return Site{Kind: "database", Name: "pg", ClusterID: cluster, ServerID: server}
	}

	if err := Reach(app(1, 5), db(1, 5), swarm, label); err != nil {
		t.Errorf("same node: %v", err)
	}
	if err := Reach(app(2, 5), db(2, 6), swarm, label); err != nil {
		t.Errorf("two nodes of a swarm: %v", err)
	}
	if err := Reach(app(1, 5), db(1, 6), swarm, label); !errors.Is(err, ErrCrossNode) {
		t.Errorf("two nodes without a swarm = %v", err)
	}
	err := Reach(app(2, 5), db(1, 5), swarm, label)
	if !errors.Is(err, ErrCrossLocation) {
		t.Fatalf("two locations = %v", err)
	}
	if want := "application api is in eu-east, database pg is in eu-central"; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("message = %q", err)
	}
}
