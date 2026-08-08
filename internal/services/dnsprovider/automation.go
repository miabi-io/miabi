// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package dnsprovider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/dns"
	"github.com/miabi-io/miabi/internal/models"
)

// ErrConflict is returned when a managed name already holds a record Miabi did
// not create — surfaced to the user, never silently overwritten.
var ErrConflict = errors.New("a conflicting DNS record already exists")

// managedTTL is the TTL for records Miabi writes (short, so changes propagate).
const managedTTL = 60 * time.Second

// ProviderExists reports whether a provider id belongs to the workspace. Used by
// the domain service to validate a dns_provider_id before linking it.
func (s *Service) ProviderExists(workspaceID, providerID uint) bool {
	_, err := s.repo.FindInWorkspace(workspaceID, providerID)
	return err == nil
}

// EnsureVerificationRecord idempotently creates the ownership TXT for a provider-connected domain and
// ledgers it, so Verify can succeed without a manual DNS step. A no-op for a manual domain with no
// provider, and it refuses to clobber a conflicting record it did not create.
func (s *Service) EnsureVerificationRecord(ctx context.Context, d *models.Domain) error {
	if d.DNSProviderID == nil {
		return nil // manual domain — nothing to automate
	}
	prov, err := s.Provider(d.WorkspaceID, *d.DNSProviderID)
	if err != nil {
		return err
	}
	rec := dns.Record{Type: "TXT", Name: d.ChallengeHost(), Value: d.ChallengeValue(), TTL: managedTTL}
	if err := s.guardConflict(ctx, prov, d.ID, d.Name, rec); err != nil {
		return err
	}
	if err := prov.SetRecord(ctx, d.Name, rec); err != nil {
		s.markError(*d.DNSProviderID, err)
		return fmt.Errorf("create verification record: %w", err)
	}
	s.markOK(*d.DNSProviderID)
	return s.records.Upsert(&models.DNSRecord{
		DomainID: d.ID, Name: rec.Name, Type: rec.Type, Value: rec.Value,
		Purpose: models.DNSRecordPurposeVerification,
	})
}

// CleanupDomain removes the records Miabi created for a domain — at the provider (best-effort) and from
// the ledger. Called when a domain is deleted, so a managed record never outlives its domain. Only
// ledgered, Miabi-owned records are touched.
func (s *Service) CleanupDomain(ctx context.Context, d *models.Domain) error {
	recs, err := s.records.ListByDomain(d.ID)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return nil
	}
	if d.DNSProviderID != nil {
		if prov, perr := s.Provider(d.WorkspaceID, *d.DNSProviderID); perr == nil {
			for _, r := range recs {
				if derr := prov.DeleteRecord(ctx, d.Name, dns.Record{Type: r.Type, Name: r.Name, Value: r.Value}); derr != nil {
					logger.Warn("dns cleanup: delete record failed", "domain", d.Name, "name", r.Name, "type", r.Type, "error", derr)
				}
			}
		}
	}
	return s.records.DeleteByDomain(d.ID)
}

// Reconcile re-asserts every ledgered record at its provider, so a record a user deleted out of band is
// restored, and refreshes each provider's Status. Driven by a cron. Best-effort: a per-record failure is
// logged and flips the owning provider to error, but never aborts the sweep.
func (s *Service) Reconcile(ctx context.Context) error {
	recs, err := s.records.All()
	if err != nil {
		return err
	}
	// Cache providers + domains across the sweep to avoid repeated lookups.
	provCache := map[uint]dns.Provider{}
	domCache := map[uint]*models.Domain{}
	for i := range recs {
		r := recs[i]
		d := domCache[r.DomainID]
		if d == nil {
			d, err = s.domains.FindByID(r.DomainID)
			if err != nil {
				continue // domain gone; a later prune can drop the orphan
			}
			domCache[r.DomainID] = d
		}
		if d.DNSProviderID == nil {
			continue
		}
		prov := provCache[*d.DNSProviderID]
		if prov == nil {
			prov, err = s.Provider(d.WorkspaceID, *d.DNSProviderID)
			if err != nil {
				continue
			}
			provCache[*d.DNSProviderID] = prov
		}
		if err := prov.SetRecord(ctx, d.Name, dns.Record{Type: r.Type, Name: r.Name, Value: r.Value, TTL: managedTTL}); err != nil {
			logger.Warn("dns reconcile: re-assert failed", "domain", d.Name, "name", r.Name, "error", err)
			s.markError(*d.DNSProviderID, err)
			continue
		}
		s.markOK(*d.DNSProviderID)
	}
	return nil
}

