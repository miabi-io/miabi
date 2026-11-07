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

//go:build integration

package docker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestEventsLifecycle(t *testing.T) {
	expectNoGoroutineLeak(t)
	cli := newClient(t)
	ensureImage(t, cli, imgAlpine)

	ctx, cancel := context.WithCancel(context.Background())

	type ev struct {
		action string
		id     string
		app    string
	}
	var mu sync.Mutex
	var events []ev
	streamErr := make(chan error, 1)
	started := make(chan struct{})
	var once sync.Once

	go func() {
		streamErr <- cli.StreamEvents(ctx, func(e EngineEvent) error {
			once.Do(func() { close(started) }) // stream is live once the first event lands
			mu.Lock()
			events = append(events, ev{action: e.Action, id: e.ContainerID, app: e.Attributes[LabelApp]})
			mu.Unlock()
			return nil
		})
	}()

	id := run(t, cli, RunSpec{
		Image:         imgAlpine,
		Cmd:           []string{"sleep", "3600"},
		RestartPolicy: "no",
		Labels:        map[string]string{LabelApp: "events-it"},
	})
	if err := cli.StopContainer(bg(t, 20*time.Second), id, 2); err != nil {
		t.Fatalf("StopContainer: %v", err)
	}

	// Wait until both start and die for our container are observed.
	deadline := time.Now().Add(30 * time.Second)
	var sawStart, sawDie, sawAppLabel bool
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, e := range events {
			if e.id != id {
				continue
			}
			switch e.action {
			case "start":
				sawStart = true
				if e.app == "events-it" {
					sawAppLabel = true
				}
			case "die":
				sawDie = true
			}
		}
		mu.Unlock()
		if sawStart && sawDie {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !sawStart {
		t.Errorf("did not observe a 'start' event for container %s", id[:12])
	}
	if !sawDie {
		t.Errorf("did not observe a 'die' event for container %s", id[:12])
	}
	if !sawAppLabel {
		t.Errorf("event attributes did not carry the io.miabi.app label")
	}

	// Cancelling must end the stream promptly with a context error.
	cancel()
	select {
	case err := <-streamErr:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("StreamEvents returned %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StreamEvents did not return after context cancel")
	}
	time.Sleep(200 * time.Millisecond) // let the SDK reader unwind before goleak
}
