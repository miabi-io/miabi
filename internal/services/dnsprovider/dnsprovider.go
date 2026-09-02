// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dnsprovider manages workspace DNS provider connections: connect, test, list, delete.
// Credentials are encrypted at rest and never returned, and connecting is gated by the AllowDNSProviders
// plan capability. It also resolves a stored connection into a usable dns.Provider for automation.
package dnsprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/dns"
	"github.com/miabi-io/miabi/internal/dnscatalog"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	// ErrNotFound is returned when a provider does not exist in the workspace.
	ErrNotFound = errors.New("dns provider not found")
	// ErrNameRequired / ErrNameTaken / ErrInvalidType are validation sentinels.
	ErrNameRequired = errors.New("name is required")
	ErrNameTaken    = errors.New("a DNS provider with that name already exists")
	ErrInvalidType  = errors.New("unknown DNS provider type")
	// ErrInvalidCredentials rejects a rotation whose credential cannot build a provider.
	ErrInvalidCredentials = errors.New("the credentials are not valid for this provider type")
)

// Service manages DNS provider connections and the records Miabi creates through
// them (the managed-record ledger).
type Service struct {
	repo    *repositories.DNSProviderRepository
	records *repositories.DNSRecordRepository
	domains *repositories.DomainRepository
	quota   *quota.Service
}

func NewService(repo *repositories.DNSProviderRepository, records *repositories.DNSRecordRepository, domains *repositories.DomainRepository) *Service {
	return &Service{repo: repo, records: records, domains: domains}
}

// SetQuota wires the plan/quota enforcer (nil-safe; nil skips checks).
func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

// ConnectInput is the payload to connect (or replace) a provider. TestZone, when
// set, is a zone (a domain on the provider) validated before the credential is
// stored — so an invalid token fails without persisting.
type ConnectInput struct {
	// Name is the desired unique slug handle; it is normalized to canonical slug
	// form. DisplayName is the free-text label (falls back to Name when blank).
	Name        string
	DisplayName string
	Type        string
	Credentials dns.Credentials
	TestZone    string
}

func (in *ConnectInput) normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Type = strings.ToLower(strings.TrimSpace(in.Type))
	in.TestZone = strings.TrimSpace(in.TestZone)
}

// Connect validates and stores a new DNS provider connection for the workspace.
func (s *Service) Connect(ctx context.Context, workspaceID uint, in ConnectInput) (*models.DNSProvider, error) {
	in.normalize()
	name := slug.Make(in.Name, "")
	if name == "" {
		return nil, ErrNameRequired
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(in.Name)
	}
	if _, ok := dnscatalog.Get(in.Type); !ok {
		return nil, ErrInvalidType
	}
	if s.quota.Enabled() {
		if err := s.quota.Require(workspaceID, quota.CapDNSProviders); err != nil {
			return nil, err
		}
	}
	if taken, _ := s.repo.ExistsByName(workspaceID, name); taken {
		return nil, ErrNameTaken
	}
	// Build the provider and (optionally) validate the credential against a zone
	// before persisting, so a bad token never gets stored.
	prov, err := dns.Build(in.Type, in.Credentials)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidType, err)
	}
	if in.TestZone != "" {
		if err := prov.Test(ctx, in.TestZone); err != nil {
			return nil, fmt.Errorf("connection test failed: %w", err)
		}
	}
	enc, err := encryptCreds(workspaceID, in.Credentials)
	if err != nil {
		return nil, err
	}
	p := &models.DNSProvider{
		WorkspaceID: workspaceID, Name: name, DisplayName: displayName, Type: in.Type,
		CredentialsEnc: enc, Status: models.DNSProviderStatusOK,
	}
	if err := s.repo.Create(p); err != nil {
		return nil, err
	}
	return p, nil
}

// UpdateInput rotates a provider's credentials and/or renames it. Type is immutable.
type UpdateInput struct {
	DisplayName *string
	Credentials dns.Credentials
	TestZone    string
}

// Update rotates credentials and/or renames a provider. A credential that fails to build, or
// fails TestZone, leaves the stored one untouched.
func (s *Service) Update(ctx context.Context, workspaceID, id uint, in UpdateInput) (*models.DNSProvider, error) {
	p, err := s.get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	if len(in.Credentials) > 0 {
		prov, berr := dns.Build(p.Type, in.Credentials)
		if berr != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidCredentials, berr)
		}
		if zone := strings.TrimSpace(in.TestZone); zone != "" {
			if terr := prov.Test(ctx, zone); terr != nil {
				return nil, fmt.Errorf("connection test failed: %w", terr)
			}
		}
		enc, eerr := encryptCreds(workspaceID, in.Credentials)
		if eerr != nil {
			return nil, eerr
		}
		p.CredentialsEnc = enc
		p.Status, p.LastError = models.DNSProviderStatusOK, ""
	}
	if in.DisplayName != nil {
		if name := strings.TrimSpace(*in.DisplayName); name != "" {
			p.DisplayName = name
		}
	}
	if err := s.repo.Update(p); err != nil {
		return nil, err
	}
	return p, nil
}

// PublicCredentials returns the non-secret credential fields for display.
func (s *Service) PublicCredentials(p *models.DNSProvider) map[string]string {
	d, ok := dnscatalog.Get(p.Type)
	if !ok {
		return map[string]string{}
	}
	creds, err := decryptCreds(p.CredentialsEnc)
	if err != nil {
		return map[string]string{}
	}
	return d.Public(creds)
}

// Test re-validates a stored provider against a zone and records the result on
// its Status / LastError.
func (s *Service) Test(ctx context.Context, workspaceID, id uint, zone string) (*models.DNSProvider, error) {
	p, err := s.get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	prov, err := s.provider(p)
	if err != nil {
		return nil, err
	}
	if zone = strings.TrimSpace(zone); zone == "" {
		return nil, fmt.Errorf("a zone (one of your domains) is required to test the connection")
	}
	if err := prov.Test(ctx, zone); err != nil {
		p.Status = models.DNSProviderStatusError
		p.LastError = err.Error()
		_ = s.repo.Update(p)
		return p, fmt.Errorf("connection test failed: %w", err)
	}
	p.Status = models.DNSProviderStatusOK
	p.LastError = ""
	if err := s.repo.Update(p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) List(workspaceID uint) ([]models.DNSProvider, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

func (s *Service) Get(workspaceID, id uint) (*models.DNSProvider, error) {
	return s.get(workspaceID, id)
}

func (s *Service) Delete(workspaceID, id uint) error {
	p, err := s.get(workspaceID, id)
	if err != nil {
		return err
	}
	return s.repo.Delete(p.ID)
}

// Provider resolves a stored connection into a usable dns.Provider (decrypting
// the credential). Used by the automation paths and the cert engine.
func (s *Service) Provider(workspaceID, id uint) (dns.Provider, error) {
	p, err := s.get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	return s.provider(p)
}

func (s *Service) get(workspaceID, id uint) (*models.DNSProvider, error) {
	p, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return p, nil
}

func (s *Service) provider(p *models.DNSProvider) (dns.Provider, error) {
	creds, err := decryptCreds(p.CredentialsEnc)
	if err != nil {
		return nil, err
	}
	return dns.Build(p.Type, creds)
}

func encryptCreds(workspaceID uint, c dns.Credentials) (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return crypto.EncryptWS(workspaceID, string(raw))
}

func decryptCreds(enc string) (dns.Credentials, error) {
	var c dns.Credentials
	raw, err := crypto.Decrypt(enc)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, err
	}
	return c, nil
}
