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

// Package twofactor wraps TOTP (RFC 6238); the caller owns persistence and encryption of the secret.
package twofactor

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"net/url"
	"strings"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// DefaultIssuer names the platform in authenticator apps when nothing better is known.
const DefaultIssuer = "Miabi"

// Issuer builds the name an authenticator app shows for this instance
func Issuer(name, publicURL string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, ":", ""))
	if name == "" {
		name = DefaultIssuer
	}
	var host string
	raw := strings.TrimSpace(publicURL)
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if u, err := url.Parse(raw); err == nil {
		host = u.Hostname()
	}
	if host == "" || strings.Contains(host, ":") {
		return name
	}
	return name + " (" + host + ")"
}

func Generate(issuer, account string) (secret, url string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: account})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

// Rendering server-side keeps the secret off third-party QR services.
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
