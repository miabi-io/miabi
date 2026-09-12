// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"errors"
	"strings"
	"testing"
)

// A refusal has to say what is in the way and what to do instead, not just that the node is busy.
func TestNodeWorkloadsErrorNamesTheWayForward(t *testing.T) {
	for _, tt := range []struct {
		err  *NodeWorkloadsError
		want []string
	}{
		{&NodeWorkloadsError{Node: "lyon", Action: EnableSwarmAction, Apps: 2, Databases: 1, Volumes: 3},
			[]string{`"lyon"`, "2 apps, 1 database and 3 volumes", "Swarm cannot be enabled", "default cluster", "empty node"}},
		{&NodeWorkloadsError{Node: "lyon", Action: JoinAction, Databases: 1},
			[]string{"runs 1 database, so it cannot join", "default cluster"}},
		{&NodeWorkloadsError{Node: "lyon", Action: LeaveAction, Apps: 1, Volumes: 1},
			[]string{"1 app and 1 volume", "cannot leave"}},
	} {
		if !errors.Is(tt.err, ErrNodeHasWorkloads) {
			t.Errorf("%v does not match ErrNodeHasWorkloads", tt.err)
		}
		for _, want := range tt.want {
			if !strings.Contains(tt.err.Error(), want) {
				t.Errorf("message is missing %q: %s", want, tt.err)
			}
		}
	}
}
