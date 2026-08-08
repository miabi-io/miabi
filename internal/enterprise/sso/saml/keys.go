//go:build enterprise

// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: LicenseRef-Miabi-Enterprise

package saml

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// loadOrGenerateKeyPair returns the SP signing key and certificate. When both PEM inputs are
// supplied they are parsed; otherwise a self-signed pair is generated in memory, which suffices
// for signing AuthnRequests since the IdP trusts the SP via the SP metadata it is configured with.
func loadOrGenerateKeyPair(keyPEM, certPEM string) (*rsa.PrivateKey, *x509.Certificate, error) {
	if keyPEM != "" && certPEM != "" {
		key, err := parseRSAKey(keyPEM)
		if err != nil {
			return nil, nil, err
		}
		cert, err := parseCert(certPEM)
		if err != nil {
			return nil, nil, err
		}
		return key, cert, nil
	}
	return generateSelfSigned()
}

func parseRSAKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("saml: invalid private key PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("saml: parse private key: %w", err)
	}
	rsaKey, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("saml: private key is not RSA")
	}
	return rsaKey, nil
}

func parseCert(pemStr string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("saml: invalid certificate PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func generateSelfSigned() (*rsa.PrivateKey, *x509.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "miabi-saml-sp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return key, cert, nil
}

func xmlMarshal(v any) ([]byte, error) { return xml.MarshalIndent(v, "", "  ") }
