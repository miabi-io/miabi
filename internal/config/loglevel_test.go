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

package config

import (
	"strings"
	"testing"

	"github.com/jkaninda/logger"
)

// Empty must keep the pre-configurable behaviour, or upgrading silently changes log noise.
func TestLogLevelDefaultsByEnvironment(t *testing.T) {
	for _, c := range []struct {
		dev  bool
		want logger.LogLevel
	}{
		{true, logger.LevelDebug},
		{false, logger.LevelInfo},
	} {
		got, err := (&Config{DevMode: c.dev}).LogLevelFor()
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("DevMode=%v: level = %q, want %q", c.dev, got, c.want)
		}
	}
}

func TestLogLevelParsing(t *testing.T) {
	for in, want := range map[string]logger.LogLevel{
		"debug":   logger.LevelDebug,
		"DEBUG":   logger.LevelDebug, // case-insensitive
		" info ":  logger.LevelInfo,  // stray whitespace from a .env or YAML value
		"warn":    logger.LevelWarning,
		"warning": logger.LevelWarning, // the library spells it long, slog spells it short
		"error":   logger.LevelError,
	} {
		got, err := (&Config{LogLevel: in}).LogLevelFor()
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

// logger.LevelOff returns before assigning the package logger, so bare logger calls would keep printing.
func TestOffIsRejectedBecauseTheLibraryCannotHonourIt(t *testing.T) {
	for _, off := range []string{"off", "none", "silent"} {
		_, err := (&Config{LogLevel: off}).LogLevelFor()
		if err == nil {
			t.Errorf("%q was accepted — it would silence only Okapi's logger, not Miabi's", off)
			continue
		}
		if !strings.Contains(err.Error(), "error") {
			t.Errorf("the error does not point at the workaround (\"error\"): %v", err)
		}
	}
}

// A typo must fail loudly rather than silently fall back to info.
func TestUnknownLogLevelIsRejected(t *testing.T) {
	for _, bad := range []string{"verbose", "trace", "warnings", "1"} {
		_, err := (&Config{LogLevel: bad}).LogLevelFor()
		if err == nil {
			t.Errorf("%q was accepted as a log level", bad)
			continue
		}
		if !strings.Contains(err.Error(), bad) || !strings.Contains(err.Error(), "debug") {
			t.Errorf("the error neither names the bad value nor lists the valid ones: %v", err)
		}
	}
}

// The level must reach the package-level logger, which almost every Miabi log line uses.
func TestInitLoggerInstallsThePackageDefault(t *testing.T) {
	c := &Config{LogLevel: "error"}
	l, err := c.initLogger()
	if err != nil {
		t.Fatal(err)
	}
	if got := l.GetLevel(); got != logger.LevelError {
		t.Fatalf("logger level = %q, want error", got)
	}
	if logger.Default().Logger != l.Logger {
		t.Error("logger.New did not install itself as the package default — every bare " +
			"logger.Info() call in Miabi would ignore MIABI_LOG_LEVEL")
	}
}
