// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package auth handles user authentication: password hashing, JWT issuance with
// revocable sessions, and password resets.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/session"
	"github.com/miabi-io/miabi/internal/services/twofactor"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"golang.org/x/crypto/bcrypt"
)

// totpIssuer labels the account in authenticator apps (the QR code label).
const totpIssuer = "Miabi"

// recoveryCodeCount is the number of backup codes issued when 2FA is enabled.
const recoveryCodeCount = 10

// TokenTTL is the lifetime of an issued access token (and its session).
const TokenTTL = 24 * time.Hour

// PasswordResetTTL is the lifetime of a password-reset token.
const PasswordResetTTL = time.Hour

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrEmailTaken         = errors.New("email already registered")
	ErrAccountDisabled    = errors.New("account is disabled")
	ErrInvalidToken       = errors.New("invalid or expired token")

	ErrTwoFactorAlreadyEnabled = errors.New("two-factor authentication is already enabled")
	ErrTwoFactorNotEnabled     = errors.New("two-factor authentication is not enabled")
	ErrTwoFactorNotInitiated   = errors.New("two-factor setup has not been initiated")
	ErrInvalidTwoFactorCode    = errors.New("invalid two-factor code")
)

type Service struct {
	users    *repositories.UserRepository
	resets   *repositories.PasswordResetRepository
	recovery *repositories.TwoFactorRecoveryRepository
	store    *session.Store
	jwtKey   []byte
	aud      string
}

func NewService(users *repositories.UserRepository, resets *repositories.PasswordResetRepository, recovery *repositories.TwoFactorRecoveryRepository, store *session.Store, jwtSecret string) *Service {
	return &Service{users: users, resets: resets, recovery: recovery, store: store, jwtKey: []byte(jwtSecret), aud: "miabi"}
}

// Authenticate verifies credentials and returns the user. The identifier is
// either an email address or a username handle — an '@' selects the email
// lookup, otherwise the username; a miss on the primary lookup falls back to the
// other so a username that happens to look unusual still resolves.
func (s *Service) Authenticate(identifier, password string) (*models.User, error) {
	user, err := s.lookupByIdentifier(identifier)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}
	if !user.Active {
		return nil, ErrAccountDisabled
	}
	return user, nil
}

// lookupByIdentifier resolves a login identifier to a user by email or username.
func (s *Service) lookupByIdentifier(identifier string) (*models.User, error) {
	id := strings.TrimSpace(identifier)
	if strings.Contains(id, "@") {
		if u, err := s.users.FindByEmail(id); err == nil {
			return u, nil
		}
		return s.users.FindByUsername(id)
	}
	if u, err := s.users.FindByUsername(id); err == nil {
		return u, nil
	}
	return s.users.FindByEmail(id)
}

// IssueToken creates a signed JWT carrying a unique jti.
func (s *Service) IssueToken(user *models.User) (token, jti string, err error) {
	jti = uuid.NewString()
	token, err = okapi.GenerateJwtToken(s.jwtKey, jwt.MapClaims{
		"sub":   user.ID,
		"email": user.Email,
		"role":  string(user.Role),
		"aud":   s.aud,
		"jti":   jti,
	}, TokenTTL)
	return token, jti, err
}

// Revoke blacklists a session by its jti until its natural expiry.
func (s *Service) Revoke(ctx context.Context, jti string) {
	s.store.MarkRevoked(ctx, jti, time.Now().Add(TokenTTL))
}

// CreatePasswordReset issues a reset token for an email. It returns the raw
// token (to be emailed) and never reveals whether the email exists.
func (s *Service) CreatePasswordReset(email string) (rawToken string, user *models.User, err error) {
	user, err = s.users.FindByEmail(email)
	if err != nil {
		return "", nil, nil // do not leak existence
	}
	raw, hash := generateToken()
	rec := &models.PasswordResetToken{UserID: user.ID, TokenHash: hash, ExpiresAt: time.Now().Add(PasswordResetTTL)}
	if err := s.resets.Create(rec); err != nil {
		return "", nil, err
	}
	return raw, user, nil
}

