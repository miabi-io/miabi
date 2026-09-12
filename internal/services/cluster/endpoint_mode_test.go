// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

func TestSwitchingTheServiceEndpointModeSwitchesRunningServices(t *testing.T) {
	store := &memStore{
		def:    models.Cluster{ID: 1, IsDefault: true, Name: "default"},
		others: map[uint]models.Cluster{4: {ID: 4, Name: "paris", Mode: models.ClusterModeSwarm}},
	}
	s := &Service{store: store}
	switched := make(chan models.ServiceEndpointMode, 2)
	s.SetEndpointModeListener(func(_ context.Context, id uint, mode models.ServiceEndpointMode) {
		if id == 4 {
			switched <- mode
		}
	})

	if got := s.ServiceEndpointMode(4); got != models.ServiceEndpointVIP {
		t.Fatalf("unset mode = %q, want vip", got)
	}
	dnsrr := models.ServiceEndpointDNSRR
	if _, err := s.UpdateCluster(4, ClusterPatch{ServiceEndpointMode: &dnsrr}); err != nil {
		t.Fatalf("update: %v", err)
	}
	select {
	case mode := <-switched:
		if mode != dnsrr {
			t.Errorf("services switched to %q, want dnsrr", mode)
		}
	case <-time.After(time.Second):
		t.Fatal("running services were not switched")
	}
	if got := s.ServiceEndpointMode(4); got != dnsrr {
		t.Errorf("stored mode = %q, want dnsrr", got)
	}

	if _, err := s.UpdateCluster(4, ClusterPatch{ServiceEndpointMode: &dnsrr}); err != nil {
		t.Fatalf("saving the same mode: %v", err)
	}
	select {
	case <-switched:
		t.Error("services were switched although the mode did not change")
	case <-time.After(50 * time.Millisecond):
	}

	bogus := models.ServiceEndpointMode("ipvs")
	if _, err := s.UpdateCluster(4, ClusterPatch{ServiceEndpointMode: &bogus}); !errors.Is(err, ErrInvalidEndpointMode) {
		t.Errorf("bogus mode err = %v, want ErrInvalidEndpointMode", err)
	}
}

func TestAVirtualIPFailureTellsTheAdminToSwitch(t *testing.T) {
	broken := &NetCheckResult{From: "paris", DNS: true}
	if v := vipVerdict(*broken); v == "ok" || v == "" {
		t.Errorf("verdict for a refused virtual IP = %q, want the IPVS explanation", v)
	}

	out := NetCheck{OK: true, Summary: "All paths work.", VIP: broken}
	finishVIP(&out, models.ServiceEndpointVIP)
	if out.OK {
		t.Error("a cluster still on virtual IPs reported OK with a broken virtual IP")
	}

	out = NetCheck{OK: true, Summary: "All paths work.", VIP: broken}
	finishVIP(&out, models.ServiceEndpointDNSRR)
	if !out.OK {
		t.Error("a cluster already on DNS round-robin was failed for a virtual IP it does not use")
	}

	out = NetCheck{OK: true, Summary: "All paths work.", VIP: &NetCheckResult{Error: "no manager", Verdict: "could not start"}}
	finishVIP(&out, models.ServiceEndpointVIP)
	if !out.OK {
		t.Error("a probe that could not run was reported as a broken virtual IP")
	}
}
