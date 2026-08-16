// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package route

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/proxy"
)

func TestNormalizeMaintenanceRejectsNonErrorStatus(t *testing.T) {
	for _, code := range []int{200, 204, 302, 399, 600} {
		_, err := normalizeMaintenance(&models.RouteMaintenance{Enabled: true, StatusCode: code})
		if !errors.Is(err, ErrMaintenanceStatus) {
			t.Errorf("status %d: err = %v, want ErrMaintenanceStatus", code, err)
		}
	}
}

func TestNormalizeMaintenanceAcceptsErrorStatus(t *testing.T) {
	for _, code := range []int{400, 418, 503, 599} {
		if _, err := normalizeMaintenance(&models.RouteMaintenance{Enabled: true, StatusCode: code}); err != nil {
			t.Errorf("status %d rejected: %v", code, err)
		}
	}
}

func TestNormalizeMaintenanceAllowsZeroStatus(t *testing.T) {
	mt, err := normalizeMaintenance(&models.RouteMaintenance{Enabled: true})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if mt.StatusCode != 0 || mt.Message != "" {
		t.Errorf("defaults were filled in locally: %+v", mt)
	}
}

func TestNormalizeMaintenanceCapsMessage(t *testing.T) {
	_, err := normalizeMaintenance(&models.RouteMaintenance{
		Enabled: true, Message: strings.Repeat("x", maintenanceMessageMax+1),
	})
	if !errors.Is(err, ErrMaintenanceMessage) {
		t.Errorf("err = %v, want ErrMaintenanceMessage", err)
	}
}

func TestNormalizeMaintenanceTrimsMessage(t *testing.T) {
	mt, err := normalizeMaintenance(&models.RouteMaintenance{Enabled: true, Message: "  back soon \n"})
	if err != nil || mt.Message != "back soon" {
		t.Errorf("mt = %+v, err = %v", mt, err)
	}
}

func TestRenderedRouteCarriesGatewaySettings(t *testing.T) {
	rt := &models.Route{
		ID: 3, WorkspaceID: 1, Name: "api", Path: "/",
		ExploitProtection: true,
		Maintenance:       models.RouteMaintenance{Enabled: true, StatusCode: 503, Message: "brb"},
	}
	rr := renderedRoute(rt, []proxy.Backend{{Endpoint: "http://mb-app-3:80"}}, false)

	if !rr.ExploitProtection {
		t.Error("ExploitProtection did not reach the renderer")
	}
	if rr.Maintenance == nil || !rr.Maintenance.Enabled || rr.Maintenance.StatusCode != 503 {
		t.Errorf("Maintenance = %+v", rr.Maintenance)
	}
}

func TestRenderedRouteWithoutMaintenance(t *testing.T) {
	rt := &models.Route{ID: 4, Name: "web"}
	rr := renderedRoute(rt, nil, false)
	if rr.Maintenance != nil {
		t.Errorf("a route that is not parked carried a maintenance block: %+v", rr.Maintenance)
	}
}

func allExist(string) (bool, error) { return true, nil }

func TestNormalizeChainPreservesOrder(t *testing.T) {
	in := []string{"rate-limit", "basic-auth", "cors"}
	out, err := normalizeChain(in, allExist)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("chain reordered: got %v, want %v", out, in)
		}
	}
}

func TestNormalizeChainReversedIsADifferentChain(t *testing.T) {
	a, _ := normalizeChain([]string{"rate-limit", "basic-auth"}, allExist)
	b, _ := normalizeChain([]string{"basic-auth", "rate-limit"}, allExist)
	if a[0] == b[0] {
		t.Error("reversing the input did not reverse the chain")
	}
}

func TestNormalizeChainRejectsDuplicate(t *testing.T) {
	_, err := normalizeChain([]string{"cors", "cors"}, allExist)
	if !errors.Is(err, ErrMiddlewareDuplicate) {
		t.Errorf("err = %v, want ErrMiddlewareDuplicate", err)
	}
}

func TestNormalizeChainRejectsUnknown(t *testing.T) {
	_, err := normalizeChain([]string{"nope"}, func(string) (bool, error) { return false, nil })
	if !errors.Is(err, ErrMiddlewareNotFound) {
		t.Errorf("err = %v, want ErrMiddlewareNotFound", err)
	}
}

func TestNormalizeChainRejectsBlank(t *testing.T) {
	if _, err := normalizeChain([]string{"cors", "   "}, allExist); !errors.Is(err, ErrMiddlewareRequired) {
		t.Errorf("err = %v, want ErrMiddlewareRequired", err)
	}
}

func TestNormalizeChainEmptyClearsTheChain(t *testing.T) {
	out, err := normalizeChain(nil, allExist)
	if err != nil || len(out) != 0 {
		t.Errorf("out = %v, err = %v", out, err)
	}
}
