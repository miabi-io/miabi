// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package mwcatalog

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/services/crypto"
)

func oidcRule(overrides map[string]any) map[string]any {
	rule := map[string]any{
		"issuer":       "https://id.example.com/application/o/app/",
		"clientId":     "goma",
		"clientSecret": "s3cr3t",
	}
	for k, v := range overrides {
		if v == nil {
			delete(rule, k)
			continue
		}
		rule[k] = v
	}
	return rule
}

// An issuer alone is enough: discovery supplies the endpoints and the keys.
func TestOIDCIssuerIsEnough(t *testing.T) {
	if err := Validate("oidc", oidcRule(nil)); err != nil {
		t.Errorf("Validate = %v, want nil", err)
	}
}

func TestOIDCNeedsSomethingToVerifyWith(t *testing.T) {
	// No issuer, no known provider, no endpoints: the route would not be guarded.
	err := Validate("oidc", oidcRule(map[string]any{"issuer": nil}))
	if !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("Validate = %v, want ErrInvalidRule", err)
	}

	// Endpoints alone, but nothing to verify a token against.
	err = Validate("oidc", oidcRule(map[string]any{
		"issuer": nil,
		"endpoint": map[string]any{
			"authUrl":  "https://id.example.com/authorize",
			"tokenUrl": "https://id.example.com/token",
		},
	}))
	if !errors.Is(err, ErrInvalidRule) || !strings.Contains(err.Error(), "verified") {
		t.Errorf("Validate = %v, want it to ask for jwksUrl or userInfoUrl", err)
	}

	// With a JWKS it is complete.
	if err := Validate("oidc", oidcRule(map[string]any{
		"issuer": nil,
		"endpoint": map[string]any{
			"authUrl":  "https://id.example.com/authorize",
			"tokenUrl": "https://id.example.com/token",
			"jwksUrl":  "https://id.example.com/jwks",
		},
	})); err != nil {
		t.Errorf("Validate(explicit endpoints) = %v, want nil", err)
	}

	// A known provider carries its own endpoints.
	if err := Validate("oidc", oidcRule(map[string]any{"issuer": nil, "provider": "google"})); err != nil {
		t.Errorf("Validate(google) = %v, want nil", err)
	}
}

func TestOIDCRejectsBadSession(t *testing.T) {
	cases := map[string]any{
		"unknown store":    map[string]any{"store": "postgres"},
		"invalid ttl":      map[string]any{"ttl": "forever"},
		"invalid idle":     map[string]any{"idleTimeout": "10 minutes"},
		"unknown sameSite": map[string]any{"cookie": map[string]any{"sameSite": "none-ish"}},
	}
	for name, session := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Validate("oidc", oidcRule(map[string]any{"session": session})); !errors.Is(err, ErrInvalidRule) {
				t.Errorf("Validate = %v, want ErrInvalidRule", err)
			}
		})
	}

	valid := map[string]any{
		"store": "redis", "ttl": "12h", "idleTimeout": "30m",
		"cookie": map[string]any{"sameSite": "lax", "path": "/"},
	}
	if err := Validate("oidc", oidcRule(map[string]any{"session": valid})); err != nil {
		t.Errorf("Validate(valid session) = %v, want nil", err)
	}
}

func TestOIDCRejectsUnknownClaimSource(t *testing.T) {
	err := Validate("oidc", oidcRule(map[string]any{"claimsSource": []any{"refresh_token"}}))
	if !errors.Is(err, ErrInvalidRule) {
		t.Errorf("Validate = %v, want ErrInvalidRule", err)
	}
	if err := Validate("oidc", oidcRule(map[string]any{"claimsSource": []any{"id_token", "userinfo"}})); err != nil {
		t.Errorf("Validate(known sources) = %v, want nil", err)
	}
}

// session.secret sits inside an object. Before nested handling it would have
// been stored in clear, returned unredacted, and wiped by any edit.
func TestNestedSecretIsEncryptedRedactedAndKept(t *testing.T) {
	rule := oidcRule(map[string]any{
		"session": map[string]any{"store": "cookie", "secret": "session-key"},
	})

	enc, err := EncryptSecrets("oidc", 1, rule)
	if err != nil {
		t.Fatalf("EncryptSecrets: %v", err)
	}
	session, _ := enc["session"].(map[string]any)
	if session == nil {
		t.Fatal("the session block did not survive encryption")
	}
	if crypto.Enabled() && session["secret"] == "session-key" {
		t.Error("session.secret was stored in clear")
	}
	// The original must not have been mutated.
	if original, _ := rule["session"].(map[string]any); original["secret"] != "session-key" {
		t.Error("EncryptSecrets mutated the rule it was given")
	}

	dec, err := DecryptSecrets("oidc", enc)
	if err != nil {
		t.Fatalf("DecryptSecrets: %v", err)
	}
	if s, _ := dec["session"].(map[string]any); s["secret"] != "session-key" {
		t.Errorf("round trip lost the value: %v", s["secret"])
	}

	red := Redact("oidc", enc)
	rs, _ := red["session"].(map[string]any)
	if rs["secret"] != RedactedSentinel {
		t.Errorf("session.secret = %v in an API response, want it redacted", rs["secret"])
	}
	if red["clientSecret"] != RedactedSentinel {
		t.Errorf("clientSecret = %v, want it redacted", red["clientSecret"])
	}

	// An edit that returns the redacted rule keeps both stored values.
	merged := MergeKeptSecrets("oidc", red, enc)
	ms, _ := merged["session"].(map[string]any)
	if ms["secret"] != session["secret"] {
		t.Error("editing the policy wiped the stored session secret")
	}
	if merged["clientSecret"] != enc["clientSecret"] {
		t.Error("editing the policy wiped the stored client secret")
	}
}

// A secret the user actually retyped must replace the stored one.
func TestNestedSecretAcceptsANewValue(t *testing.T) {
	stored := map[string]any{"session": map[string]any{"secret": "old"}}
	incoming := map[string]any{"session": map[string]any{"secret": "new"}}

	merged := MergeKeptSecrets("oidc", incoming, stored)
	if s, _ := merged["session"].(map[string]any); s["secret"] != "new" {
		t.Errorf("session.secret = %v, want the new value", s["secret"])
	}
}

func TestSSOPresetIsValidAsFarAsItGoes(t *testing.T) {
	var preset *Preset
	all := Presets()
	for i := range all {
		if all[i].Key == "sso" {
			preset = &all[i]
		}
	}
	if preset == nil {
		t.Fatal("no sso preset")
	}
	if preset.Type != "oidc" {
		t.Errorf("preset type = %q, want oidc", preset.Type)
	}
	// A preset deliberately leaves the credentials blank, so it validates only
	// once the issuer and client are filled in.
	rule := map[string]any{}
	for k, v := range preset.Rule {
		rule[k] = v
	}
	rule["issuer"] = "https://id.example.com/"
	rule["clientId"] = "goma"
	rule["clientSecret"] = "x"
	if err := Validate("oidc", rule); err != nil {
		t.Errorf("Validate(filled-in preset) = %v, want nil", err)
	}
}
