// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformbackup

import (
	"errors"
	"strings"
	"testing"
)

// RunOneShot returns a NIL error when the helper itself exits non-zero, so the
// original `%w` on that nil produced "exited with code 1: %!w(<nil>)" — a
// message that reported the one thing nobody needed and discarded the tool's
// output, which is the whole diagnosis.
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

// A real Docker failure (the container could not be created at all) must stay
// wrappable, so callers can still match on it.
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

// A loopback database host can never work from inside the helper container: it
// resolves to the container. Saying so beats a connection-refused that reads
// like the database is down.
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
