// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package dns

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/libdns/libdns"
)

// fakeLister is a libdns client that can enumerate zones (like Cloudflare).
type fakeLister struct {
	zones   []string // account zones (FQDN, trailing dot)
	setZone string   // zone captured from the last SetRecords/DeleteRecords
	setName string   // relative name captured from the last write
}

func (f *fakeLister) ListZones(context.Context) ([]libdns.Zone, error) {
	out := make([]libdns.Zone, len(f.zones))
	for i, z := range f.zones {
		out[i] = libdns.Zone{Name: z}
	}
	return out, nil
}
func (f *fakeLister) GetRecords(context.Context, string) ([]libdns.Record, error) { return nil, nil }
func (f *fakeLister) SetRecords(_ context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	f.setZone = zone
	if len(recs) > 0 {
		f.setName = recs[0].RR().Name
	}
	return recs, nil
}
func (f *fakeLister) DeleteRecords(_ context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	f.setZone = zone
	return recs, nil
}

// fakeNoList cannot enumerate zones (like Route 53/DigitalOcean here): GetRecords
// errors unless asked for the one zone it actually hosts.
type fakeNoList struct {
	zone    string // the single hosted zone, no trailing dot
	setZone string
}

func (f *fakeNoList) GetRecords(_ context.Context, zone string) ([]libdns.Record, error) {
	if strings.TrimSuffix(zone, ".") == f.zone {
		return nil, nil
	}
	return nil, fmt.Errorf("expected 1 zone, got 0 for %s", zone)
}
func (f *fakeNoList) SetRecords(_ context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	f.setZone = zone
	return recs, nil
}
func (f *fakeNoList) DeleteRecords(_ context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	return recs, nil
}

func TestSetRecordResolvesSubdomainToParentZone_Lister(t *testing.T) {
	f := &fakeLister{zones: []string{"miabi.io.", "other.example."}}
	a := newAdapter(f)
	if a.lister == nil {
		t.Fatal("expected the fake to be detected as a ZoneLister")
	}
	err := a.SetRecord(context.Background(), "apps.demo.miabi.io",
		Record{Type: "TXT", Name: "_miabi-challenge.apps.demo.miabi.io", Value: "v"})
	if err != nil {
		t.Fatalf("SetRecord: %v", err)
	}
	if f.setZone != "miabi.io." {
		t.Errorf("libdns zone = %q, want %q", f.setZone, "miabi.io.")
	}
	if f.setName != "_miabi-challenge.apps.demo" {
		t.Errorf("relative name = %q, want %q", f.setName, "_miabi-challenge.apps.demo")
	}
}

func TestResolveZonePicksLongestSuffix(t *testing.T) {
	// A delegated subdomain zone must win over the apex when both are present.
	f := &fakeLister{zones: []string{"miabi.io.", "demo.miabi.io."}}
	a := newAdapter(f)
	got, err := a.resolveZone(context.Background(), "apps.demo.miabi.io")
	if err != nil {
		t.Fatalf("resolveZone: %v", err)
	}
	if got != "demo.miabi.io." {
		t.Errorf("resolved zone = %q, want %q", got, "demo.miabi.io.")
	}
}

func TestResolveZoneNoMatch_Lister(t *testing.T) {
	f := &fakeLister{zones: []string{"someone-else.com."}}
	a := newAdapter(f)
	if _, err := a.resolveZone(context.Background(), "apps.demo.miabi.io"); err == nil {
		t.Fatal("expected an error when no account zone manages the domain")
	}
}

func TestSetRecordResolvesSubdomain_ProbeFallback(t *testing.T) {
	f := &fakeNoList{zone: "miabi.io"}
	a := newAdapter(f)
	if a.lister != nil {
		t.Fatal("fake without ListZones must not be treated as a ZoneLister")
	}
	err := a.SetRecord(context.Background(), "apps.demo.miabi.io",
		Record{Type: "TXT", Name: "_miabi-challenge.apps.demo.miabi.io", Value: "v"})
	if err != nil {
		t.Fatalf("SetRecord: %v", err)
	}
	if f.setZone != "miabi.io." {
		t.Errorf("probed zone = %q, want %q", f.setZone, "miabi.io.")
	}
}
