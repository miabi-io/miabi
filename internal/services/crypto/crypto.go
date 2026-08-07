// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package crypto provides AES-GCM encryption for secrets stored at rest.
// Init must be called once at startup with the configured key. When no key is
// configured, values are base64-encoded (dev convenience) — never use that in
// production.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

const (
	// encryptedPrefix marks a value encrypted with the master key (KEK) — the
	// historical/platform-scoped format.
	encryptedPrefix = "enc:"
	// wsPrefix marks a value encrypted with a per-workspace DEK:
	// "e2:w:<workspaceID>:<keyVersion>:<base64(nonce|ct)>". The header is
	// self-describing so Decrypt resolves the key without a workspace argument.
	wsPrefix = "e2:w:"
)

// Keyring resolves per-workspace data-encryption keys (DEKs), unwrapping them
// from the master KEK. Injected at startup via SetKeyring; nil means
// per-workspace encryption is unavailable and EncryptWS falls back to the master
// key (safe during rollout / dev).
type Keyring interface {
	// ActiveDEK returns the workspace's current key version + DEK, creating one on
	// first use.
	ActiveDEK(workspaceID uint) (version int, dek []byte, err error)
	// DEK returns a specific key version's DEK (to decrypt older ciphertext).
	DEK(workspaceID uint, version int) (dek []byte, err error)
}

var (
	key     []byte
	keyring Keyring
	keyMu   sync.RWMutex
)

// Init derives the AES-256 master key (KEK) from the secret. Empty secret
// disables encryption (dev). Does not clear an injected keyring.
func Init(secret string) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if secret == "" {
		key = nil
		return
	}
	h := sha256.Sum256([]byte(secret))
	key = h[:]
}

// SetKeyring wires the per-workspace keyring (call once at startup, after Init).
func SetKeyring(kr Keyring) {
	keyMu.Lock()
	defer keyMu.Unlock()
	keyring = kr
}

// CurrentKeyring returns the wired keyring (nil if none). Lets the composition
// root reuse the live instance (e.g. for crypto-shred / rotation) instead of
// constructing a second one with a separate cache.
func CurrentKeyring() Keyring {
	keyMu.RLock()
	defer keyMu.RUnlock()
	return keyring
}

// Enabled reports whether a real encryption key is configured.
func Enabled() bool {
	keyMu.RLock()
	defer keyMu.RUnlock()
	return key != nil
}

// IsEncrypted reports whether a stored value is AES-encrypted.
func IsEncrypted(s string) bool {
	return strings.HasPrefix(s, encryptedPrefix)
}

// LooksEncrypted reports whether a stored value carries one of the envelope
// prefixes this package produces (master "enc:" or per-workspace "e2:w:"). Bare
// legacy plaintext returns false, so a caller can Decrypt only the values it
// actually encrypted and pass legacy plaintext through untouched (Decrypt would
// otherwise base64-decode a bare value and corrupt plaintext that happens to be
// valid base64).
func LooksEncrypted(s string) bool {
	return strings.HasPrefix(s, encryptedPrefix) || strings.HasPrefix(s, wsPrefix)
}

