// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cron

import "testing"

type fakeLeader struct{ leading bool }

func (l *fakeLeader) Leading() bool { return l.leading }

func TestTasksRunOnlyWhileLeadingUnlessLocal(t *testing.T) {
	m := NewManager(nil, nil, nil, nil, nil)
	l := &fakeLeader{}
	m.SetLeader(l)

	var shared, local int
	if err := m.RegisterTask("shared", 0, "Shared", "@every 1m", func() error { shared++; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterLocalTask("local", 0, "Local", "@every 1m", func() error { local++; return nil }); err != nil {
		t.Fatal(err)
	}
	tick := func() {
		for _, key := range []string{taskKey("shared", 0), taskKey("local", 0)} {
			m.c.Entry(m.entries[key]).WrappedJob.Run()
		}
	}

	tick()
	if shared != 0 || local != 1 {
		t.Fatalf("standby tick ran shared=%d local=%d; want 0 and 1", shared, local)
	}
	for _, j := range m.Snapshot() {
		if j.Kind == "shared" && j.LastRunAt != nil {
			t.Fatal("a skipped task was recorded as run")
		}
	}

	l.leading = true
	tick()
	if shared != 1 || local != 2 {
		t.Fatalf("leader tick ran shared=%d local=%d; want 1 and 2", shared, local)
	}
}
