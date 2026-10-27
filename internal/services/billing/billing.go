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

// Package billing serves the Billing API (/api/v1/billing): an external billing platform, such
// as the Miabi Console, drives plans, quotas and restrictions with a billing-only key. The API is
// off unless MIABI_BILLING_API_ENABLED is set and the licence grants the billing flag
// (plans/billing-api-v1.md).
package billing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// ServiceUserEmail is the platform billing service user: it owns the billing keys and is the
// actor of billing audit entries, so offboarding an admin never breaks billing.
const ServiceUserEmail = "billing@" + models.ServiceAccountEmailDomain

// Service holds what the Billing API needs.
type Service struct {
	db       *gorm.DB
	users    *repositories.UserRepository
	keys     *auth.APIKeyService
	plans    *repositories.PlanRepository
	settings *repositories.SettingRepository
	info     EnrolmentInfo
	deps     AccountDeps
	userID   atomic.Uint64
	// http calls the console; nil uses a client that never follows redirects.
	http *http.Client
	now  func() time.Time
	// locked is MIABI_BILLING_CONNECTION_LOCKED.
	locked bool

	ssoRepo    *repositories.OAuthProviderRepository
	ssoResolve func(ctx context.Context, p *models.OAuthProvider) error
	ssoAPIBase string

	consoleOnly func() bool

	dns        ConsoleDNS
	dnsDomains DomainConnector
}

// SetLocked applies MIABI_BILLING_CONNECTION_LOCKED.
func (s *Service) SetLocked(locked bool) { s.locked = locked }

// Locked reports whether the console connection is locked.
func (s *Service) Locked() bool { return s.locked }

// GuardServiceUser refuses to suspend, deactivate or delete the billing service user while the
// connection is locked: its key is the connection, so losing the user is a disconnect.
func (s *Service) GuardServiceUser(u *models.User) error {
	if s.locked && u != nil && strings.EqualFold(u.Email, ServiceUserEmail) {
		return ErrConnectionLocked
	}
	return nil
}

// NewService builds the service.
func NewService(db *gorm.DB, users *repositories.UserRepository, keys *auth.APIKeyService, plans *repositories.PlanRepository,
	settings *repositories.SettingRepository, info EnrolmentInfo) *Service {
	return &Service{db: db, users: users, keys: keys, plans: plans, settings: settings, info: info, now: func() time.Time { return time.Now().UTC() }}
}

// EnsureServiceUser returns the billing service user, creating it on first use. It cannot sign
// in: its password is random and unknown, and it is a service account.
func (s *Service) EnsureServiceUser() (*models.User, error) {
	if u, err := s.users.FindByEmail(ServiceUserEmail); err == nil {
		return u, nil
	}
	hash, err := unusablePassword()
	if err != nil {
		return nil, err
	}
	u := &models.User{Name: "Billing", Username: "billing-service", Email: ServiceUserEmail, PasswordHash: hash,
		Role: models.SystemRoleUser, Kind: models.UserKindService, Active: true}
	if err := s.users.Create(u); err != nil {
		// Another replica created it first.
		if existing, ferr := s.users.FindByEmail(ServiceUserEmail); ferr == nil {
			return existing, nil
		}
		return nil, err
	}
	return u, nil
}

// MintKey creates a billing key for the service user; the plaintext is shown once.
func (s *Service) MintKey(name string, allowedIPs []string) (string, *models.APIKey, error) {
	u, err := s.EnsureServiceUser()
	if err != nil {
		return "", nil, err
	}
	return s.keys.CreateBilling(u.ID, name, allowedIPs)
}

// Plan is a Miabi plan as the Billing API reports it (contract "Plan").
type Plan struct {
	Name string `json:"name"`
	// Key is the console's key for a managed plan; empty for a plan made in Miabi.
	Key          string           `json:"key,omitempty"`
	Managed      bool             `json:"managed"`
	Description  string           `json:"description"`
	IsActive     bool             `json:"is_active"`
	IsDefault    bool             `json:"is_default"`
	Limits       map[string]int64 `json:"limits"`
	Capabilities map[string]bool  `json:"capabilities"`
	UpdatedAt    time.Time        `json:"updated_at"`
	LimitsHash   string           `json:"limits_hash"`
}

// Plans lists the plans a billing platform may map its packages to. System plans, which only
// the platform's own workspace uses, are left out.
func (s *Service) Plans() ([]Plan, error) {
	all, _, err := s.plans.ListPaged("", 1000, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Plan, 0, len(all))
	for i := range all {
		if all[i].System {
			continue
		}
		p, err := ToPlan(&all[i])
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ToPlan reads a plan's numeric max_* limits and allow_* capabilities from its JSON form, so a
// new plan field is reported without touching this code.
func ToPlan(p *models.Plan) (Plan, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return Plan{}, err
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Plan{}, err
	}
	out := Plan{Name: p.Name, Description: p.Description, IsActive: p.IsActive, IsDefault: p.IsDefault, UpdatedAt: p.UpdatedAt,
		Managed: p.ConsoleManaged(), Limits: map[string]int64{}, Capabilities: map[string]bool{}}
	if p.ExternalKey != nil {
		out.Key = *p.ExternalKey
	}
	for k, v := range fields {
		switch {
		case strings.HasPrefix(k, "max_"):
			if n, ok := v.(float64); ok {
				out.Limits[k] = int64(n)
			}
		case strings.HasPrefix(k, "allow_"):
			if b, ok := v.(bool); ok {
				out.Capabilities[k] = b
			}
		}
	}
	// Maps marshal with sorted keys, so this JSON is canonical.
	canon, err := json.Marshal(map[string]any{"limits": out.Limits, "capabilities": out.Capabilities})
	if err != nil {
		return Plan{}, err
	}
	sum := sha256.Sum256(canon)
	out.LimitsHash = hex.EncodeToString(sum[:])
	return out, nil
}

// unusablePassword hashes a random secret nobody knows, so the account signs in only by SSO or
// after setting a password.
func unusablePassword() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword(secret, bcrypt.DefaultCost)
	return string(hash), err
}
