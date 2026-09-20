// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The console ships its own language list and renders the sign-in picker straight
// from it. Nothing at build time ties that list to this one, and the failure is
// quiet in the worst way: the picker offers a language, the browser switches to it,
// and the save is rejected with "locale must be one of" — so the choice is lost at
// the next sign-in and the user is never told why.
//
// Adding a language means editing both files. This test is what says so.
const consoleLanguages = "../../web/src/i18n/languages.ts"

var languageCode = regexp.MustCompile(`\bcode:\s*'([a-z-]+)'`)

func consoleLocales(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.FromSlash(consoleLanguages))
	if err != nil {
		t.Fatalf("read %s: %v", consoleLanguages, err)
	}
	found := languageCode.FindAllSubmatch(src, -1)
	if len(found) == 0 {
		// The regex depends on how the list is written. A rewrite that breaks it must
		// fail loudly rather than silently pass with nothing to compare.
		t.Fatalf("no language codes found in %s — has the list been reshaped?", consoleLanguages)
	}
	out := make([]string, 0, len(found))
	for _, m := range found {
		out = append(out, string(m[1]))
	}
	return out
}

func TestLocalesMatchConsole(t *testing.T) {
	console := consoleLocales(t)
	server := Locales()

	// Order matters as little here as it does in the picker, but a mismatch in either
	// direction is a bug, so compare as sets and report which side is missing what.
	inServer := make(map[string]bool, len(server))
	for _, c := range server {
		inServer[c] = true
	}
	inConsole := make(map[string]bool, len(console))
	for _, c := range console {
		inConsole[c] = true
	}

	for _, c := range console {
		if !inServer[c] {
			t.Errorf("%q is offered by the console but rejected by ValidLocale — add it to models.locales", c)
		}
	}
	for _, c := range server {
		if !inConsole[c] {
			t.Errorf("%q is accepted here but the console never offers it — add it to %s", c, consoleLanguages)
		}
	}
}

func TestEveryLocaleHasACatalogue(t *testing.T) {
	for _, c := range Locales() {
		path := filepath.Join("..", "..", "web", "src", "i18n", c+".json")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("locale %q has no catalogue at %s: a user who picks it sees English", c, path)
		}
	}
}

func TestResolveLocale(t *testing.T) {
	cases := map[string]string{
		"en":       LocaleEnglish,
		"fr":       LocaleFrench,
		"FR":       LocaleFrench,
		" fr ":     LocaleFrench,
		"fr-CA":    LocaleFrench,
		"fr_BE":    LocaleFrench,
		"de":       LocaleEnglish,
		"":         LocaleEnglish,
		"-":        LocaleEnglish,
		"nonsense": LocaleEnglish,
	}
	for in, want := range cases {
		if got := ResolveLocale(in); got != want {
			t.Errorf("ResolveLocale(%q) = %q, want %q", in, got, want)
		}
	}
}
