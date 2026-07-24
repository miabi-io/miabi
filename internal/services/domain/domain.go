// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package domain manages workspace-owned domains: registration, DNS-verified
// ownership, and the default TLS policy routes inherit. It is the first-class
// owned-hostname resource (distinct from the declarative Route kind).
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
	// ErrNameImmutable means a caller tried to rename an existing domain. The name
	// is fixed at creation; delete and re-create to change it.
	ErrNameImmutable = errors.New("the domain name cannot be changed; delete the domain and add it again")
	// ErrVerificationFailed means the expected TXT record was not found.
	ErrVerificationFailed = errors.New("DNS verification record not found")
	// ErrProviderNotFound means the linked DNS provider does not exist in the
	// workspace (or no automator is wired).
	ErrProviderNotFound = errors.New("DNS provider not found in this workspace")
	// ErrDomainBanned means the domain has been banned by a platform admin and
	// cannot be verified or served.
	ErrDomainBanned = errors.New("this domain has been banned by a platform administrator")
	// ErrDomainClaimed means the hostname is already verified by another workspace
	// (or overlaps a verified wildcard elsewhere). At most one workspace may own a
	// verified domain platform-wide, so its routes can't collide with another
	// tenant's. Registration stays open; only verification is exclusive.
	ErrDomainClaimed = errors.New("this domain is already verified by another workspace")
)

// hostnameRe is a permissive domain-name check (labels, at least one dot, a
// 2+ letter TLD). A leading "*." is stripped before matching for wildcards.
var hostnameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// resolver is overridable in tests; defaults to the system resolver.
type lookupTXT func(ctx context.Context, host string) ([]string, error)

// DNSAutomator drives the records Miabi manages for a provider-connected domain.
// Injected (nil = manual only): the verification TXT is created automatically and
// the ledger is cleaned up on delete. Implemented by the dnsprovider service.
type DNSAutomator interface {
	// ProviderExists reports whether a provider id belongs to the workspace.
	ProviderExists(workspaceID, providerID uint) bool
	// EnsureVerificationRecord creates the ownership TXT for a provider-connected
	// domain (no-op when the domain has no provider).
	EnsureVerificationRecord(ctx context.Context, d *models.Domain) error
	// CleanupDomain removes the managed records for a deleted domain.
	CleanupDomain(ctx context.Context, d *models.Domain) error
}

// verifyRetries bounds how long Verify waits for an auto-created TXT to become
// visible to the resolver, keeping the request responsive.
const (
	verifyRetries = 3
	verifyDelay   = 2 * time.Second
	// verifyMissThreshold is how many consecutive failed re-checks the drift cron
	// tolerates before un-verifying a previously verified domain, absorbing
	// transient DNS resolution failures.
	verifyMissThreshold = 3
)

// ProxyResyncer re-renders a workspace's gateway config. The domain service calls
// it when ownership changes (verified ⇄ unverified) so the workspace's routes go
// live or offline without further action. Implemented by the route service;
// injected after construction (nil-safe).
type ProxyResyncer interface {
	SyncWorkspaceProxy(ctx context.Context, workspaceID uint) error
}

// Service manages domains.
type Service struct {
	repo      *repositories.DomainRepository
	lookup    lookupTXT
	automator DNSAutomator
	resyncer  ProxyResyncer
}

// NewService wires the domain service. The default TXT lookup queries the
// domain's authoritative nameservers directly (falling back to the system
// resolver)
func NewService(repo *repositories.DomainRepository) *Service {
	return &Service{repo: repo, lookup: authoritativeLookupTXT}
}

// SetDNSAutomator wires DNS automation for provider-connected domains (nil-safe;
// nil leaves the manual copy-paste flow as the only path).
func (s *Service) SetDNSAutomator(a DNSAutomator) { s.automator = a }

// SetProxyResyncer wires the gateway re-sync triggered on a verification change
// (nil-safe; nil disables the automatic route go-live/offline).
func (s *Service) SetProxyResyncer(r ProxyResyncer) { s.resyncer = r }

