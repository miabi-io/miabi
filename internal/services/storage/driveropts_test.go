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

package storage

import (
	"errors"
	"testing"
)

// Reproduced live 2026-09-18: type=nfs,o=bind,device=/ bind-mounted the host root filesystem.
func TestValidateSharedDriverOptsRefusesTheHostEscape(t *testing.T) {
	cases := []struct {
		name    string
		driver  string
		opts    map[string]string
		wantErr error
	}{
		// The device check runs first; the flag cases below prove the second guard on its own.
		{"host root via bind", "nfs", map[string]string{"device": "/", "o": "bind"}, ErrDriverDeviceForm},
		{"host etc via bind", "nfs", map[string]string{"device": "/etc", "o": "bind"}, ErrDriverDeviceForm},
		{"rbind", "nfs", map[string]string{"device": "/", "o": "rbind"}, ErrDriverDeviceForm},
		{"bind hidden in a list", "nfs", map[string]string{"device": "10.0.0.5:/e", "o": "rw,bind,soft"}, ErrDriverMountFlag},
		{"bind in mixed case", "nfs", map[string]string{"device": "10.0.0.5:/e", "o": "rw,BIND"}, ErrDriverMountFlag},
		{"propagation flag", "nfs", map[string]string{"device": "10.0.0.5:/e", "o": "rshared"}, ErrDriverMountFlag},
		{"remount", "nfs", map[string]string{"device": "10.0.0.5:/e", "o": "remount"}, ErrDriverMountFlag},

		// Even without a flag, a local path is never a share.
		{"local path, no flag", "nfs", map[string]string{"device": "/", "o": "rw"}, ErrDriverDeviceForm},
		{"local path cifs", "cifs", map[string]string{"device": "/etc", "o": "rw"}, ErrDriverDeviceForm},
		{"relative path", "nfs", map[string]string{"device": "../../etc"}, ErrDriverDeviceForm},

		// Keys outside type/o/device reach mount features nobody reviewed.
		{"unknown key", "nfs", map[string]string{"device": "10.0.0.5:/e", "mountpoint": "/host"}, ErrDriverOptKey},
		// credentials= names a file the HOST reads.
		{"cifs credentials file", "cifs", map[string]string{"device": "//10.0.0.5/s", "o": "credentials=/etc/shadow"}, ErrDriverOptValue},
		{"unknown mount option", "nfs", map[string]string{"device": "10.0.0.5:/e", "o": "nosuchoption"}, ErrDriverOptValue},

		{"no device", "nfs", map[string]string{"o": "rw"}, ErrDriverDeviceRequired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := validateSharedDriverOpts(c.driver, c.opts)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("got %v (%v), want %v", err, got, c.wantErr)
			}
		})
	}
}

func TestValidateSharedDriverOptsAcceptsRealShares(t *testing.T) {
	cases := []struct {
		name   string
		driver string
		opts   map[string]string
	}{
		{"nfs with addr", "nfs", map[string]string{"device": ":/exports/app", "o": "addr=10.0.0.5,rw,vers=4"}},
		{"nfs host export", "nfs", map[string]string{"device": "10.0.0.5:/exports/app", "o": "rw,hard,nconnect=4"}},
		{"nfs hostname", "nfs", map[string]string{"device": "nas.example.com:/vol1", "o": "ro"}},
		{"cifs share", "cifs", map[string]string{"device": "//10.0.0.5/share", "o": "username=u,password=p,vers=3.0"}},
		{"no options at all", "nfs", map[string]string{"device": "10.0.0.5:/e"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := validateSharedDriverOpts(c.driver, c.opts)
			if err != nil {
				t.Fatalf("rejected a legitimate share: %v", err)
			}
			if out["type"] != c.driver {
				t.Fatalf("type = %q, want %q — Miabi sets it, never the caller", out["type"], c.driver)
			}
		})
	}
}

// A hand-supplied type must never decide how the volume is mounted.
func TestValidateSharedDriverOptsForcesType(t *testing.T) {
	out, err := validateSharedDriverOpts("nfs", map[string]string{"device": "10.0.0.5:/e", "type": "cifs"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["type"] != "nfs" {
		t.Fatalf("type = %q, want nfs", out["type"])
	}
}

func TestSharedDriverOptsValidRechecksStoredRows(t *testing.T) {
	if err := SharedDriverOptsValid(map[string]string{"type": "nfs", "device": "/", "o": "bind"}); err == nil {
		t.Fatal("a stored host-escape volume must be refused at deploy")
	}
	if err := SharedDriverOptsValid(map[string]string{"type": "nfs", "device": "10.0.0.5:/e", "o": "rw"}); err != nil {
		t.Fatalf("a legitimate stored volume must still mount: %v", err)
	}
	if err := SharedDriverOptsValid(map[string]string{"device": "10.0.0.5:/e"}); err == nil {
		t.Fatal("options with no type are not a shared volume")
	}
}
