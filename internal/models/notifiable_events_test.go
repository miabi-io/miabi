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

package models

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The console's event picker list must match this one, or events silently become unsubscribable.
const consoleNotifiableEvents = "../../web/src/constants/notifiableEvents.ts"

var notifiableEventValue = regexp.MustCompile(`\bvalue:\s*'([a-z_.]+)'`)

func consoleEvents(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.FromSlash(consoleNotifiableEvents))
	if err != nil {
		t.Fatalf("read %s: %v", consoleNotifiableEvents, err)
	}
	found := notifiableEventValue.FindAllSubmatch(src, -1)
	if len(found) == 0 {
		t.Fatalf("no event values found in %s — has the list been reshaped?", consoleNotifiableEvents)
	}
	out := make([]string, 0, len(found))
	for _, m := range found {
		out = append(out, string(m[1]))
	}
	return out
}

// Same order too: the console renders them in it, and the grouped picker relies on the backend's sequence.
func TestConsoleNotifiableEventsMatchTheBackend(t *testing.T) {
	console := consoleEvents(t)

	want := make([]string, 0, len(NotifiableEvents))
	for _, e := range NotifiableEvents {
		want = append(want, string(e))
	}

	if len(console) != len(want) {
		t.Fatalf("the console lists %d notifiable events and the backend %d\nconsole: %v\nbackend: %v",
			len(console), len(want), console, want)
	}
	for i := range want {
		if console[i] != want[i] {
			t.Errorf("event %d: console has %q, backend has %q", i, console[i], want[i])
		}
	}
}

// A console value the backend never delivers saves a subscription that can never fire.
func TestConsoleOffersNoUnnotifiableEvent(t *testing.T) {
	for _, v := range consoleEvents(t) {
		if !IsNotifiable(AppEventType(v)) {
			t.Errorf("the console offers %q, which IsNotifiable rejects — subscribing to it does nothing", v)
		}
	}
}
