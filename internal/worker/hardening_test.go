// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"reflect"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// An app's hardening can only add to the profile's: neither side's restriction may be lost.
func TestAppHardeningTightensTheProfile(t *testing.T) {
	profile := Security{NoNewPrivileges: true, CapDrop: []string{"NET_RAW"}, Restricted: true}
	got := profile.withHardening(&models.Application{ReadOnlyRootFilesystem: true, DropCapabilities: []string{"NET_RAW", "SYS_CHROOT"}})
	if !got.NoNewPrivileges || !got.ReadOnlyRootfs || !reflect.DeepEqual(got.CapDrop, []string{"NET_RAW", "SYS_CHROOT"}) {
		t.Errorf("security = %+v", got)
	}
	if len(profile.CapDrop) != 1 {
		t.Errorf("the profile's drop list was modified: %v", profile.CapDrop)
	}
	if got := profile.withHardening(&models.Application{}); !got.NoNewPrivileges || got.ReadOnlyRootfs || len(got.CapDrop) != 1 {
		t.Errorf("an app asking for nothing loosened the profile: %+v", got)
	}
	if got := (Security{}).withHardening(&models.Application{NoNewPrivileges: true}); !got.NoNewPrivileges {
		t.Error("an app's no-new-privileges was dropped under the default profile")
	}
}
