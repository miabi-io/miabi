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

package models

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The console's language list must match this one, or saves are silently rejected and the choice lost at next sign-in.
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
		// A list reshape that breaks the regex must fail loudly rather than pass with nothing to compare.
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
