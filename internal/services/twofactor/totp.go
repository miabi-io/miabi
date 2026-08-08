// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package twofactor wraps TOTP (RFC 6238) generation and validation for time-based two-factor
// authentication. The caller owns persistence and encryption of the secret; this package only deals with the
// cryptographic primitives.
package twofactor

import (
	"bytes"
	"encoding/base64"
	"image/png"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// Generate creates a fresh TOTP secret bound to issuer/account. It returns the
// base32 secret (to persist, encrypted) and the otpauth:// URL the client turns
// into a QR code for an authenticator app.
func Generate(issuer, account string) (secret, url string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: account})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

// QRDataURI renders an otpauth:// URL as a PNG QR code and returns it as a "data:image/png;base64,..." URI the
// browser can use directly as an <img> src. Rendering server-side keeps the secret off third-party QR
// services and avoids a client-side QR dependency.
func QRDataURI(otpauthURL string, size int) (string, error) {
	key, err := otp.NewKeyFromURL(otpauthURL)
	if err != nil {
		return "", err
	}
	img, err := key.Image(size, size)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// Validate reports whether code is a currently-valid TOTP for secret.
func Validate(secret, code string) bool {
	return totp.Validate(code, secret)
}
