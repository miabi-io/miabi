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

// Package auth handles password hashing, revocable JWT sessions and password resets.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

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
	// ErrServiceAccountSession refuses a console session to a service account: it uses API keys only.
	ErrServiceAccountSession = errors.New("service accounts cannot sign in")

	ErrTwoFactorAlreadyEnabled = errors.New("two-factor authentication is already enabled")
	ErrTwoFactorNotEnabled     = errors.New("two-factor authentication is not enabled")
	ErrTwoFactorNotInitiated   = errors.New("two-factor setup has not been initiated")
	ErrInvalidTwoFactorCode    = errors.New("invalid two-factor code")
)

type Service struct {
	users  *repositories.UserRepository
	resets *repositories.PasswordResetRepository
	// Optional: unset makes the verification methods return an error rather than panic.
	verifications *repositories.EmailVerificationRepository
	recovery      *repositories.TwoFactorRecoveryRepository
	store         *session.Store
	jwtKey        []byte
	aud           string
	totpIssuer    func() string
}

// SetTOTPIssuer wires the name authenticator apps show for this instance. Unset shows "Miabi".
func (s *Service) SetTOTPIssuer(fn func() string) { s.totpIssuer = fn }

func (s *Service) issuer() string {
	if s.totpIssuer != nil {
		if v := s.totpIssuer(); v != "" {
			return v
		}
	}
	return twofactor.DefaultIssuer
}

// SetEmailVerifications wires the verification token store (nil-safe).
func (s *Service) SetEmailVerifications(r *repositories.EmailVerificationRepository) {
	s.verifications = r
}

func NewService(users *repositories.UserRepository, resets *repositories.PasswordResetRepository, recovery *repositories.TwoFactorRecoveryRepository, store *session.Store, jwtSecret string) *Service {
	return &Service{users: users, resets: resets, recovery: recovery, store: store, jwtKey: []byte(jwtSecret), aud: "miabi"}
}

