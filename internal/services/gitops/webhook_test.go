// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// A source with no secret configured must reject every signature, including one
// the caller computed under the empty key.
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
