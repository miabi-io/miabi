// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"crypto/rand"
	"math/big"
)

// The alphabet a generated secret draws from. SecretSymbols is character-for-
// character the console generator's set (web/src/composables/useGenerator.ts):
// a policy written in a manifest and the same policy set in the UI must produce
// values from the same alphabet, or `generate:` and the generator are two
// different features wearing one name.
//
// Quotes, backslash and backtick are excluded on both sides: they are what break
// a value pasted into a shell, a YAML file or a connection string, and dropping
// them costs about 0.2 bits per character.
const (
	SecretLower   = "abcdefghijklmnopqrstuvwxyz"
	SecretUpper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	SecretDigits  = "0123456789"
	SecretSymbols = "!#$%&()*+,-./:;<=>?@[]^_{|}~"
)

// DefaultSecretLength is the length used when a manifest asks to generate a
// secret without saying how long.
const DefaultSecretLength = 32

func randIndex(n int) int {
	if n <= 1 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {

		panic("declarative: system entropy unavailable: " + err.Error())
	}
	return int(v.Int64())
}

func randChar(charset string) byte { return charset[randIndex(len(charset))] }

// shuffle is Fisher–Yates over crypto randomness.
//
// This is how the minimum counts are met: place the required characters, fill
// the rest, then shuffle. Regenerating until a constraint happens to hold would
// skew the distribution toward whichever strings satisfy it most easily.
func shuffle(b []byte) {
	for i := len(b) - 1; i > 0; i-- {
		j := randIndex(i + 1)
		b[i], b[j] = b[j], b[i]
	}
}

// GenerateSecret produces a value satisfying the spec's generation policy.
//
// Conflicts are resolved before generating rather than silently disobeyed: a
// length shorter than the minimums it demands trims them (symbols first, since
// a policy is more often "at least N digits"), and the alphabet always includes
// letters and digits.
func GenerateSecret(spec SecretSpec) string {
	length := spec.Length
	if length <= 0 {
		length = DefaultSecretLength
	}

	charset := SecretLower + SecretUpper + SecretDigits
	symbols := spec.WantSymbols()
	if symbols {
		charset += SecretSymbols
	}

	minNumbers := clampMin(spec.MinNumbers, length)
	minSpecial := 0
	if symbols {
		minSpecial = clampMin(spec.MinSpecial, length)
	}
	if over := minNumbers + minSpecial - length; over > 0 {
		fromSymbols := min(minSpecial, over)
		minSpecial -= fromSymbols
		minNumbers -= over - fromSymbols
	}

	out := make([]byte, 0, length)
	for i := 0; i < minNumbers; i++ {
		out = append(out, randChar(SecretDigits))
	}
	for i := 0; i < minSpecial; i++ {
		out = append(out, randChar(SecretSymbols))
	}
	for len(out) < length {
		out = append(out, randChar(charset))
	}
	shuffle(out)
	return string(out)
}

func clampMin(v, max int) int {
	if v < 0 {
		return 0
	}
	if v > max {
		return max
	}
	return v
}
