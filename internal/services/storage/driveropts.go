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
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// Errors a rejected shared-storage option set returns.
var (
	// The local driver takes only type/o/device; anything else is ignored or reaches unreviewed mount features.
	ErrDriverOptKey = errors.New("shared volumes accept only the 'device' and 'o' driver options")
	// The kernel ignores fs type under MS_BIND, so o=bind,device=/ mounts the host root (verified 2026-09-18).
	ErrDriverMountFlag = errors.New("shared volumes may not set mount flags such as bind, remount or a propagation mode")
	// ErrDriverOptValue is returned for an unknown or malformed entry in the `o` list.
	ErrDriverOptValue = errors.New("unsupported mount option for this share type")
	// ErrDriverDeviceForm is returned when `device` is not a remote export or share.
	ErrDriverDeviceForm = errors.New("device must be an NFS export (host:/export) or a CIFS share (//host/share), never a local path")
)

// Bind variants are the escape itself; remount/move/propagation are levers on the host mount table.
var mountFlags = map[string]bool{
	"bind": true, "rbind": true, "remount": true, "move": true,
	"shared": true, "rshared": true, "slave": true, "rslave": true,
	"private": true, "rprivate": true, "unbindable": true, "runbindable": true,
	"loop": true, "dirsync": true, "mand": true, "silent": true,
}

// The list is short on purpose and grows only by review.
var nfsOptions = map[string]bool{
	"addr": true, "nfsvers": true, "vers": true, "proto": true, "port": true, "mountport": true,
	"rsize": true, "wsize": true, "timeo": true, "retrans": true, "actimeo": true,
	"hard": true, "soft": true, "intr": true, "nointr": true, "ac": true, "noac": true,
	"rw": true, "ro": true, "sync": true, "async": true, "noatime": true, "atime": true,
	"nodiratime": true, "lookupcache": true, "local_lock": true, "sec": true, "namlen": true,
	"nolock": true, "lock": true, "bg": true, "fg": true, "resvport": true, "noresvport": true,
	"nconnect": true, "clientaddr": true,
}

// cifsOptions are the CIFS/SMB mount options a tenant may set.
var cifsOptions = map[string]bool{
	"username": true, "user": true, "password": true, "pass": true, "domain": true, "dom": true,
	"workgroup": true, "vers": true, "sec": true, "addr": true, "ip": true, "port": true,
	"uid": true, "gid": true, "forceuid": true, "forcegid": true,
	"file_mode": true, "dir_mode": true, "iocharset": true, "nounix": true, "noserverino": true,
	"rw": true, "ro": true, "soft": true, "hard": true, "actimeo": true, "cache": true,
	"seal": true, "noperm": true, "mfsymlinks": true, "serverino": true, "rsize": true, "wsize": true,
	"echo_interval": true, "handletimeout": true, "credentials": false, // credentials names a FILE the host reads
}

var (
	// nfsDevice is host:/export — an IP or hostname, then an absolute export path.
	nfsDevice = regexp.MustCompile(`^[A-Za-z0-9._:\[\]-]+:/[^\s]*$`)
	// cifsDevice is //host/share (or the backslash spelling).
	cifsDevice = regexp.MustCompile(`^(//|\\\\)[A-Za-z0-9._:\[\]-]+[/\\][^\s]+$`)
)

func validateSharedDriverOpts(driver string, in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	var device, o string
	for k, v := range in {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "device":
			device = strings.TrimSpace(v)
		case "o":
			o = strings.TrimSpace(v)
		case "type":
		default:
			return nil, fmt.Errorf("%w: %q", ErrDriverOptKey, k)
		}
	}
	if device == "" {
		return nil, ErrDriverDeviceRequired
	}
	if err := validateShareDevice(driver, device); err != nil {
		return nil, err
	}
	if o != "" {
		clean, err := validateMountOptions(driver, o)
		if err != nil {
			return nil, err
		}
		out["o"] = clean
	}
	out["device"] = device
	// Docker's local driver backs NFS/CIFS through the type option; Miabi sets it, never the caller.
	out["type"] = driver
	return out, nil
}

// A local absolute path is the escape payload, so it's refused by shape rather than by blocklist.
func validateShareDevice(driver, device string) error {
	switch driver {
	case "nfs":
		if strings.HasPrefix(device, ":/") {
			return nil
		}
		if !nfsDevice.MatchString(device) {
			return fmt.Errorf("%w: %q is not host:/export", ErrDriverDeviceForm, device)
		}
	case "cifs":
		if !cifsDevice.MatchString(device) {
			return fmt.Errorf("%w: %q is not //host/share", ErrDriverDeviceForm, device)
		}
	default:
		return ErrInvalidDriver
	}
	return nil
}

func validateMountOptions(driver, o string) (string, error) {
	allowed := nfsOptions
	if driver == "cifs" {
		allowed = cifsOptions
	}
	parts := strings.Split(o, ",")
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		name := p
		if eq := strings.IndexByte(p, '='); eq >= 0 {
			name = p[:eq]
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if mountFlags[name] {
			return "", fmt.Errorf("%w: %q", ErrDriverMountFlag, name)
		}
		if ok, known := allowed[name]; !known || !ok {
			return "", fmt.Errorf("%w: %q", ErrDriverOptValue, name)
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, ","), nil
}

// A volume created before the allow-list existed must not mount just because its row survived.
func SharedDriverOptsValid(opts map[string]string) error {
	driver := strings.ToLower(strings.TrimSpace(opts["type"]))
	if driver != "nfs" && driver != "cifs" {
		return fmt.Errorf("%w: %q", ErrInvalidDriver, driver)
	}
	_, err := validateSharedDriverOpts(driver, opts)
	return err
}

// Operators should learn which pre-allow-list volumes need recreating from a boot log, not a failed deploy.
func (s *Service) AuditSharedVolumes() {
	vols, err := s.repo.ListAll()
	if err != nil {
		logger.Warn("shared volume audit: list failed", "error", err)
		return
	}
	var bad int
	for i := range vols {
		v := vols[i]
		if v.Driver != models.VolumeDriverNFS && v.Driver != models.VolumeDriverCIFS {
			continue
		}
		if strings.TrimSpace(v.DriverOptsEnc) == "" {
			continue
		}
		raw, derr := crypto.Decrypt(v.DriverOptsEnc)
		if derr != nil {
			continue
		}
		opts := map[string]string{}
		if json.Unmarshal([]byte(raw), &opts) != nil {
			continue
		}
		if err := SharedDriverOptsValid(opts); err != nil {
			bad++
			// The options themselves are not logged: they may carry a CIFS password.
			logger.Error("shared volume has unsafe driver options and will not mount; recreate it",
				"volume", v.ID, "name", v.Name, "workspace", v.WorkspaceID, "reason", err)
		}
	}
	if bad > 0 {
		logger.Error("shared volume audit found volumes that can no longer mount", "count", bad)
	}
}
