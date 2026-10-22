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
package route

import (
	"testing"

	"github.com/miabi-io/miabi/internal/proxy"
)

func TestApplySuspension(t *testing.T) {
	s := &Service{}
	rr := proxy.RenderedRoute{WorkspaceID: 7, Maintenance: &proxy.RouteMaintenance{Enabled: true, StatusCode: 502, Message: "own"}}
	s.applySuspension(&rr)
	if rr.Maintenance.StatusCode != 502 {
		t.Fatal("without the check wired, a route keeps its own maintenance setting")
	}
	s.SetSuspended(func(ws uint) bool { return ws == 7 })
	s.applySuspension(&rr)
	if rr.Maintenance == nil || !rr.Maintenance.Enabled || rr.Maintenance.StatusCode != 503 || rr.Maintenance.Message != suspendedNotice {
		t.Fatalf("suspended workspace route: %+v", rr.Maintenance)
	}
	other := proxy.RenderedRoute{WorkspaceID: 8}
	s.applySuspension(&other)
	if other.Maintenance != nil {
		t.Fatal("another workspace's route must be untouched")
	}
}
