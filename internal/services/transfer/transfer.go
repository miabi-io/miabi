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

// Package transfer splices bytes between Docker engines through the control plane; nodes never talk directly.
package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
)

// Engines resolves a node's Docker client. Satisfied by *nodes.Clients.
type Engines interface {
	For(serverID uint) (docker.Client, error)
}

// Service runs transfers.
type Service struct {
	engines Engines
	// syncImage carries rsync; relayImage carries socat and is the same image database port-forward uses.
	syncImage, relayImage func() string
}

// The image funcs are read per transfer so a catalog override applies without a restart.
func New(engines Engines, syncImage, relayImage func() string) *Service {
	return &Service{engines: engines, syncImage: syncImage, relayImage: relayImage}
}

var ErrSameNode = errors.New("transfer: source and target are the same node")

// counted, when set, is updated live for progress reporting.
func splice(a, b io.ReadWriter, counted *atomic.Int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(countingWriter{w: b, n: counted}, a)
		closeWrite(b)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		closeWrite(a)
	}()
	wg.Wait()
}

type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if c.n != nil {
		c.n.Add(int64(n))
	}
	return n, err
}

func closeWrite(x any) {
	switch c := x.(type) {
	case interface{ CloseWrite() error }:
		_ = c.CloseWrite()
	case io.Closer:
		_ = c.Close()
	}
}

// tail trims a tool's output to its last lines, for an error message a person can read.
func tail(s string, lines int) string {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

func exitErr(what string, code int, err error, stderr string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if code != 0 {
		if msg := tail(stderr, 5); msg != "" {
			return fmt.Errorf("%s exited %d: %s", what, code, msg)
		}
		return fmt.Errorf("%s exited %d", what, code)
	}
	return nil
}

// Falls back to the local copy so an air-gapped node or a hand-loaded helper doesn't block a transfer.
func pull(ctx context.Context, dc docker.Client, ref string) error {
	err := dc.PullImage(ctx, ref, nil)
	if err == nil {
		return nil
	}
	if ok, xerr := dc.ImageExists(ctx, ref); xerr == nil && ok {
		return nil
	}
	return fmt.Errorf("pull %s: %w", ref, err)
}

// ticker calls fn every interval until stop is closed.
func ticker(interval time.Duration, stop <-chan struct{}, fn func()) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			fn()
		}
	}
}
