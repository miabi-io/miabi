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

package billing

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/dnsprovider"
	"gorm.io/gorm"
)

// ConsoleDNSAPIPath is where the console serves its PowerDNS-compatible API, below its URL.
const ConsoleDNSAPIPath = "/dns/api/v1"

// maxDNSZones bounds a push; a customer with more zones is not a shared-hosting customer.
const maxDNSZones = 500

var (
	ErrDNSUnavailable   = coded(http.StatusServiceUnavailable, "BILLING_DNS_UNAVAILABLE", "DNS providers are unavailable on this instance")
	ErrDNSNotConfigured = coded(http.StatusNotFound, "BILLING_DNS_NOT_CONFIGURED", "no console-managed DNS provider in this workspace")
	ErrDNSNameTaken     = coded(http.StatusConflict, "DNS_PROVIDER_NAME_TAKEN",
		"a DNS provider made in Miabi is already named "+dnsprovider.ConsoleProviderName+" in this workspace")
	ErrDNSToken = coded(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "api_token is required for the workspace's first push")
	ErrDNSZones = coded(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "zones must be at most 500 lower-case domain names")
)

// ConsoleDNS stores the DNS providers the console pushes. Implemented by the dnsprovider service.
type ConsoleDNS interface {
	UpsertConsole(ctx context.Context, workspaceID uint, in dnsprovider.ConsoleInput) (*models.DNSProvider, error)
	ConsoleProvider(workspaceID uint) (*models.DNSProvider, error)
	DeleteConsole(workspaceID uint) (bool, error)
	DeleteAllConsole() error
}

// DomainConnector connects a workspace's domains to a provider whose zones cover them. Implemented by
// the domain service.
type DomainConnector interface {
	ConnectToProvider(workspaceID uint, p *models.DNSProvider) (int, error)
}

// DNSProviderPush is PUT /billing/accounts/{id}/workspaces/{ws}/dns-provider.
type DNSProviderPush struct {
	// APIToken is the console DNS token; it may be omitted after the first push to change only the zones.
	APIToken string   `json:"api_token"`
	Zones    []string `json:"zones"`
}

// DNSProviderView is a workspace's console-managed DNS provider as the Billing API reports it.
type DNSProviderView struct {
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Zones     []string  `json:"zones"`
	Status    string    `json:"status"`
	LastError string    `json:"last_error"`
	Domains   int64     `json:"domains"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SetConsoleDNS wires the provider store and the domain connector for §6 of the contract.
func (s *Service) SetConsoleDNS(dns ConsoleDNS, domains DomainConnector) {
	s.dns, s.dnsDomains = dns, domains
}

var zoneRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// normalizeZones lower-cases, trims and de-duplicates zone names, refusing anything that is not one.
func normalizeZones(in []string) ([]string, error) {
	if len(in) > maxDNSZones {
		return nil, ErrDNSZones
	}
	out := make([]string, 0, len(in))
	for _, z := range in {
		z = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(z)), ".")
		if !zoneRe.MatchString(z) {
			return nil, ErrDNSZones
		}
		if !slices.Contains(out, z) {
			out = append(out, z)
		}
	}
	slices.Sort(out)
	return out, nil
}

// PushDNSProvider creates or updates the workspace's console-managed DNS provider, then connects the
// workspace's domains under its zones and verifies them.
func (s *Service) PushDNSProvider(ctx context.Context, externalID, ref string, in DNSProviderPush) (*DNSProviderView, error) {
	if s.dns == nil {
		return nil, ErrDNSUnavailable
	}
	zones, err := normalizeZones(in.Zones)
	if err != nil {
		return nil, err
	}
	conn, err := s.Connection()
	if err != nil {
		return nil, err
	}
	if !conn.Connected {
		return nil, ErrNotConnected
	}
	ws, err := s.billedWorkspace(externalID, ref)
	if err != nil {
		return nil, err
	}
	p, err := s.dns.UpsertConsole(ctx, ws.ID, dnsprovider.ConsoleInput{
		ServerURL: strings.TrimRight(conn.ConsoleURL, "/") + ConsoleDNSAPIPath, APIToken: in.APIToken, Zones: zones,
	})
	switch {
	case errors.Is(err, dnsprovider.ErrConsoleNameTaken):
		return nil, ErrDNSNameTaken
	case errors.Is(err, dnsprovider.ErrInvalidCredentials):
		return nil, ErrDNSToken
	case err != nil:
		return nil, err
	}
	if s.dnsDomains != nil {
		if _, err := s.dnsDomains.ConnectToProvider(ws.ID, p); err != nil {
			return nil, err
		}
	}
	return s.dnsView(p)
}

// DNSProvider reports the workspace's console-managed DNS provider.
func (s *Service) DNSProvider(externalID, ref string) (*DNSProviderView, error) {
	if s.dns == nil {
		return nil, ErrDNSUnavailable
	}
	ws, err := s.billedWorkspace(externalID, ref)
	if err != nil {
		return nil, err
	}
	p, err := s.dns.ConsoleProvider(ws.ID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrDNSNotConfigured
	}
	return s.dnsView(p)
}

// DeleteDNSProvider removes the workspace's console-managed DNS provider; its domains fall back to
// manual DNS.
func (s *Service) DeleteDNSProvider(externalID, ref string) error {
	if s.dns == nil {
		return ErrDNSUnavailable
	}
	ws, err := s.billedWorkspace(externalID, ref)
	if err != nil {
		return err
	}
	deleted, err := s.dns.DeleteConsole(ws.ID)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrDNSNotConfigured
	}
	return nil
}

func (s *Service) billedWorkspace(externalID, ref string) (*models.Workspace, error) {
	var ws *models.Workspace
	err := s.db.Transaction(func(tx *gorm.DB) error {
		a, err := lockAccount(tx, externalID)
		if err != nil {
			return err
		}
		ws, err = accountWorkspace(tx, a, ref)
		return err
	})
	return ws, err
}

func (s *Service) dnsView(p *models.DNSProvider) (*DNSProviderView, error) {
	var n int64
	if err := s.db.Model(&models.Domain{}).Where("dns_provider_id = ?", p.ID).Count(&n).Error; err != nil {
		return nil, err
	}
	zones := p.Zones
	if zones == nil {
		zones = []string{}
	}
	return &DNSProviderView{Name: p.Name, Type: p.Type, Zones: zones, Status: p.Status, LastError: p.LastError,
		Domains: n, UpdatedAt: p.UpdatedAt}, nil
}
