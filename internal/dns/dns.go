// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dns abstracts a managed DNS host behind one small Provider interface, backed by libdns
// modules. It mirrors the blob.Store pattern: adding a host later is a new case in Build, not a
// new client. Miabi manages only the records it owns — never a user's other records.
package dns

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/libdns/libdns"
	"github.com/miabi-io/miabi/internal/dnscatalog"
)

// Record is Miabi's provider-agnostic view of a DNS record. Name is a FQDN (the
// adapter relativizes it to the zone); Value is the record data (TXT text, A/AAAA
// IP, CNAME target).
type Record struct {
	Type  string        `json:"type"`  // TXT | A | AAAA | CNAME
	Name  string        `json:"name"`  // FQDN, e.g. _miabi-challenge.example.com
	Value string        `json:"value"` // record data
	TTL   time.Duration `json:"-"`     // 0 = provider default
}

// Credentials is a provider's credential blob, keyed by the field names its dnscatalog
// descriptor declares. Wire-compatible with the flat struct it replaced.
type Credentials map[string]string

// Provider is Miabi's view of a DNS host. Implementations are idempotent and
// safe for concurrent use (libdns guarantees the latter).
type Provider interface {
	// GetRecords lists the records in a zone (used by test + conflict checks).
	GetRecords(ctx context.Context, zone string) ([]Record, error)
	// SetRecord upserts a record (creates or replaces the RRset for name+type).
	SetRecord(ctx context.Context, zone string, rec Record) error
	// DeleteRecord removes a record.
	DeleteRecord(ctx context.Context, zone string, rec Record) error
	// Test validates the credentials against a zone (a successful GetRecords).
	Test(ctx context.Context, zone string) error
}

// zoneClient is the libdns surface the adapter needs; every wired module
// satisfies it.
type zoneClient interface {
	libdns.RecordGetter
	libdns.RecordSetter
	libdns.RecordDeleter
}

// Build returns a Provider for a connection type + its (already-decrypted) credentials.
// Required fields are validated against the catalog before the per-type constructor runs.
func Build(providerType string, creds Credentials) (Provider, error) {
	d, ok := dnscatalog.Get(providerType)
	if !ok {
		return nil, fmt.Errorf("unknown DNS provider type %q", providerType)
	}
	if err := d.Validate(creds); err != nil {
		return nil, err
	}
	build, ok := constructors[d.Type]
	if !ok {
		return nil, fmt.Errorf("DNS provider type %q is catalogued but not wired", providerType)
	}
	return newAdapter(build(creds)), nil
}

// probeDepth bounds parent-suffix probing for providers without zone enumeration.
const probeDepth = 3

type adapter struct {
	z      zoneClient
	lister libdns.ZoneLister
	mu     sync.Mutex
	zones  map[string]string
	failed map[string]string
}

func newAdapter(z zoneClient) *adapter {
	a := &adapter{z: z, zones: map[string]string{}, failed: map[string]string{}}
	if zl, ok := z.(libdns.ZoneLister); ok {
		a.lister = zl
	}
	return a
}

// resolveZone maps a domain to the provider zone that actually hosts it, caching
// the result. A subdomain resolves to its parent zone; an apex resolves to itself.
func (a *adapter) resolveZone(ctx context.Context, domain string) (string, error) {
	key := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if key == "" {
		return "", fmt.Errorf("empty domain")
	}
	a.mu.Lock()
	cached, ok := a.zones[key]
	failure, failedBefore := a.failed[key]
	a.mu.Unlock()
	if ok {
		return cached, nil
	}
	// Negative results are cached too: without this, every record write for a domain the
	// provider does not host repeats the whole probe against a rate-limited API.
	if failedBefore {
		return "", errors.New(failure)
	}
	zone, err := a.discoverZone(ctx, key)
	if err != nil {
		a.mu.Lock()
		a.failed[key] = err.Error()
		a.mu.Unlock()
		return "", err
	}
	a.mu.Lock()
	a.zones[key] = zone
	a.mu.Unlock()
	return zone, nil
}