// Encrypt returns the encrypted, prefixed, base64-encoded form of plaintext.
func Encrypt(plaintext string) (string, error) {
	keyMu.RLock()
	k := key
	keyMu.RUnlock()
	if k == nil {
		return base64.StdEncoding.EncodeToString([]byte(plaintext)), nil
	}
	ct, err := aesGCMEncrypt(k, []byte(plaintext))
	if err != nil {
		return "", fmt.Errorf("crypto: failed to encrypt: %w", err)
	}
	return encryptedPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// devMasterKey is the fallback HMAC key for DeriveToken when no encryption key
// is configured (dev only). It keeps derived tokens stable within a dev instance
// — never security-relevant, since dev has no real secrets to protect.
var devMasterKey = []byte("miabi-dev-insecure-master-key")

// DeriveToken returns a stable, high-entropy token derived from the master key
// via HMAC-SHA256(masterKey, label), prefixed and URL-safe base64. It lets the
// platform mint internal shared secrets (e.g. the registry platform credential)
// deterministically from the already-configured encryption key: no separate
// config or storage, and every process that shares the key derives the identical
// value. A given label always yields the same token for the same key, and
// different labels yield independent tokens.
func DeriveToken(label string) string {
	keyMu.RLock()
	k := key
	keyMu.RUnlock()
	if k == nil {
		k = devMasterKey
	}
	mac := hmac.New(sha256.New, k)
	mac.Write([]byte(label))
	return "mrt_" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// DeriveTokenFrom is DeriveToken for a master-key secret other than this
// process's. Disaster recovery needs it: after opening a backup's identity
// envelope, the restore path must check that the key inside is the one the
// recovery point was taken under — a comparison it cannot make with the running
// process's key, because on a fresh host that key is a stranger.
//
// An empty secret yields "" rather than the dev fallback, so a missing key is
// never mistaken for a matching fingerprint.
func DeriveTokenFrom(secret, label string) string {
	if secret == "" {
		return ""
	}
	h := sha256.Sum256([]byte(secret))
	mac := hmac.New(sha256.New, h[:])
	mac.Write([]byte(label))
	return "mrt_" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// EncryptWS encrypts plaintext with the workspace's data-encryption key,
// producing the self-describing "e2:w:<ws>:<ver>:<b64>" form. Falls back to the
// master key (Encrypt) when no keyring is wired or no master key is configured —
// so callers can adopt it safely before the keyring exists, and dev still works.
func EncryptWS(workspaceID uint, plaintext string) (string, error) {
	keyMu.RLock()
	kr := keyring
	k := key
	keyMu.RUnlock()
	if kr == nil || k == nil {
		return Encrypt(plaintext)
	}
	ver, dek, err := kr.ActiveDEK(workspaceID)
	if err != nil {
		return "", fmt.Errorf("crypto: resolve workspace key: %w", err)
	}
	ct, err := aesGCMEncrypt(dek, []byte(plaintext))
	if err != nil {
		return "", fmt.Errorf("crypto: failed to encrypt: %w", err)
	}
	return fmt.Sprintf("%s%d:%d:", wsPrefix, workspaceID, ver) + base64.StdEncoding.EncodeToString(ct), nil
}

// Reencrypt rewrites a stored value under the workspace's current active DEK if
// it isn't already (legacy master-key, dev, or an older DEK version → active
// version). Returns (newValue, changed, err). A no-op (changed=false) when there
// is no keyring, the value is empty, or it is already at the active version — so
// callers can run it idempotently across a workspace's rows during rotation.
func Reencrypt(workspaceID uint, stored string) (string, bool, error) {
	keyMu.RLock()
	kr := keyring
	keyMu.RUnlock()
	if kr == nil || stored == "" {
		return stored, false, nil
	}
	ver, _, err := kr.ActiveDEK(workspaceID)
	if err != nil {
		return "", false, fmt.Errorf("crypto: resolve workspace key: %w", err)
	}
	if strings.HasPrefix(stored, fmt.Sprintf("%s%d:%d:", wsPrefix, workspaceID, ver)) {
		return stored, false, nil // already at the active version
	}
	pt, err := Decrypt(stored)
	if err != nil {
		return "", false, err
	}
	enc, err := EncryptWS(workspaceID, pt)
	if err != nil {
		return "", false, err
	}
	return enc, true, nil
}

// Decrypt reverses Encrypt / EncryptWS. It is self-describing: a per-workspace
// ("e2:w:") value resolves its DEK via the keyring; an "enc:" value uses the
// master key; a bare value is dev passthrough.
func Decrypt(stored string) (string, error) {
	if strings.HasPrefix(stored, wsPrefix) {
		return decryptWS(stored)
	}
	if IsEncrypted(stored) {
		keyMu.RLock()
		k := key
		keyMu.RUnlock()
		if k == nil {
			return "", fmt.Errorf("crypto: encrypted value found but no encryption key configured")
		}
		encoded := strings.TrimPrefix(stored, encryptedPrefix)
		ct, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", fmt.Errorf("crypto: failed to decode ciphertext: %w", err)
		}
		pt, err := aesGCMDecrypt(k, ct)
		if err != nil {
			return "", fmt.Errorf("crypto: failed to decrypt: %w", err)
		}
		return string(pt), nil
	}
	decoded, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return stored, nil
	}
	return string(decoded), nil
}

// decryptWS decrypts an "e2:w:<ws>:<ver>:<b64>" value via the keyring.
func decryptWS(stored string) (string, error) {
	keyMu.RLock()
	kr := keyring
	keyMu.RUnlock()
	if kr == nil {
		return "", fmt.Errorf("crypto: per-workspace ciphertext found but no keyring configured")
	}
	rest := strings.TrimPrefix(stored, wsPrefix)
	parts := strings.SplitN(rest, ":", 3) // <ws>, <ver>, <b64>
	if len(parts) != 3 {
		return "", fmt.Errorf("crypto: malformed per-workspace ciphertext")
	}
	ws, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("crypto: bad workspace id in ciphertext: %w", err)
	}
	ver, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", fmt.Errorf("crypto: bad key version in ciphertext: %w", err)
	}
	dek, err := kr.DEK(uint(ws), ver)
	if err != nil {
		return "", fmt.Errorf("crypto: resolve workspace key: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("crypto: failed to decode ciphertext: %w", err)
	}
	pt, err := aesGCMDecrypt(dek, ct)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to decrypt: %w", err)
	}
	return string(pt), nil
}

func aesGCMEncrypt(k, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func aesGCMDecrypt(k, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("crypto: ciphertext too short")
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}
