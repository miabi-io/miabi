// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package events

import (
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

func TestNextStoredStatus(t *testing.T) {
	run := models.AppStatusRunning
	cases := []struct {
		name       string
		action     string
		exit       string
		current    models.AppStatus
		want       models.AppStatus
		wantChange bool
	}{
		{"crash flips running to failed", "die", "1", run, models.AppStatusFailed, true},
		{"oom flips running to failed", "oom", "", run, models.AppStatusFailed, true},
		{"graceful exit no change", "die", "0", run, run, false},
		{"sigterm exit no change", "die", "143", run, run, false},
		{"start recovers failed to running", "start", "", models.AppStatusFailed, models.AppStatusRunning, true},
		{"start when already running no change", "start", "", run, run, false},
		{"crash when stopped no change", "die", "1", models.AppStatusStopped, models.AppStatusStopped, false},
		{"health event ignored", "health_status: unhealthy", "", run, run, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, change := nextStoredStatus(c.action, c.exit, c.current)
			if got != c.want || change != c.wantChange {
				t.Errorf("nextStoredStatus(%q,%q,%q) = (%q,%v), want (%q,%v)", c.action, c.exit, c.current, got, change, c.want, c.wantChange)
			}
		})
	}
}

func TestDieIsStop(t *testing.T) {
	cases := []struct {
		name        string
		exit        string
		userStopped bool
		wantStop    bool
	}{
		{"graceful exit is a stop", "0", false, true},
		{"sigterm exit is a stop", "143", false, true},
		{"crash while running is not a stop", "1", false, false},
		{"sigkill while running is not a stop", "137", false, false},
		// A user-initiated stop is a stop regardless of exit code — Git/buildpack
		// images get SIGKILLed (137) because /bin/sh doesn't forward SIGTERM.
		{"sigkill of a stopped resource is a stop", "137", true, true},
		{"any code of a stopped resource is a stop", "1", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dieIsStop(c.exit, c.userStopped); got != c.wantStop {
				t.Errorf("dieIsStop(%q,%v) = %v, want %v", c.exit, c.userStopped, got, c.wantStop)
			}
		})
	}
}

// TestClassify pins the classification shared by the app and database paths: only the
// stop/crash split varies, and it varies with userStopped alone.
func TestClassify(t *testing.T) {
	ev := func(action, exit string) docker.EngineEvent {
		return docker.EngineEvent{Action: action, Attributes: map[string]string{"exitCode": exit}}
	}
	cases := []struct {
		name        string
		ev          docker.EngineEvent
		userStopped bool
		wantType    models.AppEventType
		wantSev     models.AppEventSeverity
		wantOK      bool
	}{
		{"start", ev("start", ""), false, models.EventContainerStarted, models.SeverityInfo, true},
		{"oom", ev("oom", ""), false, models.EventContainerOOM, models.SeverityError, true},
		{"unhealthy", ev("health_status: unhealthy", ""), false, models.EventContainerHealth, models.SeverityWarning, true},
		{"healthy", ev("health_status: healthy", ""), false, models.EventContainerHealth, models.SeverityInfo, true},
		{"crash", ev("die", "137"), false, models.EventContainerDied, models.SeverityError, true},
		{"stop", ev("die", "137"), true, models.EventContainerStopped, models.SeverityInfo, true},
		{"unrelated action", ev("attach", ""), false, "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			typ, sev, _, ok := classify(c.ev, c.userStopped)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if typ != c.wantType || sev != c.wantSev {
				t.Errorf("classify = (%q,%q), want (%q,%q)", typ, sev, c.wantType, c.wantSev)
			}
		})
	}
}
