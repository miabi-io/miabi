// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"errors"
	"reflect"
	"testing"
)

// Any Linux capability may be dropped, not only the grantable ones: dropping takes privilege away.
func TestNormalizeDropCapabilities(t *testing.T) {
	got, err := NormalizeDropCapabilities([]string{" net_raw", "CAP_SYS_CHROOT", "NET_RAW", "SYS_BOOT"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"NET_RAW", "SYS_BOOT", "SYS_CHROOT"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, _ := NormalizeDropCapabilities([]string{"NET_RAW", "all"}); !reflect.DeepEqual(got, []string{CapabilityAll}) {
		t.Errorf("got %v, want ALL alone", got)
	}
	if _, err := NormalizeDropCapabilities([]string{"NET_RAWW"}); !errors.Is(err, ErrCapabilityNotLinux) {
		t.Errorf("a typo was accepted (err = %v)", err)
	}
}

func TestCheckCapabilityConflict(t *testing.T) {
	if err := CheckCapabilityConflict([]string{"NET_ADMIN"}, []string{"NET_ADMIN"}); !errors.Is(err, ErrCapabilityConflict) {
		t.Errorf("err = %v, want a conflict", err)
	}
	if err := CheckCapabilityConflict([]string{"NET_ADMIN"}, []string{CapabilityAll}); err != nil {
		t.Errorf("dropping ALL to keep only the grants was refused: %v", err)
	}
}
