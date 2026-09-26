// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// Pausing bypasses the licence gate, so it must mean exactly "switch off": any other edit riding
// along would let a lapsed licence reconfigure a paid feature.
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
