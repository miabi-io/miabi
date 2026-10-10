// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

// Package sealed implements sealed secret values: a secret encrypted to a workspace's public sealing key so
// it can be committed to git and only Miabi can open it.
//
// A sealed value reads "sealed:v1:<keyVersion>:<base64(age ciphertext)>". The ciphertext is an age (X25519)
// file whose plaintext names the secret it was sealed for, so a value copied into another Secret refuses to
// open. Each workspace has its own key pair, which binds the value to the workspace as well. keyVersion is a
// hint for picking the private key; 0 means unknown and every retained key is tried.
package sealed

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"filippo.io/age"
)

// Prefix starts every sealed value of the current format.
const Prefix = "sealed:v1:"

// maxPlaintext bounds what Open will read, so a crafted value cannot exhaust memory.
const maxPlaintext = 1 << 20

var (
	// ErrFormat means the string is not a sealed value of a known format.
	ErrFormat = errors.New("not a sealed value (expected sealed:v1:<version>:<data>)")
	// ErrNoKey means none of the given private keys opens the value: it was sealed for another workspace, or
	// with a sealing key that no longer exists.
	ErrNoKey = errors.New("sealed value does not open with this workspace's sealing keys")
)

// NameMismatchError reports a value sealed for a different secret than the one it is declared on.
type NameMismatchError struct {
	SealedFor, DeclaredAs string
}

func (e *NameMismatchError) Error() string {
	return fmt.Sprintf("sealed value was sealed for secret %q, not %q", e.SealedFor, e.DeclaredAs)
}

type payload struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// GenerateKey returns a new key pair: the private identity ("AGE-SECRET-KEY-1…") and its public recipient
// ("age1…").
func GenerateKey() (identity, recipient string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	return id.String(), id.Recipient().String(), nil
}

// Recipient derives the public recipient of a private identity.
func Recipient(identity string) (string, error) {
	id, err := age.ParseX25519Identity(identity)
	if err != nil {
		return "", err
	}
	return id.Recipient().String(), nil
}

// ValidRecipient reports whether s parses as a public sealing key.
func ValidRecipient(s string) bool {
	_, err := age.ParseX25519Recipient(strings.TrimSpace(s))
	return err == nil
}

// Seal encrypts value for the secret name to recipient. keyVersion is recorded as a hint; pass 0 if unknown.
func Seal(recipient string, keyVersion int, name, value string) (string, error) {
	rcpt, err := age.ParseX25519Recipient(strings.TrimSpace(recipient))
	if err != nil {
		return "", fmt.Errorf("sealing key: %w", err)
	}
	if name == "" {
		return "", errors.New("a secret name is required")
	}
	if keyVersion < 0 {
		keyVersion = 0
	}
	pt, err := json.Marshal(payload{Name: name, Value: value})
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rcpt)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(pt); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return Prefix + strconv.Itoa(keyVersion) + ":" + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// IsSealed reports whether s looks like a sealed value. It does not validate it; use Parse for that.
func IsSealed(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), Prefix) }

// Parse splits a sealed value into its key-version hint and ciphertext.
func Parse(s string) (keyVersion int, ciphertext []byte, err error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), Prefix)
	if !ok {
		return 0, nil, ErrFormat
	}
	ver, data, ok := strings.Cut(rest, ":")
	if !ok {
		return 0, nil, ErrFormat
	}
	keyVersion, err = strconv.Atoi(ver)
	if err != nil || keyVersion < 0 {
		return 0, nil, ErrFormat
	}
	ciphertext, err = base64.StdEncoding.DecodeString(data)
	if err != nil || len(ciphertext) == 0 {
		return 0, nil, ErrFormat
	}
	return keyVersion, ciphertext, nil
}

// Open decrypts a sealed value declared on the secret name, trying each private identity in order, and
// returns the plaintext and the index of the identity that opened it.
func Open(s, name string, identities ...string) (value string, used int, err error) {
	_, ct, err := Parse(s)
	if err != nil {
		return "", -1, err
	}
	for i, raw := range identities {
		id, perr := age.ParseX25519Identity(raw)
		if perr != nil {
			return "", -1, fmt.Errorf("sealing key %d: %w", i, perr)
		}
		r, derr := age.Decrypt(bytes.NewReader(ct), id)
		if derr != nil {
			var noMatch *age.NoIdentityMatchError
			if errors.As(derr, &noMatch) {
				continue
			}
			return "", -1, fmt.Errorf("open sealed value: %w", derr)
		}
		pt, rerr := io.ReadAll(io.LimitReader(r, maxPlaintext+1))
		if rerr != nil {
			return "", -1, fmt.Errorf("open sealed value: %w", rerr)
		}
		if len(pt) > maxPlaintext {
			return "", -1, errors.New("sealed value is larger than 1 MiB")
		}
		var p payload
		if jerr := json.Unmarshal(pt, &p); jerr != nil {
			return "", -1, fmt.Errorf("open sealed value: %w", jerr)
		}
		if p.Name != name {
			return "", -1, &NameMismatchError{SealedFor: p.Name, DeclaredAs: name}
		}
		return p.Value, i, nil
	}
	return "", -1, ErrNoKey
}

// Fingerprint identifies a sealed value without revealing anything about the plaintext. Every seal draws a
// fresh ephemeral key, so re-sealing the same value yields a different fingerprint.
func Fingerprint(s string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(s)))
	return hex.EncodeToString(sum[:16])
}