// discoverZone finds the hosting zone for domain (lowercase, no trailing dot).
// With a ZoneLister it picks the longest account zone that is a suffix of domain;
// otherwise it probes parent suffixes, most specific first, via GetRecords.
func (a *adapter) discoverZone(ctx context.Context, domain string) (string, error) {
	if a.lister != nil {
		zones, err := a.lister.ListZones(ctx)
		if err != nil {
			return "", fmt.Errorf("list zones: %w", err)
		}
		best := ""
		for _, z := range zones {
			zn := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(z.Name), "."))
			if zn == "" {
				continue
			}
			if domain == zn || strings.HasSuffix(domain, "."+zn) {
				if len(zn) > len(best) {
					best = zn
				}
			}
		}
		if best == "" {
			return "", fmt.Errorf("no DNS zone in this account manages %s — add the domain to the provider first", domain)
		}
		return canonicalZone(best), nil
	}
	// Fallback for providers without zone enumeration: probe the domain and its parents,
	// most specific first. Bounded to probeDepth candidates so a deep subdomain does not
	// issue one API call per label against a rate-limited host.
	labels := strings.Split(domain, ".")
	empty := ""
	tried := 0
	for i := 0; i+1 < len(labels) && tried < probeDepth; i++ {
		cand := canonicalZone(strings.Join(labels[i:], "."))
		tried++
		recs, err := a.z.GetRecords(ctx, cand)
		if err != nil {
			continue
		}
		// A zone that returns records is proof. An empty success is not — some hosts
		// answer 200 with nothing for a zone the account does not hold — but an owned
		// zone can legitimately be empty, so keep the first as a fallback rather than
		// rejecting it.
		if len(recs) > 0 {
			return cand, nil
		}
		if empty == "" {
			empty = cand
		}
	}
	if empty != "" {
		return empty, nil
	}
	return "", fmt.Errorf("could not find a DNS zone that manages %s", domain)
}

func canonicalZone(zone string) string {
	zone = strings.TrimSpace(zone)
	if zone == "" {
		return "."
	}
	if !strings.HasSuffix(zone, ".") {
		zone += "."
	}
	return zone
}

func (a *adapter) GetRecords(ctx context.Context, zone string) ([]Record, error) {
	cz, err := a.resolveZone(ctx, zone)
	if err != nil {
		return nil, err
	}
	recs, err := a.z.GetRecords(ctx, cz)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(recs))
	for _, r := range recs {
		rr := r.RR()
		// Return the FQDN: the resolved zone may be a parent of the requested
		// domain, so libdns's zone-relative name is relativized to the wrong root
		// for callers that compare against the domain. AbsoluteName re-anchors it.
		out = append(out, Record{Type: rr.Type, Name: libdns.AbsoluteName(rr.Name, cz), Value: rr.Data, TTL: rr.TTL})
	}
	return out, nil
}

func (a *adapter) SetRecord(ctx context.Context, zone string, rec Record) error {
	cz, err := a.resolveZone(ctx, zone)
	if err != nil {
		return err
	}
	_, err = a.z.SetRecords(ctx, cz, []libdns.Record{a.toRR(cz, rec)})
	return err
}

func (a *adapter) DeleteRecord(ctx context.Context, zone string, rec Record) error {
	cz, err := a.resolveZone(ctx, zone)
	if err != nil {
		return err
	}
	_, err = a.z.DeleteRecords(ctx, cz, []libdns.Record{a.toRR(cz, rec)})
	return err
}

func (a *adapter) Test(ctx context.Context, zone string) error {
	cz, err := a.resolveZone(ctx, zone)
	if err != nil {
		return err
	}
	_, err = a.z.GetRecords(ctx, cz)
	return err
}

// toRR builds a libdns record relative to the zone. RR implements libdns.Record;
// providers accept it for set/delete (only the specific RR-types are required on
// the *return* path, which we don't use here).
func (a *adapter) toRR(zone string, rec Record) libdns.RR {
	return libdns.RR{
		Type: rec.Type,
		Name: libdns.RelativeName(strings.TrimSuffix(rec.Name, "."), zone),
		Data: rec.Value,
		TTL:  rec.TTL,
	}
}
