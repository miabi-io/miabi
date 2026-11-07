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

package logstore

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Every process that reads or writes it must mount the same directory.
type fsBackend struct {
	root string
}

// NewFSBackend roots a filesystem backend at dir, creating it if needed.
func NewFSBackend(dir string) (Backend, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &fsBackend{root: dir}, nil
}

func (b *fsBackend) Name() string { return "filesystem" }

// Refs are built internally, but the store stays hermetic.
func (b *fsBackend) path(ref string) (string, bool) {
	ref = strings.ReplaceAll(ref, "\\", "/")
	for _, seg := range strings.Split(ref, "/") {
		if seg == ".." {
			return "", false
		}
	}
	clean := filepath.Clean("/" + ref)
	p := filepath.Join(b.root, clean)
	if p != b.root && !strings.HasPrefix(p, b.root+string(os.PathSeparator)) {
		return "", false
	}
	return p, true
}

func (b *fsBackend) Create(ref string) (io.WriteCloser, error) {
	p, ok := b.path(ref)
	if !ok {
		return nil, os.ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return nil, err
	}
	return os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
}

func (b *fsBackend) Open(ref string) (io.ReadCloser, error) {
	p, ok := b.path(ref)
	if !ok {
		return nil, os.ErrInvalid
	}
	f, err := os.Open(p)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	return f, err
}

func (b *fsBackend) Delete(ref string) error {
	p, ok := b.path(ref)
	if !ok {
		return os.ErrInvalid
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Best-effort prune of now-empty parent directories, back up to the root.
	for dir := filepath.Dir(p); dir != b.root && strings.HasPrefix(dir, b.root); dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			break // non-empty or gone: stop climbing
		}
	}
	return nil
}

// Sweep removes .log files last modified before cutoff and returns the count.
func (b *fsBackend) Sweep(cutoff time.Time) (int, error) {
	var removed int
	err := filepath.WalkDir(b.root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, keep sweeping
		}
		if d.IsDir() || !strings.HasSuffix(p, ".log") {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			if rerr := os.Remove(p); rerr == nil {
				removed++
			}
		}
		return nil
	})
	return removed, err
}
