// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dr holds the disaster-recovery primitives shared by the running platform and the
// host-side `miabi restore`: the sealed identity envelope and the naming of a recovery point's
// artifacts. It depends on nothing else in Miabi — the restore path runs on a bare host.
package dr

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/argon2"
)

// IdentitySchema is the envelope payload version. Restore refuses a schema it
// does not know rather than guessing at fields.
const IdentitySchema = 1

// Envelope framing. The header is self-describing so a future format change is a
// clean refusal instead of a garbled decrypt.
const (
	magic       = "MBID1"
	saltLen     = 16
	nonceLen    = 12
	keyLen      = 32
	headerLen   = len(magic) + 1 + saltLen + nonceLen
	formatVer   = 1
	minPassLen  = 12
	argonTime   = 3
	argonMemory = 64 * 1024 // KiB
	argonLanes  = 4
)

var (
	// ErrBadPassphrase means the envelope did not authenticate: a wrong
	// passphrase, or a tampered file. The two are indistinguishable by design.
	ErrBadPassphrase = errors.New("identity envelope did not decrypt: wrong passphrase or corrupt file")
	// ErrNotEnvelope means the bytes are not an identity envelope at all.
	ErrNotEnvelope = errors.New("not a Miabi identity envelope")
	// ErrWeakPassphrase is returned when a passphrase is too weak to protect the
	// most sensitive artifact the platform produces.
	ErrWeakPassphrase = fmt.Errorf("backup passphrase must be at least %d characters and mix letters with digits or symbols", minPassLen)
)

// Identity is everything needed to rebuild the stack that owns a control-plane dump. Without it a
// fresh host lists every workspace and decrypts no secret, since a fresh install generates a new
// key. The database and Redis passwords are NOT here — those belong to the new host.
type Identity struct {
	Schema int `json:"schema"`

	InstallID    string `json:"install_id"`
	MiabiVersion string `json:"miabi_version"`
	DBSchema     string `json:"db_schema,omitempty"`

	// EncryptionKey is MIABI_ENCRYPTION_KEY — the master key every workspace
	// data-encryption key is wrapped under. It is the reason this envelope exists.
	EncryptionKey string `json:"encryption_key"`
	// JWTSecret keeps issued API tokens and sessions valid across the restore.
	JWTSecret string `json:"jwt_secret"`

	Domain          string `json:"domain,omitempty"`
	WebURL          string `json:"web_url,omitempty"`
	ControlURL      string `json:"control_url,omitempty"`
	ACMEEmail       string `json:"acme_email,omitempty"`
	RegistryHost    string `json:"registry_host,omitempty"`
	RegistryStorage string `json:"registry_storage,omitempty"`

	NetworkName         string `json:"network_name,omitempty"`
	NetworkSubnet       string `json:"network_subnet,omitempty"`
	InternalNetworkName string `json:"internal_network_name,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// Validate checks an opened envelope carries what restore depends on.
func (i *Identity) Validate() error {
	if i.Schema != IdentitySchema {
		return fmt.Errorf("identity envelope schema %d is not supported by this build (expected %d)", i.Schema, IdentitySchema)
	}
	if strings.TrimSpace(i.EncryptionKey) == "" {
		return errors.New("identity envelope carries no encryption key — a restore from it could not decrypt any secret")
	}
	if strings.TrimSpace(i.InstallID) == "" {
		return errors.New("identity envelope carries no install id")
	}
	return nil
}

// ValidatePassphrase rejects passphrases too weak for the envelope. This is the
// one artifact whose compromise hands over the whole platform, so the floor is
// enforced rather than suggested.
func ValidatePassphrase(pass string) error {
	if len([]rune(pass)) < minPassLen {
		return ErrWeakPassphrase
	}
	var hasLetter, hasOther bool
	for _, r := range pass {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r), unicode.IsPunct(r), unicode.IsSymbol(r):
			hasOther = true
		}
	}
	if !hasLetter || !hasOther {
		return ErrWeakPassphrase
	}
	return nil
}

// Seal encrypts the identity under a key derived from passphrase with Argon2id,
// returning the framed envelope. A fresh random salt and nonce are generated per
// call, so sealing the same identity twice never produces the same bytes.
func Seal(id *Identity, passphrase string) ([]byte, error) {
	if id == nil {
		return nil, errors.New("nil identity")
	}
	if err := ValidatePassphrase(passphrase); err != nil {
		return nil, err
	}
	id.Schema = IdentitySchema
	plaintext, err := json.Marshal(id)
	if err != nil {
		return nil, fmt.Errorf("marshal identity: %w", err)
	}

	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	key := deriveKey(passphrase, salt)
	defer zero(key)
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, headerLen+len(plaintext)+gcm.Overhead())
	out = append(out, magic...)
	out = append(out, byte(formatVer))
	out = append(out, salt...)
	out = append(out, nonce...)
	// The header is authenticated as additional data, so a file whose salt or
	// version was edited fails to open rather than decrypting to nonsense.
	out = gcm.Seal(out, nonce, plaintext, out[:headerLen])
	zero(plaintext)
	return out, nil
}

// Open decrypts a sealed envelope. A wrong passphrase and a tampered file are
// reported identically — the AEAD cannot tell them apart, and pretending
// otherwise would be a decryption oracle.
func Open(sealed []byte, passphrase string) (*Identity, error) {
	if len(sealed) < headerLen || string(sealed[:len(magic)]) != magic {
		return nil, ErrNotEnvelope
	}
	if sealed[len(magic)] != formatVer {
		return nil, fmt.Errorf("%w: unsupported envelope format version %d", ErrNotEnvelope, sealed[len(magic)])
	}
	off := len(magic) + 1
	salt := sealed[off : off+saltLen]
	nonce := sealed[off+saltLen : headerLen]
	ct := sealed[headerLen:]

	key := deriveKey(passphrase, salt)
	defer zero(key)
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, nonce, ct, sealed[:headerLen])
	if err != nil {
		return nil, ErrBadPassphrase
	}
	defer zero(plaintext)

	var id Identity
	if err := json.Unmarshal(plaintext, &id); err != nil {
		return nil, fmt.Errorf("decode identity: %w", err)
	}
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return &id, nil
}

func deriveKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonLanes, keyLen)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("identity cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("identity cipher: %w", err)
	}
	return gcm, nil
}

// zero wipes key material we are done with. Go's GC gives no guarantee the
// backing array is not copied, but leaving live keys in a long-running control
// plane's heap for no reason is worse.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
