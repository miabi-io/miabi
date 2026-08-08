// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// libSQL (sqld) authenticates HTTP clients with an EdDSA-signed JWT rather than a password. Each instance gets
// its own Ed25519 keypair: sqld starts with the public key and verifies tokens minted with the private one,
// which is stored encrypted so a token can be re-minted. The client token lives on the implicit Database row.

const (
	libsqlHTTPPort = 8080
	libsqlDataDir  = "/var/lib/sqld"
	// libsqlDatabaseName is the name of the single implicit logical database every
	// libSQL instance hosts (libSQL has no notion of named databases without
	// namespaces, which we keep disabled).
	libsqlDatabaseName = "default"
	libsqlUsername     = "libsql"
)

// libsqlNewKeypair generates an Ed25519 keypair for a libSQL instance and returns
// the encrypted private key (to persist on the instance) together with a freshly
// minted, non-expiring read-write client token signed by it.
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

// libsqlMintToken mints a non-expiring full-access (read-write) client token
// signed with EdDSA. sqld verifies it with the matching public key; the "a":"rw"
// claim is libSQL's grant of read-write access.
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

// libsqlServerEnv builds the sqld container environment for a libSQL instance: a single-node primary
// serving HTTP clients on 8080, verifying JWTs with the instance's public key. sqld accepts the key as
// URL-safe base64 (no padding) of the raw Ed25519 public key.
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