// resync re-renders the workspace's gateway config after an ownership change, so
// its routes flip live/offline. Best-effort: a failure is logged, not returned —
// the periodic resync and the next route edit both recover.
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
	if err := s.repo.Create(d); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Service) Update(workspaceID, id uint, in Input) (*models.Domain, error) {
	d, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	in.normalize()
	// The domain name is immutable once created: the ownership proof, issued
	// certificates, and any routes bound under it are all keyed to this name.
	// Renaming would silently invalidate them, so we refuse it — the user deletes
	// and re-creates the domain instead. An unchanged (or empty) name is fine, so
	// updating only TLS mode / wildcard still works.
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
	return d, nil
}

func (s *Service) List(workspaceID uint) ([]models.Domain, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

func (s *Service) Delete(ctx context.Context, workspaceID, id uint) error {
	d, err := s.Get(workspaceID, id)
	if err != nil {
		return err
	}
	// Remove any managed DNS records (the verification TXT) before the domain row,
	// so a provider-created record never outlives its domain. Best-effort.
	if s.automator != nil {
		_ = s.automator.CleanupDomain(ctx, d)
	}
	return s.repo.Delete(workspaceID, id)
}

// SetDNSProvider links (or, with nil, unlinks) a connected DNS provider to a
// domain so ownership verification (and later app records) is automated. A nil
// providerID reverts the domain to the manual copy-paste flow.
func (s *Service) SetDNSProvider(workspaceID, id uint, providerID *uint) (*models.Domain, error) {
	d, err := s.Get(workspaceID, id)
	if err != nil {
		return nil, err
	}
	if providerID != nil {
		if s.automator == nil || !s.automator.ProviderExists(workspaceID, *providerID) {
			return nil, ErrProviderNotFound
		}
	}
	d.DNSProviderID = providerID
	if err := s.repo.Update(d); err != nil {
		return nil, err
	}
	return d, nil
}

// Verify checks the domain's ownership TXT record and, on success, marks it
// verified. For a provider-connected domain it first creates the TXT itself
// (idempotent) and retries the lookup briefly to absorb propagation, so no manual
// DNS step is needed. The lookup reads live DNS, so re-verification is idempotent.
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
	for i := range others {
		if domainsOverlap(d, &others[i]) {
			return ErrDomainClaimed
		}
	}
	return nil
}

// domainsOverlap reports whether a and b would serve the same hostname: an exact
// name match, or one being a wildcard that covers the other as a subdomain.
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

// ForceVerify marks a domain verified without a DNS check — a platform-admin
// override for private-DNS or otherwise unreachable zones. It clears any prior
// failure state and re-syncs the workspace so the domain's routes go live. Use
// sparingly: it bypasses the ownership proof Verify enforces.
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
	if err := s.repo.Update(d); err != nil {
		return nil, err
	}
	if !wasVerified {
		s.resync(ctx, workspaceID)
	}
	return d, nil
}

// Ban blocks a domain platform-wide (a platform-admin action, e.g. for abuse).
// A banned domain is never served — its routes are forced offline on the next
// re-sync, triggered here — and it can no longer be verified. Verification state
// is left intact so an unban restores the prior status.
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

// Unban lifts a domain ban and re-syncs the workspace so its routes can serve
// again (subject to the normal verification/privilege gate).
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

// Reverify re-checks ownership for every verified, manually-managed domain and
// un-verifies any whose TXT record has been missing for verifyMissThreshold
// consecutive runs (transient DNS failures are absorbed by the threshold). On an
// un-verify it re-syncs the workspace so the domain's routes drop offline.
// Provider-automated domains are skipped — the DNS reconcile cron reasserts their
// records. Intended to be driven by a periodic job.
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

// reverifyOne re-checks a single verified domain's TXT record and persists the
// outcome, un-verifying it once misses cross the threshold.
func (s *Service) reverifyOne(ctx context.Context, d *models.Domain) {
	now := time.Now()
	d.VerificationCheckedAt = &now
	if s.txtMatches(ctx, d) {
		if d.VerificationMisses == 0 && d.VerificationError == "" {
			return // still good, nothing changed — skip the write
		}
		d.VerificationMisses = 0
		d.VerificationError = ""
		if err := s.repo.Update(d); err != nil {
			logger.Warn("domain reverify update failed", "domain", d.ID, "error", err)
		}
		return
	}
	d.VerificationMisses++
	d.VerificationError = ErrVerificationFailed.Error()
	if d.VerificationMisses >= verifyMissThreshold {
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

// txtMatches reports whether the domain's ownership TXT record is currently
// present in DNS.
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
