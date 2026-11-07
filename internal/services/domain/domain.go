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

// Package domain manages workspace-owned domains: registration, DNS-verified ownership, and default TLS policy.
package domain

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/declarative"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNotFound     = errors.New("domain not found")
	ErrNameTaken    = errors.New("this domain is already registered in the workspace")
	ErrNameRequired = errors.New("domain name is required")
	ErrInvalidName  = errors.New("invalid domain name")
	// ErrNameImmutable: a domain's name is fixed at creation; delete and re-create to change it.
	ErrNameImmutable = errors.New("the domain name cannot be changed; delete the domain and add it again")
	// ErrVerificationFailed means the expected TXT record was not found.
	ErrVerificationFailed = errors.New("DNS verification record not found")
	ErrProviderNotFound   = errors.New("DNS provider not found in this workspace")
	// ErrChallengeOnlyProvider rejects a DNS-01-only host (acme-dns) for record automation.
	ErrChallengeOnlyProvider = errors.New("this DNS provider only solves certificate challenges; it cannot manage a domain's records")
	ErrDomainBanned          = errors.New("this domain has been banned by a platform administrator")
	// ErrDomainClaimed: only one workspace may verify a domain platform-wide; registration stays open.
	ErrDomainClaimed = errors.New("another workspace has already verified this domain or a wildcard that covers it. " +
		"Ask your platform administrator if this workspace needs to serve it")
)

// The message stays generic because workspace users see it; only platform-admin handlers read Holder.
type ClaimedError struct{ Holder models.Domain }

func (e *ClaimedError) Error() string { return ErrDomainClaimed.Error() }
func (e *ClaimedError) Unwrap() error { return ErrDomainClaimed }

// A leading "*." is stripped before matching for wildcards.
var hostnameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// resolver is overridable in tests; defaults to the system resolver.
type lookupTXT func(ctx context.Context, host string) ([]string, error)

// DNSAutomator drives Miabi-managed records for provider-connected domains; nil means manual only.
type DNSAutomator interface {
	// ProviderExists reports whether a provider id belongs to the workspace.
	ProviderExists(workspaceID, providerID uint) bool
	// RecordsSupported reports whether the provider can own ordinary records.
	RecordsSupported(workspaceID, providerID uint) bool
	EnsureVerificationRecord(ctx context.Context, d *models.Domain) error
	// CleanupDomain removes the managed records for a deleted domain.
	CleanupDomain(ctx context.Context, d *models.Domain) error
}

// Bounded so Verify stays responsive while waiting for an auto-created TXT to propagate.
const (
	verifyRetries = 3
	verifyDelay   = 2 * time.Second
	// Consecutive misses tolerated before un-verifying, to absorb transient DNS failures.
	verifyMissThreshold = 3
)

// Privileged workspaces serve unverified domains, so the domain service reports that instead of "pending".
type WorkspacePolicy interface {
	IsPrivileged(workspaceID uint) (bool, error)
}

// ProxyResyncer re-renders a workspace's gateway so routes go live or offline when ownership changes.
type ProxyResyncer interface {
	SyncWorkspaceProxy(ctx context.Context, workspaceID uint) error
}

type ConsoleDNS interface {
	ConsoleProviderFor(workspaceID uint, name string) *uint
}

// Service manages domains.
type Service struct {
	repo       *repositories.DomainRepository
	lookup     lookupTXT
	automator  DNSAutomator
	resyncer   ProxyResyncer
	wsPolicy   WorkspacePolicy
	consoleDNS ConsoleDNS
}

func NewService(repo *repositories.DomainRepository) *Service {
	return &Service{repo: repo, lookup: authoritativeLookupTXT}
}

func (s *Service) SetDNSAutomator(a DNSAutomator) { s.automator = a }

func (s *Service) SetConsoleDNS(c ConsoleDNS) { s.consoleDNS = c }

func (s *Service) SetProxyResyncer(r ProxyResyncer) { s.resyncer = r }

func (s *Service) SetWorkspacePolicy(p WorkspacePolicy) { s.wsPolicy = p }

// A missing policy or lookup error reads as not privileged, matching the route service's fail-closed gate.
func (s *Service) privileged(workspaceID uint) bool {
	if s.wsPolicy == nil {
		return false
	}
	p, err := s.wsPolicy.IsPrivileged(workspaceID)
	if err != nil {
		logger.Warn("failed to load workspace privilege for domain view", "workspace", workspaceID, "err", err)
		return false
	}
	return p
}

