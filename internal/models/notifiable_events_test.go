// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The console renders the webhook and notification-channel event pickers straight from its own list,
// and nothing at build time ties that list to this one.
//
// The failure is quiet in the direction that matters. An event missing from the console is an event a
// workspace cannot subscribe to — the checkbox is simply not on the page — while the backend would
// have delivered it happily. That is how every database event, backups included, went unsubscribable:
// the backend accepted all eight and the picker offered none.
//
// Adding a notifiable event means editing both files. This test is what says so.
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

// The two lists must hold the same events in the same order. Order matters because the console
// renders them in it, and the grouped picker is only coherent if it reproduces the backend's
// application-then-database sequence.
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

// Every value the console offers has to be one the backend will actually deliver, or the checkbox
// saves a subscription that can never fire.
func TestConsoleOffersNoUnnotifiableEvent(t *testing.T) {
	for _, v := range consoleEvents(t) {
		if !IsNotifiable(AppEventType(v)) {
			t.Errorf("the console offers %q, which IsNotifiable rejects — subscribing to it does nothing", v)
		}
	}
}
