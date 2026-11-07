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

// Package webhooksig verifies inbound provider push webhooks.
package webhooksig

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// Bounded because the body must be read before the signature can be checked on these unauthenticated endpoints.
const MaxBodyBytes int64 = 1 << 20

// SignatureHeader is GitHub's HMAC header; TokenHeader is GitLab's bare-secret one.
const (
	SignatureHeader = "X-Hub-Signature-256"
	TokenHeader     = "X-Gitlab-Token"
)

// An empty secret never verifies: the HMAC under an empty key is computable by anyone.
func Verify(secret, signature string, body []byte) bool {
	signature = strings.TrimSpace(signature)
	if secret == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if hmac.Equal([]byte(expected), []byte(signature)) {
		return true
	}
	// GitLab sends the bare secret, so the comparison must be constant-time.
	return subtle.ConstantTimeCompare([]byte(signature), []byte(secret)) == 1
}
