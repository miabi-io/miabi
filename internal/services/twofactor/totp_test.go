// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package twofactor

import (
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

func TestGenerateReturnsSecretAndOtpauthURL(t *testing.T) {
	secret, url, err := Generate("Miabi", "user@example.com")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if secret == "" {
		t.Fatal("expected a non-empty secret")
	}
	if !strings.HasPrefix(url, "otpauth://totp/") {
		t.Fatalf("expected otpauth URL, got %q", url)
	}
	if !strings.Contains(url, "issuer=Miabi") {
		t.Fatalf("expected issuer in URL, got %q", url)
	}
}

func TestValidate(t *testing.T) {
	secret, _, err := Generate("Miabi", "user@example.com")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	if !Validate(secret, code) {
		t.Error("expected the current code to validate")
	}
	if Validate(secret, "000000") {
		t.Error("expected an obviously wrong code to fail")
	}
	if Validate("", code) {
		t.Error("expected validation against an empty secret to fail")
	}
}

func TestQRDataURI(t *testing.T) {
	_, url, err := Generate("Miabi", "user@example.com")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	uri, err := QRDataURI(url, 200)
	if err != nil {
		t.Fatalf("QRDataURI: %v", err)
	}
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatal("expected a PNG data URI")
	}
}

func TestIssuer(t *testing.T) {
	cases := []struct{ name, url, want string }{
		{"", "https://miabi.example.com", "Miabi (miabi.example.com)"},
		{"Acme Cloud", "https://cloud.acme.com/", "Acme Cloud (cloud.acme.com)"},
		{"", "https://miabi.example.com:8443/app", "Miabi (miabi.example.com)"},
		{"", "miabi.example.com", "Miabi (miabi.example.com)"},
		{"", "", "Miabi"},
		{"", "https://[2001:db8::1]:8443", "Miabi"},
		{"Acme: Cloud", "https://a.example", "Acme Cloud (a.example)"},
		{"  ", "https://a.example", "Miabi (a.example)"},
	}
	for _, tc := range cases {
		got := Issuer(tc.name, tc.url)
		if got != tc.want {
			t.Errorf("Issuer(%q, %q) = %q, want %q", tc.name, tc.url, got, tc.want)
		}
		if strings.Contains(got, ":") {
			t.Errorf("Issuer(%q, %q) = %q contains a colon, which splits the otpauth label", tc.name, tc.url, got)
		}
	}
}

// The otpauth URL must carry the full issuer, both as the label prefix and the issuer parameter.
func TestGenerateCarriesIssuer(t *testing.T) {
	_, u, err := Generate("Miabi (miabi.example.com)", "jonas@example.com")
	if err != nil {
		t.Fatal(err)
	}
	key, err := otp.NewKeyFromURL(u)
	if err != nil {
		t.Fatal(err)
	}
	if key.Issuer() != "Miabi (miabi.example.com)" || key.AccountName() != "jonas@example.com" {
		t.Errorf("issuer %q account %q", key.Issuer(), key.AccountName())
	}
}