// Privilege is a serving exemption, never a claim, so this must not verify anything.
func (s *Service) annotateServing(workspaceID uint, domains []models.Domain) {
	if len(domains) == 0 || !s.privileged(workspaceID) {
		return
	}
	for i := range domains {
		d := &domains[i]
		d.ServingUnverified = !d.Verified && !d.Banned
	}
}

// Best-effort: the periodic resync and the next route edit both recover from a failure.
func (s *Service) resync(ctx context.Context, workspaceID uint) {
	if s.resyncer == nil {
		return
	}
	if err := s.resyncer.SyncWorkspaceProxy(ctx, workspaceID); err != nil {
		logger.Warn("proxy resync after domain verification change failed", "workspace", workspaceID, "error", err)
	}
}

// Input is the create/update payload for a domain.
type Input struct {
	Name     string
	TLSMode  models.DomainTLSMode
	Wildcard bool
}

func (in *Input) normalize() {
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	in.Name = strings.TrimPrefix(in.Name, "*.")
	in.Name = strings.TrimSuffix(in.Name, ".")
	if in.TLSMode != models.DomainTLSCustom {
		in.TLSMode = models.DomainTLSACME
	}
}

func validName(name string) bool { return hostnameRe.MatchString(name) }

func (s *Service) Create(workspaceID uint, in Input) (*models.Domain, error) {
	in.normalize()
	if in.Name == "" {
		return nil, ErrNameRequired
	}
	if !validName(in.Name) {
		return nil, ErrInvalidName
	}
	if taken, _ := s.repo.ExistsByName(workspaceID, in.Name); taken {
		return nil, ErrNameTaken
	}
	d := &models.Domain{
		WorkspaceID: workspaceID, Name: in.Name, TLSMode: in.TLSMode, Wildcard: in.Wildcard,
		VerificationToken: declarative.RandAlphaNum(32),
	}
	if s.consoleDNS != nil && s.automator != nil {
		d.DNSProviderID = s.consoleDNS.ConsoleProviderFor(workspaceID, in.Name)
	}
	if err := s.repo.Create(d); err != nil {
		return nil, err
	}
	if d.DNSProviderID != nil {
		s.verifyInBackground(workspaceID, []uint{d.ID})
	}
	return d, nil
}

func (s *Service) ConnectToProvider(workspaceID uint, p *models.DNSProvider) (int, error) {
	domains, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}
	var pending []uint
	connected := 0
	for i := range domains {
		d := &domains[i]
		if d.DNSProviderID != nil || d.Banned || !p.CoversDomain(d.Name) {
			continue
		}
		d.DNSProviderID = &p.ID
		if err := s.repo.Update(d); err != nil {
			return connected, err
		}
		connected++
		if !d.Verified {
			pending = append(pending, d.ID)
		}
	}
	s.verifyInBackground(workspaceID, pending)
	return connected, nil
}

// Off the request path because each one waits for propagation.
func (s *Service) verifyInBackground(workspaceID uint, ids []uint) {
	if len(ids) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(ids))*30*time.Second)
		defer cancel()
		for _, id := range ids {
			if d, err := s.Verify(ctx, workspaceID, id); err != nil {
				name := ""
				if d != nil {
					name = d.Name
				}
				logger.Warn("domain: automatic verification failed", "workspace_id", workspaceID, "domain", name, "error", err)
			}
		}
	}()
}

func (s *Service) Update(workspaceID, id uint, in Input) (*models.Domain, error) {
	d, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	in.normalize()
	// The ownership proof, certificates and routes are keyed to the name, so renaming would silently invalidate them.
	if in.Name != "" && in.Name != d.Name {
		return nil, ErrNameImmutable
	}
	d.TLSMode = in.TLSMode
	d.Wildcard = in.Wildcard
	if err := s.repo.Update(d); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Service) Get(workspaceID, id uint) (*models.Domain, error) {
	d, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	one := []models.Domain{*d}
	s.annotateServing(workspaceID, one)
	return &one[0], nil
}

func (s *Service) List(workspaceID uint) ([]models.Domain, error) {
	out, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	s.annotateServing(workspaceID, out)
	return out, nil
}