// ReconcileAppAddresses makes an app's A/AAAA/CNAME records match its routed hosts, pointing at the
// gateway's public address. Only hosts under a verified, provider-connected domain are managed; records
// for this app no longer in hosts are removed. Passing none removes them all. Best-effort per record.
func (s *Service) ReconcileAppAddresses(ctx context.Context, workspaceID, appID uint, hosts []string, ip, hostname string) error {
	// Build the desired set keyed by the ledger key (domainID|name|type).
	type want struct {
		domain *models.Domain
		rec    dns.Record
	}
	desired := map[string]want{}
	domByID := map[uint]*models.Domain{}
	for _, h := range hosts {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if h == "" || strings.HasPrefix(h, "*") {
			continue // skip empty + wildcard hosts (wildcards are a cert concern)
		}
		d := s.domainForHost(workspaceID, h)
		if d == nil {
			continue // not under a managed domain
		}
		rec := addressRecord(h, d.Name, ip, hostname)
		if rec.Type == "" {
			continue // no usable target (e.g. CNAME at apex, or no public address)
		}
		domByID[d.ID] = d
		desired[ledgerKey(d.ID, rec.Name, rec.Type)] = want{domain: d, rec: rec}
	}

	provByID := map[uint]dns.Provider{}
	providerFor := func(d *models.Domain) dns.Provider {
		if d.DNSProviderID == nil {
			return nil
		}
		if p, ok := provByID[*d.DNSProviderID]; ok {
			return p
		}
		p, err := s.Provider(d.WorkspaceID, *d.DNSProviderID)
		if err != nil {
			return nil
		}
		provByID[*d.DNSProviderID] = p
		return p
	}

	for _, w := range desired {
		prov := providerFor(w.domain)
		if prov == nil {
			continue
		}
		if err := s.guardConflict(ctx, prov, w.domain.ID, w.domain.Name, w.rec); err != nil {
			logger.Warn("dns app-address: conflict, skipping", "host", w.rec.Name, "error", err)
			continue
		}
		if err := prov.SetRecord(ctx, w.domain.Name, w.rec); err != nil {
			logger.Warn("dns app-address: set failed", "host", w.rec.Name, "error", err)
			s.markError(*w.domain.DNSProviderID, err)
			continue
		}
		s.markOK(*w.domain.DNSProviderID)
		appIDCopy := appID
		_ = s.records.Upsert(&models.DNSRecord{
			DomainID: w.domain.ID, Name: w.rec.Name, Type: w.rec.Type, Value: w.rec.Value,
			Purpose: models.DNSRecordPurposeAppAddress, AppID: &appIDCopy,
		})
	}

	existing, err := s.records.ListByApp(appID)
	if err != nil {
		return err
	}
	for i := range existing {
		r := existing[i]
		if r.Purpose != models.DNSRecordPurposeAppAddress {
			continue
		}
		if _, keep := desired[ledgerKey(r.DomainID, r.Name, r.Type)]; keep {
			continue
		}
		d := domByID[r.DomainID]
		if d == nil {
			if d, err = s.domains.FindByID(r.DomainID); err != nil {
				_ = s.records.Delete(r.ID) // domain gone — drop the orphan
				continue
			}
		}
		if prov := providerFor(d); prov != nil {
			if derr := prov.DeleteRecord(ctx, d.Name, dns.Record{Type: r.Type, Name: r.Name, Value: r.Value}); derr != nil {
				logger.Warn("dns app-address: delete failed", "host", r.Name, "error", derr)
			}
		}
		_ = s.records.Delete(r.ID)
	}
	return nil
}