// ResetPassword consumes a reset token and sets a new password.
func (s *Service) ResetPassword(rawToken, newPassword string) error {
	rec, err := s.resets.FindValidByHash(hashToken(rawToken))
	if err != nil {
		return ErrInvalidToken
	}
	user, err := s.users.FindByID(rec.UserID)
	if err != nil {
		return ErrInvalidToken
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	user.PasswordHash = string(hash)
	user.MustChangePassword = false // the user set their own password
	if err := s.users.Update(user); err != nil {
		return err
	}
	return s.resets.MarkUsed(rec.ID)
}

// ChangePassword verifies an authenticated user's current password and sets a
// new one. Distinct from the token-based ResetPassword: this is the self-service
// path from the security page, gated by the current password rather than an
// emailed token.
func (s *Service) ChangePassword(userID uint, currentPassword, newPassword string) error {
	user, err := s.users.FindByID(userID)
	if err != nil {
		return ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)); err != nil {
		return ErrInvalidCredentials
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	user.PasswordHash = string(hash)
	user.MustChangePassword = false // the user set their own password
	return s.users.Update(user)
}

// CreateResetSession issues a short-lived, single-use reset-session token (Redis)
// for the forced-password-change flow: a user with an admin-set/reset password
// gets this instead of a full session at login, and exchanges it for a real
// session once they set their own password.
func (s *Service) CreateResetSession(ctx context.Context, userID uint) (string, error) {
	return s.store.CreateResetSession(ctx, userID)
}

// CompletePasswordReset consumes a reset-session token, sets the user's new
// password, clears the must-change flag, and returns the user so the caller can
// issue a full session. Errors when the token is invalid, expired, or already used.
func (s *Service) CompletePasswordReset(ctx context.Context, token, newPassword string) (*models.User, error) {
	userID, err := s.store.ConsumeResetSession(ctx, token)
	if err != nil {
		return nil, err
	}
	user, err := s.users.FindByID(userID)
	if err != nil {
		return nil, session.ErrResetSession
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	user.PasswordHash = string(hash)
	user.MustChangePassword = false
	if err := s.users.Update(user); err != nil {
		return nil, err
	}
	return user, nil
}

// generateToken returns a random URL-safe token and its sha256 hex hash.
func generateToken() (raw, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	raw = hex.EncodeToString(b)
	return raw, hashToken(raw)
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// --- Two-factor authentication (TOTP) ---

// BeginTwoFactorSetup generates a fresh TOTP secret for the user, stores it
// encrypted (not yet enabled), and returns the secret plus an otpauth:// URL the
// client renders as a QR code. Calling it again before confirming rotates the
// pending secret.
func (s *Service) BeginTwoFactorSetup(user *models.User) (secret, url string, err error) {
	if user.TwoFactorEnabled {
		return "", "", ErrTwoFactorAlreadyEnabled
	}
	secret, url, err = twofactor.Generate(totpIssuer, user.Email)
	if err != nil {
		return "", "", err
	}
	enc, err := crypto.Encrypt(secret)
	if err != nil {
		return "", "", err
	}
	user.TwoFactorSecret = enc
	if err := s.users.Update(user); err != nil {
		return "", "", err
	}
	return secret, url, nil
}

// ConfirmTwoFactor validates a code against the pending secret and, on success,
// activates two-factor authentication and issues a fresh set of single-use
// recovery codes (returned in plaintext, shown to the user once).
func (s *Service) ConfirmTwoFactor(user *models.User, code string) (recoveryCodes []string, err error) {
	if user.TwoFactorEnabled {
		return nil, ErrTwoFactorAlreadyEnabled
	}
	if user.TwoFactorSecret == "" {
		return nil, ErrTwoFactorNotInitiated
	}
	if !s.validateTOTP(user, code) {
		return nil, ErrInvalidTwoFactorCode
	}
	user.TwoFactorEnabled = true
	if err := s.users.Update(user); err != nil {
		return nil, err
	}
	return s.regenerateRecoveryCodes(user.ID)
}

// DisableTwoFactor turns off two-factor authentication after verifying a code
// (a TOTP code or a recovery code), clearing the stored secret and any codes.
func (s *Service) DisableTwoFactor(user *models.User, code string) error {
	if !user.TwoFactorEnabled {
		return ErrTwoFactorNotEnabled
	}
	if !s.validateTOTP(user, code) && !s.consumeRecoveryCode(user, code) {
		return ErrInvalidTwoFactorCode
	}
	user.TwoFactorEnabled = false
	user.TwoFactorSecret = ""
	if err := s.users.Update(user); err != nil {
		return err
	}
	return s.recovery.DeleteForUser(user.ID)
}

// RegenerateRecoveryCodes verifies a current TOTP code and replaces the user's
// recovery codes with a fresh set, returned in plaintext.
func (s *Service) RegenerateRecoveryCodes(user *models.User, code string) ([]string, error) {
	if !user.TwoFactorEnabled {
		return nil, ErrTwoFactorNotEnabled
	}
	if !s.validateTOTP(user, code) {
		return nil, ErrInvalidTwoFactorCode
	}
	return s.regenerateRecoveryCodes(user.ID)
}

// RecoveryCodesRemaining reports how many unused recovery codes the user has.
func (s *Service) RecoveryCodesRemaining(userID uint) int {
	n, err := s.recovery.CountUnused(userID)
	if err != nil {
		return 0
	}
	return int(n)
}

// VerifyLoginCode checks a second-factor input at login: it tries the TOTP code
// first, then falls back to consuming a one-time recovery code.
func (s *Service) VerifyLoginCode(user *models.User, code string) bool {
	if s.validateTOTP(user, code) {
		return true
	}
	return s.consumeRecoveryCode(user, code)
}

// validateTOTP decrypts the stored secret and verifies the supplied code.
func (s *Service) validateTOTP(user *models.User, code string) bool {
	if user.TwoFactorSecret == "" {
		return false
	}
	secret, err := crypto.Decrypt(user.TwoFactorSecret)
	if err != nil {
		return false
	}
	return twofactor.Validate(secret, code)
}

// consumeRecoveryCode validates and marks a recovery code as used. It returns
// true only when an unused matching code was found and consumed.
func (s *Service) consumeRecoveryCode(user *models.User, code string) bool {
	norm := normalizeRecoveryCode(code)
	if norm == "" {
		return false
	}
	rec, err := s.recovery.FindUnusedByHash(user.ID, hashToken(norm))
	if err != nil {
		return false
	}
	return s.recovery.MarkUsed(rec.ID) == nil
}

// regenerateRecoveryCodes creates a fresh set of codes, persists their hashes,
// and returns the plaintext codes.
func (s *Service) regenerateRecoveryCodes(userID uint) ([]string, error) {
	plain := make([]string, 0, recoveryCodeCount)
	records := make([]*models.TwoFactorRecoveryCode, 0, recoveryCodeCount)
	for i := 0; i < recoveryCodeCount; i++ {
		c := generateRecoveryCode()
		plain = append(plain, c)
		records = append(records, &models.TwoFactorRecoveryCode{
			UserID:   userID,
			CodeHash: hashToken(normalizeRecoveryCode(c)),
		})
	}
	if err := s.recovery.ReplaceForUser(userID, records); err != nil {
		return nil, err
	}
	return plain, nil
}

// generateRecoveryCode returns a formatted backup code like "a1b2c-3d4e5".
func generateRecoveryCode() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b) // 10 hex chars
	return h[:5] + "-" + h[5:]
}

// normalizeRecoveryCode strips formatting so stored hashes match user input
// regardless of dashes, spaces, or case.
func normalizeRecoveryCode(code string) string {
	r := strings.NewReplacer("-", "", " ", "")
	return strings.ToLower(strings.TrimSpace(r.Replace(code)))
}