func (s *Service) Delete(ctx context.Context, workspaceID, id uint) error {
	d, err := s.Get(workspaceID, id)
	if err != nil {
		return err
	}
	// Before the domain row, so a provider-created record never outlives its domain.
	if s.automator != nil {
		_ = s.automator.CleanupDomain(ctx, d)
	}
	return s.repo.Delete(workspaceID, id)
}

// A nil providerID reverts the domain to the manual copy-paste flow.
func (s *Service) SetDNSProvider(workspaceID, id uint, providerID *uint) (*models.Domain, error) {
	d, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	if providerID != nil {
		if s.automator == nil || !s.automator.ProviderExists(workspaceID, *providerID) {
			return nil, ErrProviderNotFound
		}
		if !s.automator.RecordsSupported(workspaceID, *providerID) {
			return nil, ErrChallengeOnlyProvider
		}
	}
	d.DNSProviderID = providerID
	if err := s.repo.Update(d); err != nil {
		return nil, err
	}
	return d, nil
}

// Verify creates the TXT itself for provider-connected domains and retries briefly to absorb propagation.
func (s *Service) Verify(ctx context.Context, workspaceID, id uint) (*models.Domain, error) {
	d, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	if d.Banned {
		return d, ErrDomainBanned
	}
	wasVerified := d.Verified
	automated := d.DNSProviderID != nil && s.automator != nil
	if automated {
		if err := s.automator.EnsureVerificationRecord(ctx, d); err != nil {
			return d, err // conflict / provider error surfaces to the caller
		}
	}
	want := d.ChallengeValue()
	attempts := 1
	if automated {
		attempts = verifyRetries // give the auto-created record time to propagate
	}
	verified := false
	for i := 0; i < attempts && !verified; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return d, ctx.Err()
			case <-time.After(verifyDelay):
			}
		}
		records, lerr := s.lookup(ctx, d.ChallengeHost())
		if lerr != nil {
			continue
		}
		for _, r := range records {
			if strings.TrimSpace(r) == want {
				verified = true
				break
			}
		}
	}
	if verified {
		if !wasVerified {
			if cerr := s.ensureClaimable(d); cerr != nil {
				return d, cerr
			}
		}
		now := time.Now()
		d.Verified = true
		d.VerifiedAt = &now
		d.VerificationCheckedAt = &now
		d.VerificationError = ""
		d.VerificationMisses = 0
		// A real proof supersedes any prior admin waiver.
		d.VerifiedVia = models.VerifiedViaDNS
		if automated {
			d.VerifiedVia = models.VerifiedViaDNSProvider
		}
		if err := s.repo.Update(d); err != nil {
			return nil, err
		}
		if !wasVerified {
			s.resync(ctx, workspaceID)
		}
		return d, nil
	}
	// Record the failed check so the UI can show when ownership last failed.
	now := time.Now()
	d.VerificationCheckedAt = &now
	d.VerificationError = ErrVerificationFailed.Error()
	if err := s.repo.Update(d); err != nil {
		return nil, err
	}
	return d, ErrVerificationFailed
}

func (s *Service) ensureClaimable(d *models.Domain) error {
	others, err := s.repo.ListVerifiedElsewhere(d.WorkspaceID)
	if err != nil {
		return err
	}
	if holder := claimHolder(d, others); holder != nil {
		return &ClaimedError{Holder: *holder}
	}
	return nil
}

// claimHolder returns the domain verified elsewhere that would serve the same hostname as d, if any.
func claimHolder(d *models.Domain, others []models.Domain) *models.Domain {
	for i := range others {
		if domainsOverlap(d, &others[i]) {
			return &others[i]
		}
	}
	return nil
}

// Names are stored normalized (lowercase, no "*." prefix, no trailing dot).
func domainsOverlap(a, b *models.Domain) bool {
	na, nb := strings.ToLower(a.Name), strings.ToLower(b.Name)
	if na == nb {
		return true
	}
	if a.Wildcard && strings.HasSuffix(nb, "."+na) {
		return true // a's *.na covers subdomain nb
	}
	if b.Wildcard && strings.HasSuffix(na, "."+nb) {
		return true // b's *.nb covers subdomain na
	}
	return false
}

