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

package database

import (
	"crypto/ed25519"
	crand "crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// sqld verifies EdDSA JWTs with the instance public key; the private key is stored encrypted so tokens can be re-minted.

const (
	libsqlHTTPPort = 8080
	libsqlDataDir  = "/var/lib/sqld"
	// libSQL has no named databases without namespaces, which we keep disabled.
	libsqlDatabaseName = "default"
	libsqlUsername     = "libsql"
)

func libsqlNewKeypair(workspaceID uint) (privEnc, token string, err error) {
	_, priv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		return "", "", err
	}
	token, err = libsqlMintToken(priv)
	if err != nil {
		return "", "", err
	}
	privEnc, err = crypto.EncryptWS(workspaceID, base64.StdEncoding.EncodeToString(priv))
	if err != nil {
		return "", "", err
	}
	return privEnc, token, nil
}

// The "a":"rw" claim is libSQL's grant of read-write access.
func libsqlMintToken(priv ed25519.PrivateKey) (string, error) {
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"a": "rw"})
	return tok.SignedString(priv)
}

func libsqlPrivateKey(privEnc string) (ed25519.PrivateKey, error) {
	dec, err := crypto.Decrypt(privEnc)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(dec)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid libsql private key length %d", len(raw))
	}
	return ed25519.PrivateKey(raw), nil
}

// sqld wants the key as unpadded URL-safe base64 of the raw Ed25519 public key.
func libsqlServerEnv(inst *models.DatabaseInstance) ([]string, error) {
	priv, err := libsqlPrivateKey(inst.JWTPrivateKeyEnc)
	if err != nil {
		return nil, err
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("derive libsql public key")
	}
	return []string{
		"SQLD_NODE=primary",
		fmt.Sprintf("SQLD_HTTP_LISTEN_ADDR=0.0.0.0:%d", libsqlHTTPPort),
		"SQLD_AUTH_JWT_KEY=" + base64.RawURLEncoding.EncodeToString(pub),
	}, nil
}
