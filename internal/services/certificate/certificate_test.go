// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package certificate

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

func TestHostMatches(t *testing.T) {
	cases := []struct {
		sans []string
		host string
		want bool
	}{
		{[]string{"app.example.com"}, "app.example.com", true},
		{[]string{"app.example.com"}, "other.example.com", false},
		{[]string{"*.example.com"}, "app.example.com", true},
		{[]string{"*.example.com"}, "a.b.example.com", false}, // only one label
		{[]string{"*.example.com"}, "example.com", false},     // bare apex not covered
		{[]string{"*.example.com"}, "APP.example.com", true},  // case-insensitive (caller lowercases)
		{[]string{"a.com", "*.b.com"}, "x.b.com", true},
		{nil, "app.example.com", false},
		{[]string{"app.example.com"}, "", false},
	}
	for _, c := range cases {
		if got := hostMatches(c.sans, c.host); got != c.want {
			t.Errorf("hostMatches(%v, %q) = %v, want %v", c.sans, c.host, got, c.want)
		}
	}
}

// fakeDomains implements DomainLister for the import-gate tests.
type fakeDomains struct{ list []models.Domain }

func (f fakeDomains) ListByWorkspace(uint) ([]models.Domain, error) { return f.list, nil }

// fakeWorkspaces implements WorkspacePrivilege for the import-gate tests.
type fakeWorkspaces struct{ privileged bool }

func (f fakeWorkspaces) FindByID(id uint) (*models.Workspace, error) {
	return &models.Workspace{ID: id, Privileged: f.privileged}, nil
}

func TestValidateAgainstDomains(t *testing.T) {
	meta := &certMeta{commonName: "example.com", dnsNames: []string{"example.com", "*.example.com"}}
	verified := []models.Domain{{Name: "example.com", Verified: true}}

	// No domain registered → blocked.
	s := &Service{domains: fakeDomains{}}
	if err := s.validateAgainstDomains(1, meta); !errors.Is(err, ErrNoDomains) {
		t.Errorf("no domains: got %v, want ErrNoDomains", err)
	}

	// Cert names all covered by a verified domain → ok.
	s = &Service{domains: fakeDomains{list: verified}}
	if err := s.validateAgainstDomains(1, meta); err != nil {
		t.Errorf("covered cert should pass, got %v", err)
	}

	// A SAN for a domain the workspace does not control → blocked.
	other := &certMeta{commonName: "example.com", dnsNames: []string{"example.com", "evil.org"}}
	if err := s.validateAgainstDomains(1, other); !errors.Is(err, ErrDomainMismatch) {
		t.Errorf("uncovered SAN: got %v, want ErrDomainMismatch", err)
	}

	// Nil lister (unwired/tests) skips the checks.
	if err := (&Service{}).validateAgainstDomains(1, other); err != nil {
		t.Errorf("nil domain lister should skip, got %v", err)
	}
}

// The hijack: registering a domain proves nothing — anyone can register "console.example.com" — and
// Goma loads every inline cert into one global map where an exact match beats ACME. A certificate on
// an unverified name therefore takes that hostname's TLS from whoever really serves it.
func TestValidateAgainstDomainsRequiresAVerifiedDomain(t *testing.T) {
	meta := &certMeta{commonName: "console.example.com", dnsNames: []string{"console.example.com"}}
	unverified := []models.Domain{{Name: "example.com"}}

	s := &Service{domains: fakeDomains{list: unverified}}
	if err := s.validateAgainstDomains(1, meta); !errors.Is(err, ErrDomainUnverified) {
		t.Fatalf("unverified domain: got %v, want ErrDomainUnverified", err)
	}

	// A privileged workspace may SERVE an unverified domain (routeServeState says so), so it may
	// hold a certificate for one too — the two rules have to agree.
	s = &Service{domains: fakeDomains{list: unverified}, workspaces: fakeWorkspaces{privileged: true}}
	if err := s.validateAgainstDomains(1, meta); err != nil {
		t.Fatalf("privileged workspace should be allowed, got %v", err)
	}

	// Unwired privilege check means not privileged: this gate fails closed.
	s = &Service{domains: fakeDomains{list: unverified}}
	if err := s.validateAgainstDomains(1, meta); err == nil {
		t.Fatal("an unwired privilege check must not grant the exemption")
	}
}

// A ban outranks verification — that is the point of banning a name.
func TestValidateAgainstDomainsRefusesABannedDomain(t *testing.T) {
	meta := &certMeta{commonName: "app.example.com", dnsNames: []string{"app.example.com"}}
	banned := []models.Domain{{Name: "example.com", Verified: true, Banned: true}}

	for _, privileged := range []bool{false, true} {
		s := &Service{domains: fakeDomains{list: banned}, workspaces: fakeWorkspaces{privileged: privileged}}
		if err := s.validateAgainstDomains(1, meta); !errors.Is(err, ErrDomainBanned) {
			t.Fatalf("banned domain (privileged=%v): got %v, want ErrDomainBanned", privileged, err)
		}
	}
}

func TestCoveringDomain(t *testing.T) {
	domains := []models.Domain{{Name: "Example.com", Verified: true}}
	for _, c := range []struct {
		name string
		want bool
	}{
		{"example.com", true},
		{"app.example.com", true},
		{"*.example.com", true},
		{"example.org", false},
		{"notexample.com", false},
	} {
		got := coveringDomain(c.name, domains) != nil
		if got != c.want {
			t.Errorf("coveringDomain(%q) matched = %v, want %v", c.name, got, c.want)
		}
	}
	// The caller needs the domain itself, not a yes/no, so it can check verified and banned.
	if d := coveringDomain("app.example.com", domains); d == nil || !d.Verified {
		t.Errorf("coveringDomain returned %+v, want the matched domain with its flags", d)
	}
}

func TestParse(t *testing.T) {
	certPEM, keyPEM := genCert(t, "example.com", []string{"example.com", "*.example.com"})

	meta, err := parse(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse valid cert: %v", err)
	}
	if meta.commonName != "example.com" {
		t.Errorf("commonName = %q, want example.com", meta.commonName)
	}
	if len(meta.dnsNames) != 2 {
		t.Errorf("dnsNames = %v, want 2 SANs", meta.dnsNames)
	}
	if meta.notAfter.Before(time.Now()) {
		t.Errorf("notAfter %v is in the past", meta.notAfter)
	}

	// Missing material.
	if _, err := parse("", keyPEM); !errors.Is(err, ErrPEMRequired) {
		t.Errorf("parse empty cert: got %v, want ErrPEMRequired", err)
	}

	// Mismatched key (a second, unrelated key).
	_, otherKey := genCert(t, "other.com", nil)
	if _, err := parse(certPEM, otherKey); !errors.Is(err, ErrInvalidPEM) {
		t.Errorf("parse mismatched key: got %v, want ErrInvalidPEM", err)
	}
}

// genCert returns a self-signed cert + key PEM for testing.
func genCert(t *testing.T, cn string, sans []string) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     sans,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM
}
