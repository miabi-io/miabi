// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

// AdminPasswordEnv supplies the first admin's password to setup, so an automated install can choose it.
const AdminPasswordEnv = "MIABI_ADMIN_PASSWORD"

const (
	// MinAdminPasswordLength matches the control plane's own minimum, which refuses to seed a shorter one.
	MinAdminPasswordLength = 12
	// MaxAdminPasswordBytes is bcrypt's input limit; a longer password would fail the seed at startup.
	MaxAdminPasswordBytes = 72
)

// CheckAdminPassword reports why pw cannot be the first admin's password, so setup fails before
// anything is created rather than leaving a control plane that cannot start.
func CheckAdminPassword(pw string) error {
	if n := utf8.RuneCountInString(pw); n < MinAdminPasswordLength {
		return fmt.Errorf("%s must be at least %d characters (got %d)", AdminPasswordEnv, MinAdminPasswordLength, n)
	}
	if len(pw) > MaxAdminPasswordBytes {
		return fmt.Errorf("%s must be at most %d bytes (got %d)", AdminPasswordEnv, MaxAdminPasswordBytes, len(pw))
	}
	for _, r := range pw {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s must not contain control characters", AdminPasswordEnv)
		}
	}
	return nil
}
