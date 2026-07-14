// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"strings"
	"testing"

	"github.com/jkaninda/logger"
)

// Empty must keep exactly the behaviour that existed before the level was
// configurable, or upgrading silently changes how noisy every install is.
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

// "off" is rejected on purpose. logger.LevelOff makes logger.New() install a discard
// handler on its own instance and return BEFORE assigning the package-level logger —
// so it silences the logger handed to Okapi and nothing else, while every bare
// logger.Info() in Miabi keeps printing. A switch that looks like it turns logging off
// while the logs keep coming is worse than no switch at all.
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

// A typo must fail loudly. Falling back to info would leave an operator watching
// logs for something they believe they enabled, with nothing to tell them otherwise.
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

// The level must reach the PACKAGE-LEVEL logger, not only the one handed to Okapi:
// almost every log line in Miabi is a bare logger.Info(...) call, which resolves
// through logger.Default(). A level that applied only to Okapi's logger would look
// wired up and do nothing.
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
