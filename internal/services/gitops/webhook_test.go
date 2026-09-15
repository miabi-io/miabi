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
	// A source with no secret must verify nothing: the HMAC below would be taken
	// over an empty key, which any caller holding the body can reproduce, and the
	// webhook route is unauthenticated.
	if s.VerifyWebhook(&models.GitSource{}, good, body) {
		t.Error("source with no secret accepted a signature")
	}
	blank := hmac.New(sha256.New, nil)
	blank.Write(body)
	forged := "sha256=" + hex.EncodeToString(blank.Sum(nil))
	if s.VerifyWebhook(&models.GitSource{}, forged, body) {
		t.Error("signature forged under an empty secret accepted")
	}
}
