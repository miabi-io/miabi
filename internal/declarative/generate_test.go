// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func countIn(s, set string) int {
	n := 0
	for _, c := range s {
		if strings.ContainsRune(set, c) {
			n++
		}
	}
	return n
}

func TestGenerateSecretLength(t *testing.T) {
	for _, n := range []int{1, 8, 32, 128} {
		if got := GenerateSecret(SecretSpec{Length: n}); len(got) != n {
			t.Errorf("length %d produced %d characters", n, len(got))
		}
	}
	if got := GenerateSecret(SecretSpec{}); len(got) != DefaultSecretLength {
		t.Errorf("unset length = %d, want the %d default", len(got), DefaultSecretLength)
	}
}

func TestGenerateSecretDefaultsToAlphanumeric(t *testing.T) {
	alnum := regexp.MustCompile(`^[A-Za-z0-9]+$`)
	for i := 0; i < 200; i++ {
		if got := GenerateSecret(SecretSpec{Generate: true, Length: 40}); !alnum.MatchString(got) {
			t.Fatalf("default alphabet leaked symbols: %q", got)
		}
	}
}

func TestGenerateSecretSymbols(t *testing.T) {
	yes := true
	seen := false
	for i := 0; i < 200; i++ {
		if countIn(GenerateSecret(SecretSpec{Length: 40, Symbols: &yes}), SecretSymbols) > 0 {
			seen = true
			break
		}
	}
	if !seen {
		t.Error("symbols: true never produced a symbol in 200 tries")
	}

	// An explicit false stays alphanumeric even alongside a minimum.
	no := false
	for i := 0; i < 100; i++ {
		got := GenerateSecret(SecretSpec{Length: 30, Symbols: &no, MinSpecial: 3})
		if countIn(got, SecretSymbols) != 0 {
			t.Fatalf("symbols: false produced a symbol: %q", got)
		}
	}
}

// Asking for a minimum number of symbols implies wanting symbols at all —
// otherwise the option would be silently unsatisfiable.
func TestGenerateSecretMinSpecialImpliesSymbols(t *testing.T) {
	for i := 0; i < 100; i++ {
		got := GenerateSecret(SecretSpec{Length: 24, MinSpecial: 2})
		if n := countIn(got, SecretSymbols); n < 2 {
			t.Fatalf("minSpecial: 2 produced %d symbols: %q", n, got)
		}
	}
}

func TestGenerateSecretMinimums(t *testing.T) {
	yes := true
	for i := 0; i < 300; i++ {
		got := GenerateSecret(SecretSpec{Length: 16, Symbols: &yes, MinNumbers: 3, MinSpecial: 2})
		if n := countIn(got, SecretDigits); n < 3 {
			t.Fatalf("got %d digits, want >= 3: %q", n, got)
		}
		if n := countIn(got, SecretSymbols); n < 2 {
			t.Fatalf("got %d symbols, want >= 2: %q", n, got)
		}
	}
}

// Minimums that exceed the length must be trimmed rather than producing a value
// longer than asked for, or one that silently ignores the length.
func TestGenerateSecretMinimumsExceedingLength(t *testing.T) {
	yes := true
	got := GenerateSecret(SecretSpec{Length: 6, Symbols: &yes, MinNumbers: 5, MinSpecial: 5})
	if len(got) != 6 {
		t.Fatalf("length = %d, want 6: %q", len(got), got)
	}
	// Digits win when something has to give, matching the console.
	if n := countIn(got, SecretDigits); n < 5 {
		t.Errorf("expected digits to be preferred, got %d in %q", n, got)
	}
}

// Required characters must be spread by the shuffle, not left clustered at the
// front where a place-and-return implementation would leave them.
func TestGenerateSecretShufflesRequiredCharacters(t *testing.T) {
	const length, rounds = 16, 4000
	positions := make([]int, length)
	for r := 0; r < rounds; r++ {
		got := GenerateSecret(SecretSpec{Length: length, MinNumbers: 1})
		for i, c := range []byte(got) {
			if strings.IndexByte(SecretDigits, c) >= 0 {
				positions[i]++
			}
		}
	}
	avg := 0
	for _, p := range positions {
		avg += p
	}
	mean := float64(avg) / float64(length)
	for i, p := range positions {
		if delta := float64(p) - mean; delta/mean > 0.35 || delta/mean < -0.35 {
			t.Errorf("digit frequency at position %d is %d, far from the %.0f mean — not shuffled", i, p, mean)
		}
	}
}

func TestGenerateSecretIsRandom(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		v := GenerateSecret(SecretSpec{Length: 24})
		if seen[v] {
			t.Fatalf("repeated value after %d draws: %q", i, v)
		}
		seen[v] = true
	}
}

// The round trip the plan asks for: the same policy expressed in the console and
// in a manifest must draw from the same alphabet. The console's alphabet is read
// out of its source rather than restated here, so the two cannot drift without
// this failing.
func TestSymbolAlphabetMatchesTheConsole(t *testing.T) {
	src, err := os.ReadFile("../../web/src/composables/useGenerator.ts")
	if err != nil {
		t.Skipf("console generator not present in this tree: %v", err)
	}
	re := regexp.MustCompile(`export const SYMBOLS = '([^']*)'`)
	m := re.FindSubmatch(src)
	if m == nil {
		t.Fatal("could not find the SYMBOLS constant in useGenerator.ts — did it move or get renamed?")
	}
	if got := string(m[1]); got != SecretSymbols {
		t.Errorf("symbol alphabets have drifted:\n  console: %q\n  manifest: %q", got, SecretSymbols)
	}

	// And the classes either side agree, so "the same policy" really is the same.
	for _, pair := range []struct{ name, ts, go_ string }{
		{"UPPER", `export const UPPER = '([^']*)'`, SecretUpper},
		{"LOWER", `export const LOWER = '([^']*)'`, SecretLower},
		{"DIGITS", `export const DIGITS = '([^']*)'`, SecretDigits},
	} {
		m := regexp.MustCompile(pair.ts).FindSubmatch(src)
		if m == nil {
			t.Errorf("could not find %s in useGenerator.ts", pair.name)
			continue
		}
		if got := string(m[1]); got != pair.go_ {
			t.Errorf("%s drifted:\n  console: %q\n  manifest: %q", pair.name, got, pair.go_)
		}
	}
}
