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

package gitops

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestVerifyWebhook(t *testing.T) {
	s := &Service{}
	src := &models.GitSource{WebhookSecret: "topsecret"}
	body := []byte(`{"ref":"refs/heads/main"}`)

	mac := hmac.New(sha256.New, []byte(src.WebhookSecret))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !s.VerifyWebhook(src, good, body) {
		t.Error("valid GitHub HMAC signature rejected")
	}
	if !s.VerifyWebhook(src, "topsecret", body) {
		t.Error("valid GitLab token rejected")
	}
	if s.VerifyWebhook(src, "sha256=deadbeef", body) {
		t.Error("forged signature accepted")
	}
	if s.VerifyWebhook(src, "", body) {
		t.Error("empty signature accepted")
	}
}

// An unconfigured secret must reject every signature, including one computed under the empty key.
func TestVerifyWebhookRejectsAnUnconfiguredSecret(t *testing.T) {
	s := &Service{}
	src := &models.GitSource{}
	body := []byte(`{"ref":"refs/heads/main"}`)

	mac := hmac.New(sha256.New, nil)
	mac.Write(body)
	forged := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if s.VerifyWebhook(src, forged, body) {
		t.Error("HMAC under the empty key accepted")
	}
	if s.VerifyWebhook(src, "", body) {
		t.Error("empty signature accepted against an empty secret")
	}
}
