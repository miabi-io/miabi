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

package dnsprovider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/dns"
	"github.com/miabi-io/miabi/internal/dnscatalog"
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

// RecordsSupported reports whether a provider can own ordinary DNS records. A challenge-only
// host (acme-dns) answers for its own delegated subdomain and nothing else, so it can solve
// DNS-01 but can neither carry the ownership TXT nor an app's A/AAAA records.
func (s *Service) RecordsSupported(workspaceID, providerID uint) bool {
	p, err := s.repo.FindInWorkspace(workspaceID, providerID)
	if err != nil {
		return false
	}
	d, ok := dnscatalog.Get(p.Type)
	return ok && !d.ChallengeOnly
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
// for this app no longer wanted are removed. Passing none removes them all. Best-effort per record.
func (s *Service) ReconcileAppAddresses(ctx context.Context, workspaceID, appID uint, hosts []string, target models.PublicAddress) error {
	type want struct {
		domain *models.Domain
		rec    dns.Record
	}
	desired := map[string]want{}
	order := make([]string, 0)
	domByID := map[uint]*models.Domain{}
	for _, h := range hosts {
		h = models.NormalizeHost(h)
		if h == "" || strings.HasPrefix(h, "*") {
			continue // skip empty + wildcard hosts (wildcards are a cert concern)
		}
		d := s.domainForHost(workspaceID, h)
		if d == nil {
			continue // not under a managed domain
		}
		domByID[d.ID] = d
		for _, rec := range addressRecords(h, d.Name, hosts, target) {
			key := ledgerKey(d.ID, rec.Name, rec.Type)
			if _, dup := desired[key]; !dup {
				order = append(order, key)
			}
			desired[key] = want{domain: d, rec: rec}
		}
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

	// A www alias follows its bare name only when that name will point here: one held by a record Miabi did not
	// create would carry www away with it, so www keeps address records of its own instead.
	if target.HasIP() {
		for _, key := range slices.Clone(order) {
			w := desired[key]
			if w.rec.Type != "CNAME" {
				continue
			}
			prov := providerFor(w.domain)
			bare := strings.TrimSuffix(w.rec.Value, ".")
			if prov == nil || s.bareNameFree(ctx, prov, w.domain, bare, target) {
				continue
			}
			delete(desired, key)
			for _, rec := range addressRecords(w.rec.Name, w.domain.Name, nil, target) {
				k := ledgerKey(w.domain.ID, rec.Name, rec.Type)
				order = append(order, k)
				desired[k] = want{domain: w.domain, rec: rec}
			}
		}
	}

	// Stale records go first: a name switching between A/AAAA and CNAME cannot hold both at once.
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

	for _, key := range order {
		w, ok := desired[key]
		if !ok {
			continue
		}
		prov := providerFor(w.domain)
		if prov == nil {
			continue
		}
		if err := s.guardConflict(ctx, prov, w.domain.ID, w.domain.Name, w.rec); err != nil {
			logger.Warn("dns app-address: conflict, skipping", "host", w.rec.Name, "type", w.rec.Type, "error", err)
			continue
		}
		if err := prov.SetRecord(ctx, w.domain.Name, w.rec); err != nil {
			logger.Warn("dns app-address: set failed", "host", w.rec.Name, "type", w.rec.Type, "error", err)
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
	return nil
}

// bareNameFree reports whether Miabi may point bare at target: no record it did not create stands in the way.
func (s *Service) bareNameFree(ctx context.Context, prov dns.Provider, d *models.Domain, bare string, target models.PublicAddress) bool {
	for _, rec := range addressRecords(bare, d.Name, nil, target) {
		if s.guardConflict(ctx, prov, d.ID, d.Name, rec) != nil {
			return false
		}
	}
	return true
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

// addressRecords builds the records that point host, under zone, at target; see models.PublicAddress.AddressRecords.
func addressRecords(host, zone string, routed []string, target models.PublicAddress) []dns.Record {
	planned := target.AddressRecords(host, routed, strings.EqualFold(host, zone))
	out := make([]dns.Record, 0, len(planned))
	for _, r := range planned {
		out = append(out, dns.Record{Type: r.Type, Name: r.Name, Value: r.Value, TTL: managedTTL})
	}
	return out
}

func ledgerKey(domainID uint, name, typ string) string {
	return fmt.Sprintf("%d|%s|%s", domainID, strings.ToLower(name), strings.ToUpper(typ))
}

// guardConflict refuses to overwrite a record at the managed name that Miabi did not create. A ledgered
// record (ours) or a matching value is fine; a different value with no ledger entry is a conflict, and so
// is any record beside a CNAME, which DNS lets share its name with nothing. A provider read error does not
// block (best-effort — SetRecord still runs).
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
		if !strings.EqualFold(relName(r.Name, zone), wantRel) {
			continue
		}
		sameType := strings.EqualFold(r.Type, want.Type)
		if sameType && (r.Value == want.Value || strings.EqualFold(want.Type, "CNAME") && sameTarget(r.Value, want.Value)) {
			continue
		}
		if sameType || cnameClash(r.Type, want.Type) {
			return fmt.Errorf("%w: a %s record at %q already exists and was not created by Miabi", ErrConflict, r.Type, want.Name)
		}
	}
	return nil
}

// cnameClash reports two record types that cannot share a name: a CNAME and an address record.
func cnameClash(have, want string) bool {
	isAddr := func(t string) bool { return strings.EqualFold(t, "A") || strings.EqualFold(t, "AAAA") }
	return (strings.EqualFold(want, "CNAME") && isAddr(have)) || (strings.EqualFold(have, "CNAME") && isAddr(want))
}

// sameTarget compares CNAME targets the way DNS does: with or without the final dot, in any case.
func sameTarget(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
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
