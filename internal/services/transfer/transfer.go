// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package transfer moves bytes between two Docker engines through the control plane: a volume with rsync,
// or a database dump piped straight into a restore. Nodes never talk to each other, so every stream is
// spliced here, over the connections the control plane already holds (the local socket or an agent
// tunnel). Nothing is encrypted beyond what that transport provides, by design.
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

// New builds a transfer service. The image funcs are read per transfer, so an admin override of the
// catalog applies to the next copy without a restart.
func New(engines Engines, syncImage, relayImage func() string) *Service {
	return &Service{engines: engines, syncImage: syncImage, relayImage: relayImage}
}

// ErrSameNode refuses a copy whose ends are one engine: there is nothing to move, and the target volume
// would be the source.
var ErrSameNode = errors.New("transfer: source and target are the same node")

// splice copies both ways between a and b until either side ends, and returns the bytes that flowed from a
// to b. counted, when set, is updated live so a caller can report progress while the copy runs.
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

// pull refreshes an image, and settles for the local copy when the registry cannot be reached: an air-gapped
// node, or a helper loaded by hand, must not stop a transfer the image is already there for.
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