// ForceVerify is a platform-admin override that bypasses the ownership proof, for private or unreachable zones.
func (s *Service) ForceVerify(ctx context.Context, workspaceID, id uint) (*models.Domain, error) {
	d, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	if d.Banned {
		return d, ErrDomainBanned
	}
	wasVerified := d.Verified
	// Even an admin override can't create a hostname collision with another tenant.
	if !wasVerified {
		if cerr := s.ensureClaimable(d); cerr != nil {
			return d, cerr
		}
	}
	now := time.Now()
	d.Verified = true
	d.VerifiedAt = &now
	d.VerificationCheckedAt = &now
	d.VerificationError = ""
	d.VerificationMisses = 0
	// A waiver, not a proof: stops the drift cron from un-verifying the domain and taking its routes offline.
	d.VerifiedVia = models.VerifiedViaAdmin
	if err := s.repo.Update(d); err != nil {
		return nil, err
	}
	if !wasVerified {
		s.resync(ctx, workspaceID)
	}
	return d, nil
}

// Ban leaves verification state intact, so an unban restores the prior status.
func (s *Service) Ban(ctx context.Context, workspaceID, id uint, reason string) (*models.Domain, error) {
	d, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	if d.Banned {
		return d, nil // idempotent
	}
	now := time.Now()
	d.Banned = true
	d.BannedAt = &now
	d.BanReason = strings.TrimSpace(reason)
	if err := s.repo.Update(d); err != nil {
		return nil, err
	}
	s.resync(ctx, workspaceID)
	return d, nil
}

func (s *Service) Unban(ctx context.Context, workspaceID, id uint) (*models.Domain, error) {
	d, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	if !d.Banned {
		return d, nil // idempotent
	}
	d.Banned = false
	d.BannedAt = nil
	d.BanReason = ""
	if err := s.repo.Update(d); err != nil {
		return nil, err
	}
	s.resync(ctx, workspaceID)
	return d, nil
}

// Admin waivers are probed but never un-verified; provider-automated domains are left to the DNS reconcile cron.
func (s *Service) Reverify(ctx context.Context) error {
	domains, err := s.repo.ListVerifiedManual()
	if err != nil {
		return err
	}
	for i := range domains {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		s.reverifyOne(ctx, &domains[i])
	}
	return nil
}

// reverifyOutcome is what one drift re-check implies for a domain's stored state.
type reverifyOutcome struct {
	Misses   int  // the new consecutive-miss count
	Failed   bool // record the verification error
	Unverify bool // revoke the proof — never applied to a waiver
	Promote  bool // a waiver proved itself; record it as a DNS proof from now on
	Write    bool // anything worth persisting
}

// A proof can go stale, a waiver cannot, and a waiver that starts resolving becomes a proof.
func reverifyDecision(via models.VerifiedVia, misses int, hadError, matched bool) reverifyOutcome {
	waiver := via.IsWaiver()
	if matched {
		return reverifyOutcome{Promote: waiver, Write: waiver || misses != 0 || hadError}
	}
	misses++
	return reverifyOutcome{
		Misses:   misses,
		Failed:   true,
		Unverify: !waiver && misses >= verifyMissThreshold,
		Write:    true,
	}
}

func (s *Service) reverifyOne(ctx context.Context, d *models.Domain) {
	now := time.Now()
	d.VerificationCheckedAt = &now
	out := reverifyDecision(d.VerifiedVia, d.VerificationMisses, d.VerificationError != "", s.txtMatches(ctx, d))
	if !out.Write {
		return // still good, nothing changed — skip the write
	}
	d.VerificationMisses = out.Misses
	if out.Failed {
		d.VerificationError = ErrVerificationFailed.Error()
	} else {
		d.VerificationError = ""
	}
	if out.Promote {
		d.VerifiedVia = models.VerifiedViaDNS
		logger.Info("domain proved ownership; promoting admin override to a DNS proof",
			"domain", d.ID, "name", d.Name)
	}
	if out.Unverify {
		d.Verified = false
		d.VerifiedAt = nil
		logger.Warn("domain ownership lost; un-verifying", "domain", d.ID, "name", d.Name)
	}
	if err := s.repo.Update(d); err != nil {
		logger.Warn("domain reverify update failed", "domain", d.ID, "error", err)
		return
	}
	if !d.Verified {
		s.resync(ctx, d.WorkspaceID)
	}
}

func (s *Service) txtMatches(ctx context.Context, d *models.Domain) bool {
	records, err := s.lookup(ctx, d.ChallengeHost())
	if err != nil {
		return false
	}
	want := d.ChallengeValue()
	for _, r := range records {
		if strings.TrimSpace(r) == want {
			return true
		}
	}
	return false
}
