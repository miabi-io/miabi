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

package analytics

import (
	"context"
	"testing"
	"time"
)

func TestLiveScopeKey(t *testing.T) {
	// The workspace scope and an app scope inside it must never collide.
	ws := scopeKey(9, 0)
	app := scopeKey(9, 3)
	if ws == app {
		t.Fatalf("workspace and app scope share a key: %q", ws)
	}
	if ws != "miabi:live:ws9" || app != "miabi:live:ws9:app3" {
		t.Fatalf("unexpected keys: %q %q", ws, app)
	}
	// Distinct workspaces never share a set (ws1+app11 vs ws11+app1).
	if scopeKey(1, 11) == scopeKey(11, 1) {
		t.Fatal("workspace/app ids ambiguous in the key")
	}
}

func TestLiveWindowDefaults(t *testing.T) {
	if got := NewLiveTracker(nil, 0).Window(); got != DefaultLiveWindow {
		t.Fatalf("zero window = %v, want %v", got, DefaultLiveWindow)
	}
	if got := NewLiveTracker(nil, 90*time.Second).Window(); got != 90*time.Second {
		t.Fatalf("explicit window = %v, want 90s", got)
	}
}

// Redis is optional: live visitors is an accent on a dashboard otherwise read from Postgres.
func TestLiveTrackerNoRedisIsInert(t *testing.T) {
	ctx := context.Background()
	tr := NewLiveTracker(nil, time.Minute)
	tr.Touch(ctx, []LiveVisit{{Workspace: 9, App: 3, VID: "vid"}}) // must not panic
	tr.Sweep(ctx)
	n, err := tr.Count(ctx, 9, 0)
	if err != nil || n != 0 {
		t.Fatalf("Count with no redis = %d, %v; want 0, nil", n, err)
	}

	var nilTracker *LiveTracker
	nilTracker.Touch(ctx, []LiveVisit{{Workspace: 9, VID: "vid"}})
	nilTracker.Sweep(ctx)
	if n, err := nilTracker.Count(ctx, 9, 0); err != nil || n != 0 {
		t.Fatalf("nil tracker Count = %d, %v; want 0, nil", n, err)
	}
}

// The nil client would panic if a command were issued.
func TestLiveTrackerSkipsUnusableVisits(t *testing.T) {
	tr := &LiveTracker{rdb: nil, window: time.Minute, scopes: map[string]struct{}{}}
	tr.Touch(context.Background(), nil)
	tr.Touch(context.Background(), []LiveVisit{
		{Workspace: 0, App: 3, VID: "no-workspace"},
		{Workspace: 9, App: 3, VID: ""},
	})
	if len(tr.scopes) != 0 {
		t.Fatalf("remembered scopes for unusable visits: %v", tr.scopes)
	}
}

// Zero, not an error, so the pill can render before the first visitor arrives.
func TestLiveCountUnknownScope(t *testing.T) {
	tr := NewLiveTracker(nil, time.Minute)
	if n, err := tr.Count(context.Background(), 4242, 0); err != nil || n != 0 {
		t.Fatalf("unknown scope = %d, %v; want 0, nil", n, err)
	}
}

func TestIsBotUA(t *testing.T) {
	if !IsBotUA("Googlebot/2.1 (+http://www.google.com/bot.html)") {
		t.Error("googlebot not classified as a bot")
	}
	if IsBotUA("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/120 Safari/537.36") {
		t.Error("chrome classified as a bot")
	}
}