// domainForHost returns the longest verified, provider-connected domain that host
// falls under (apex or subdomain), or nil when none manages it.
func (s *Service) domainForHost(workspaceID uint, host string) *models.Domain {
	doms, err := s.domains.ListByWorkspace(workspaceID)
	if err != nil {
		return nil
	}
	var best *models.Domain
	for i := range doms {
		d := &doms[i]
		if d.DNSProviderID == nil || !d.Verified {
			continue
		}
		name := strings.ToLower(d.Name)
		if host == name || strings.HasSuffix(host, "."+name) {
			if best == nil || len(name) > len(best.Name) {
				best = d
			}
		}
	}
	return best
}

// addressRecord builds the A/AAAA (preferred, from ip) or CNAME (from hostname)
// record for a host. CNAME is never used at the zone apex (DNS forbids it). Empty
// Type means "no usable target".
func addressRecord(host, zone, ip, hostname string) dns.Record {
	if ip != "" {
		typ := "A"
		if strings.Contains(ip, ":") {
			typ = "AAAA"
		}
		return dns.Record{Type: typ, Name: host, Value: ip, TTL: managedTTL}
	}
	if hostname != "" && !strings.EqualFold(host, zone) {
		v := strings.TrimSuffix(hostname, ".") + "."
		return dns.Record{Type: "CNAME", Name: host, Value: v, TTL: managedTTL}
	}
	return dns.Record{}
}

func ledgerKey(domainID uint, name, typ string) string {
	return fmt.Sprintf("%d|%s|%s", domainID, strings.ToLower(name), strings.ToUpper(typ))
}

// guardConflict refuses to overwrite a record at the managed name that Miabi did not create. A ledgered
// record (ours) or a matching value is fine; a different value with no ledger entry is a conflict. A
// provider read error does not block (best-effort — SetRecord still runs).
func (s *Service) guardConflict(ctx context.Context, prov dns.Provider, domainID uint, zone string, want dns.Record) error {
	if _, err := s.records.Find(domainID, want.Name, want.Type); err == nil {
		return nil // we already own this record
	}
	existing, err := prov.GetRecords(ctx, zone)
	if err != nil {
		return nil // can't verify — proceed (SetRecord is the source of truth)
	}
	wantRel := relName(want.Name, zone)
	for _, r := range existing {
		if strings.EqualFold(relName(r.Name, zone), wantRel) && strings.EqualFold(r.Type, want.Type) && r.Value != want.Value {
			return fmt.Errorf("%w: a %s record at %q already exists and was not created by Miabi", ErrConflict, want.Type, want.Name)
		}
	}
	return nil
}

// relName reduces a possibly-FQDN name to its label relative to zone, so a
// provider's relative names and Miabi's FQDNs compare equal.
func relName(name, zone string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	zone = strings.TrimSuffix(strings.TrimSpace(zone), ".")
	if name == zone {
		return "@"
	}
	return strings.TrimSuffix(name, "."+zone)
}

func (s *Service) markError(providerID uint, cause error) {
	if p, err := s.repo.FindByID(providerID); err == nil {
		p.Status = models.DNSProviderStatusError
		p.LastError = cause.Error()
		_ = s.repo.Update(p)
	}
}

func (s *Service) markOK(providerID uint) {
	if p, err := s.repo.FindByID(providerID); err == nil && p.Status != models.DNSProviderStatusOK {
		p.Status = models.DNSProviderStatusOK
		p.LastError = ""
		_ = s.repo.Update(p)
	}
}