// A miss on the primary lookup falls back to the other so an unusual username still resolves.
func (s *Service) Authenticate(identifier, password string) (*models.User, error) {
	if utf8.RuneCountInString(password) < models.MinLoginPasswordLength {
		return nil, ErrInvalidCredentials
	}
	user, err := s.lookupByIdentifier(identifier)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	if user.IsService() {
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

// Service accounts are refused here, the one place every console session is minted.
func (s *Service) IssueToken(user *models.User) (token, jti string, err error) {
	if user.IsService() {
		return "", "", ErrServiceAccountSession
	}
	jti = uuid.NewString()
	token, err = okapi.GenerateJwtToken(s.jwtKey, jwt.MapClaims{
		"sub":                    user.ID,
		"email":                  user.Email,
		"role":                   string(user.Role),
		"aud":                    s.aud,
		"jti":                    jti,
		ClaimPasswordFingerprint: SessionFingerprint(s.jwtKey, user.PasswordHash),
	}, TokenTTL)
	return token, jti, err
}

// ClaimPasswordFingerprint is the session claim binding a token to the password it was issued under.
const ClaimPasswordFingerprint = "pwf"

// SessionFingerprint changes on any password change, ending every older session.
func SessionFingerprint(key []byte, passwordHash string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("miabi-session:" + passwordHash))
	return hex.EncodeToString(m.Sum(nil))[:16]
}

// Revoke blacklists a session by its jti until its natural expiry.
func (s *Service) Revoke(ctx context.Context, jti string) {
	s.store.MarkRevoked(ctx, jti, time.Now().Add(TokenTTL))
}

// CreatePasswordReset never reveals whether the email exists.
func (s *Service) CreatePasswordReset(email string) (rawToken string, user *models.User, err error) {
	user, err = s.users.FindByEmail(email)
	if err != nil || user.IsService() {
		return "", nil, nil // do not leak existence
	}
	raw, hash := generateToken()
	rec := &models.PasswordResetToken{UserID: user.ID, TokenHash: hash, ExpiresAt: time.Now().Add(PasswordResetTTL)}
	if err := s.resets.Create(rec); err != nil {
		return "", nil, err
	}
	return raw, user, nil
}

// Longer than a reset: mail may sit unread overnight, and the link only proves inbox access.
const EmailVerificationTTL = 48 * time.Hour

// Retires any outstanding link so a re-send leaves exactly one live.
func (s *Service) CreateEmailVerification(user *models.User) (rawToken string, err error) {
	if s.verifications == nil {
		return "", errors.New("email verification is not available")
	}
	if err := s.verifications.InvalidateForUser(user.ID); err != nil {
		return "", err
	}
	raw, hash := generateToken()
	rec := &models.EmailVerificationToken{UserID: user.ID, TokenHash: hash, ExpiresAt: time.Now().Add(EmailVerificationTTL)}
	if err := s.verifications.Create(rec); err != nil {
		return "", err
	}
	return raw, nil
}

func (s *Service) ConfirmEmailVerification(rawToken string) (*models.User, error) {
	if s.verifications == nil {
		return nil, errors.New("email verification is not available")
	}
	rec, err := s.verifications.FindValidByHash(hashToken(rawToken))
	if err != nil {
		return nil, ErrInvalidToken
	}
	user, err := s.users.FindByID(rec.UserID)
	if err != nil {
		return nil, ErrInvalidToken
	}
	// Spend the token first so it can't be reused if the update fails and the caller retries.
	if err := s.verifications.MarkUsed(rec.ID); err != nil {
		return nil, err
	}
	if user.EmailVerifiedAt == nil {
		now := time.Now()
		user.EmailVerifiedAt = &now
		if err := s.users.Update(user); err != nil {
			return nil, err
		}
	}
	return user, nil
}

// ResetPassword consumes a reset token, sets a new password and returns the user's id.
func (s *Service) ResetPassword(rawToken, newPassword string) (uint, error) {
	if err := models.CheckNewPassword(newPassword); err != nil {
		return 0, err
	}
	rec, err := s.resets.FindValidByHash(hashToken(rawToken))
	if err != nil {
		return 0, ErrInvalidToken
	}
	user, err := s.users.FindByID(rec.UserID)
	if err != nil {
		return 0, ErrInvalidToken
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	user.PasswordHash = string(hash)
	user.MustChangePassword = false // the user set their own password
	if err := s.users.Update(user); err != nil {
		return 0, err
	}
	return user.ID, s.resets.MarkUsed(rec.ID)
}

func (s *Service) ChangePassword(userID uint, currentPassword, newPassword string) error {
	if err := models.CheckNewPassword(newPassword); err != nil {
		return err
	}
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

// A user with an admin-set password gets this instead of a full session until they set their own.
func (s *Service) CreateResetSession(ctx context.Context, userID uint) (string, error) {
	return s.store.CreateResetSession(ctx, userID)
}

func (s *Service) CompletePasswordReset(ctx context.Context, token, newPassword string) (*models.User, error) {
	// Checked before the token is consumed: a rejected password must not burn the single-use session.
	if err := models.CheckNewPassword(newPassword); err != nil {
		return nil, err
	}
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

// Calling it again before confirming rotates the pending secret.
func (s *Service) BeginTwoFactorSetup(user *models.User) (secret, url string, err error) {
	if user.TwoFactorEnabled {
		return "", "", ErrTwoFactorAlreadyEnabled
	}
	secret, url, err = twofactor.Generate(s.issuer(), user.Email)
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

func (s *Service) VerifyLoginCode(user *models.User, code string) bool {
	if s.validateTOTP(user, code) {
		return true
	}
	return s.consumeRecoveryCode(user, code)
}

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

func generateRecoveryCode() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b) // 10 hex chars
	return h[:5] + "-" + h[5:]
}

func normalizeRecoveryCode(code string) string {
	r := strings.NewReplacer("-", "", " ", "")
	return strings.ToLower(strings.TrimSpace(r.Replace(code)))
}

func (s *Service) VerifySecondFactor(user *models.User, code string) error {
	if !user.TwoFactorEnabled {
		return ErrTwoFactorNotEnabled
	}
	if s.validateTOTP(user, code) || s.consumeRecoveryCode(user, code) {
		return nil
	}
	return ErrInvalidTwoFactorCode
}
