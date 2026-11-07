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

package handlers

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// Pausing bypasses the gate, so any other edit riding along would let a lapsed licence reconfigure it.
func TestPausesOnly(t *testing.T) {
	sched := &models.VolumeBackupSchedule{Cron: "0 3 * * *", Enabled: true, MaxPoints: 7, RetentionDays: 30}
	req := func(cron string, enabled bool, max, days int) *VolumeBackupScheduleRequest {
		r := &VolumeBackupScheduleRequest{}
		r.Body.Cron, r.Body.Enabled, r.Body.MaxPoints, r.Body.RetentionDays = cron, enabled, max, days
		return r
	}
	if !pausesOnly(sched, req(" 0 3 * * * ", false, 7, 30)) {
		t.Error("switching off with nothing else changed must count as a pause")
	}
	for name, r := range map[string]*VolumeBackupScheduleRequest{
		"resume":         req("0 3 * * *", true, 7, 30),
		"new cron":       req("0 4 * * *", false, 7, 30),
		"new max points": req("0 3 * * *", false, 1, 30),
		"new retention":  req("0 3 * * *", false, 7, 1),
	} {
		if pausesOnly(sched, r) {
			t.Errorf("%s: treated as a pause", name)
		}
	}
}
