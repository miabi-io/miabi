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

package platformbackup

import (
	"errors"
	"strings"
	"testing"
)

// RunOneShot returns nil on non-zero exit, so %w produced "%!w(<nil>)" and dropped the tool's output.
func TestOneShotErrorCarriesTheOutput(t *testing.T) {
	err := oneShotError("control-plane database backup", 1,
		"pg_dump: error: connection to server at \"miabi-postgres\" failed", nil)

	msg := err.Error()
	if strings.Contains(msg, "%!w") || strings.Contains(msg, "<nil>") {
		t.Fatalf("the formatting bug is back: %s", msg)
	}
	if !strings.Contains(msg, "connection to server") {
		t.Fatalf("the tool's output was dropped: %s", msg)
	}
	if !strings.Contains(msg, "code 1") {
		t.Fatalf("the exit code was dropped: %s", msg)
	}
}

// Real Docker failures must stay wrappable so callers can match on them.
func TestOneShotErrorWrapsARealError(t *testing.T) {
	sentinel := errors.New("no such image")
	err := oneShotError("volume backup", 0, "", sentinel)
	if !errors.Is(err, sentinel) {
		t.Fatalf("wrapped error was lost: %v", err)
	}
	if !strings.Contains(err.Error(), "(no output)") {
		t.Fatalf("empty output should be stated, not blank: %s", err.Error())
	}
}

// The message lands in a database column, not a log file.
func TestOneShotErrorTruncatesLongOutput(t *testing.T) {
	err := oneShotError("backup", 1, strings.Repeat("x", 10_000), nil)
	if len(err.Error()) > 2200 {
		t.Fatalf("message is %d bytes; it should keep only the tail", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "…") {
		t.Error("truncation should be visible, so nobody reads a tail as the whole story")
	}
}

// Loopback resolves to the helper container itself; say so instead of a misleading connection-refused.
func TestAssertDBReachableRejectsLoopback(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "", "LOCALHOST"} {
		s := &Service{db: DBConn{Host: host}}
		err := s.assertDBReachable()
		if err == nil {
			t.Errorf("host %q was accepted; it points at the backup container itself", host)
			continue
		}
		if !strings.Contains(err.Error(), "MIABI_DB_HOST") {
			t.Errorf("host %q: the error should name the setting to change: %v", host, err)
		}
	}

	for _, host := range []string{"miabi-postgres", "db.internal", "10.0.0.5"} {
		s := &Service{db: DBConn{Host: host}}
		if err := s.assertDBReachable(); err != nil {
			t.Errorf("host %q was rejected: %v", host, err)
		}
	}
}
