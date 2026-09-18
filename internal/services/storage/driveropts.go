// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
	// ErrDriverOptKey is returned for a driver option outside type/o/device. The local driver takes
	// only those three; anything else is either ignored or a way to reach a mount feature Miabi has
	// not reviewed.
	ErrDriverOptKey = errors.New("shared volumes accept only the 'device' and 'o' driver options")
	// ErrDriverMountFlag is returned when the `o` list carries a mount FLAG rather than a data
	// option. This is the one that matters: the kernel ignores the filesystem type when MS_BIND is
	// set, so `o=bind` with `device=/` bind-mounts the host's root filesystem into the volume
	// whatever `type` says. Verified against a live daemon, 2026-09-18.
	ErrDriverMountFlag = errors.New("shared volumes may not set mount flags such as bind, remount or a propagation mode")
	// ErrDriverOptValue is returned for an unknown or malformed entry in the `o` list.
	ErrDriverOptValue = errors.New("unsupported mount option for this share type")
	// ErrDriverDeviceForm is returned when `device` is not a remote export or share.
	ErrDriverDeviceForm = errors.New("device must be an NFS export (host:/export) or a CIFS share (//host/share), never a local path")
)

// mountFlags are the `o` entries that change HOW the kernel mounts rather than what the filesystem
// does with the data. Every one of them is refused: bind and its variants are the escape itself, and
// the rest (remount, move, propagation) are levers on the host's mount table that a tenant has no
// business pulling.
var mountFlags = map[string]bool{
	"bind": true, "rbind": true, "remount": true, "move": true,
	"shared": true, "rshared": true, "slave": true, "rslave": true,
	"private": true, "rprivate": true, "unbindable": true, "runbindable": true,
	"loop": true, "dirsync": true, "mand": true, "silent": true,
}

// nfsOptions are the NFS mount options a tenant may set. Anything absent is refused rather than
// passed through: the list is short on purpose, and grows only by review.
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

// validateShareDevice requires a remote export or share. A local absolute path is the payload of the
// escape, so it is refused by shape rather than by blocklist — there is no local path that belongs
// here even without the bind flag.
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

// validateMountOptions parses the comma-separated `o` list, refusing mount flags and any data option
// outside the driver's allow-list.
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

// SharedDriverOptsValid re-checks a stored option set, for the deploy path and the boot audit. A
// volume created before the allow-list existed must not be able to mount just because its row
// survived.
func SharedDriverOptsValid(opts map[string]string) error {
	driver := strings.ToLower(strings.TrimSpace(opts["type"]))
	if driver != "nfs" && driver != "cifs" {
		return fmt.Errorf("%w: %q", ErrInvalidDriver, driver)
	}
	_, err := validateSharedDriverOpts(driver, opts)
	return err
}

// AuditSharedVolumes re-checks every stored shared volume against the allow-list and reports the
// ones that would be refused. Volumes created before the allow-list existed keep their options
// encrypted on the row; the deploy path already refuses to mount them, but an operator should learn
// which ones need recreating from a log line at boot, not from a deploy that quietly lost its data.
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
