// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dns abstracts a managed DNS host behind one small Provider interface,
// backed by libdns modules (Cloudflare, Route 53, DigitalOcean). It mirrors the
// blob.Store pattern: adding a host later is a new case in Build, not a new
// client. Miabi uses it to manage only the records it owns (ownership TXT, app
// A/AAAA) — never a user's other records.
package dns

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/libdns/cloudflare"
	"github.com/libdns/digitalocean"
	"github.com/libdns/libdns"
	"github.com/libdns/route53"
	"github.com/miabi-io/miabi/internal/models"
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

// Credentials is the union of fields across provider types, parsed from the
// decrypted JSON blob. Only the fields relevant to a given Type are set.
type Credentials struct {
	APIToken        string `json:"api_token,omitempty"`         // cloudflare, digitalocean
	AccessKeyID     string `json:"access_key_id,omitempty"`     // route53
	SecretAccessKey string `json:"secret_access_key,omitempty"` // route53
	Region          string `json:"region,omitempty"`            // route53
}

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

// Build returns a Provider for a connection type + its (already-decrypted)
// credentials. Unknown types return an error.
func Build(providerType string, creds Credentials) (Provider, error) {
	switch providerType {
	case models.DNSProviderCloudflare:
		if creds.APIToken == "" {
			return nil, fmt.Errorf("cloudflare: api_token is required")
		}
		return newAdapter(&cloudflare.Provider{APIToken: creds.APIToken}), nil
	case models.DNSProviderDigitalOcean:
		if creds.APIToken == "" {
			return nil, fmt.Errorf("digitalocean: api_token is required")
		}
		return newAdapter(&digitalocean.Provider{APIToken: creds.APIToken}), nil
	case models.DNSProviderRoute53:
		if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
			return nil, fmt.Errorf("route53: access_key_id and secret_access_key are required")
		}
		return newAdapter(&route53.Provider{
			AccessKeyId: creds.AccessKeyID, SecretAccessKey: creds.SecretAccessKey, Region: creds.Region,
		}), nil
	default:
		return nil, fmt.Errorf("unknown DNS provider type %q", providerType)
	}
}

type adapter struct {
	z      zoneClient
	lister libdns.ZoneLister
	mu     sync.Mutex
	zones  map[string]string
}

// newAdapter wraps a libdns client, detecting whether it can enumerate zones.
func newAdapter(z zoneClient) *adapter {
	a := &adapter{z: z, zones: map[string]string{}}
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
	a.mu.Unlock()
	if ok {
		return cached, nil
	}
	zone, err := a.discoverZone(ctx, key)
	if err != nil {
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
	// Fallback for providers without zone enumeration: try the domain and each
	// parent
	labels := strings.Split(domain, ".")
	for i := 0; i+1 < len(labels); i++ {
		cand := strings.Join(labels[i:], ".")
		if _, err := a.z.GetRecords(ctx, canonicalZone(cand)); err == nil {
			return canonicalZone(cand), nil
		}
	}
	return "", fmt.Errorf("could not find a DNS zone that manages %s", domain)
}

// canonicalZone gives the zone a trailing dot, libdns's canonical FQDN form.
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
