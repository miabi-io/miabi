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

package declarative

import (
	"crypto/rand"
	"math/big"
)

// Must match the console generator's alphabet (useGenerator.ts); quotes, backslash and backtick break shells and YAML.
const (
	SecretLower   = "abcdefghijklmnopqrstuvwxyz"
	SecretUpper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	SecretDigits  = "0123456789"
	SecretSymbols = "!#$%&()*+,-./:;<=>?@[]^_{|}~"
)

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

// Minimums are met by placing then shuffling; regenerating until a constraint holds would skew the distribution.
func shuffle(b []byte) {
	for i := len(b) - 1; i > 0; i-- {
		j := randIndex(i + 1)
		b[i], b[j] = b[j], b[i]
	}
}

// GenerateSecret trims minimums that exceed the length (symbols first) rather than silently disobeying them.
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
