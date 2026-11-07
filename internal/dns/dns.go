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

// Package dns wraps libdns behind one Provider interface; Miabi only manages the records it owns.
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

// Name is a FQDN; the adapter relativizes it to the zone.
type Record struct {
	Type  string        `json:"type"`  // TXT | A | AAAA | CNAME
	Name  string        `json:"name"`  // FQDN, e.g. _miabi-challenge.example.com
	Value string        `json:"value"` // record data
	TTL   time.Duration `json:"-"`     // 0 = provider default
}

// Credentials keys are the field names the dnscatalog descriptor declares.
type Credentials map[string]string

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

type zoneClient interface {
	libdns.RecordGetter
	libdns.RecordSetter
	libdns.RecordDeleter
}

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

// A subdomain resolves to its parent zone; results are cached.
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
	// Negative results are cached so writes for an unhosted domain don't re-probe a rate-limited API.
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
	// Bounded to probeDepth candidates so a deep subdomain doesn't cost one API call per label.
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
		// Some hosts return 200 with nothing for unheld zones, but owned zones can be empty, so keep it as a fallback.
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
		// The resolved zone may be a parent of the domain, so re-anchor names with AbsoluteName.
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

func (a *adapter) toRR(zone string, rec Record) libdns.RR {
	return libdns.RR{
		Type: rec.Type,
		Name: libdns.RelativeName(strings.TrimSuffix(rec.Name, "."), zone),
		Data: rec.Value,
		TTL:  rec.TTL,
	}
}
