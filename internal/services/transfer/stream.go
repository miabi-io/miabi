// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package transfer

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
)

// StreamEnd is one side of a stream: a one-shot container on a node. The producer writes the data to
// stdout; the consumer reads it from stdin.
type StreamEnd struct {
	ServerID uint
	Spec     docker.RunSpec
}

// Stream pipes a producer's stdout into a consumer's stdin, with nothing written to disk in between: a
// database dump flows straight into its restore. It returns the bytes that crossed.
func (s *Service) Stream(ctx context.Context, producer, consumer StreamEnd, progress func(int64)) (int64, error) {
	pdc, err := s.engines.For(producer.ServerID)
	if err != nil {
		return 0, fmt.Errorf("source node: %w", err)
	}
	cdc, err := s.engines.For(consumer.ServerID)
	if err != nil {
		return 0, fmt.Errorf("target node: %w", err)
	}
	if err := pull(ctx, pdc, producer.Spec.Image); err != nil {
		return 0, err
	}
	if err := pull(ctx, cdc, consumer.Spec.Image); err != nil {
		return 0, err
	}
	_ = pdc.RemoveContainer(ctx, producer.Spec.Name, true)
	_ = cdc.RemoveContainer(ctx, consumer.Spec.Name, true)

	// The consumer starts first, so it is reading by the time the first byte arrives.
	in, err := cdc.RunAttached(ctx, consumer.Spec)
	if err != nil {
		return 0, fmt.Errorf("start restore: %w", err)
	}
	defer in.Close()
	out, err := pdc.RunAttached(ctx, producer.Spec)
	if err != nil {
		return 0, fmt.Errorf("start dump: %w", err)
	}
	defer out.Close()
	_ = out.CloseWrite()

	// Whatever the consumer prints must be drained, or a chatty client blocks on a full pipe.
	go func() { _, _ = io.Copy(io.Discard, in) }()

	var n atomic.Int64
	stop := make(chan struct{})
	if progress != nil {
		go ticker(3*time.Second, stop, func() { progress(n.Load()) })
	}
	_, copyErr := io.Copy(countingWriter{w: in, n: &n}, out)
	close(stop)
	_ = in.CloseWrite()

	pcode, perr := out.Wait(ctx)
	if err := exitErr("dump", pcode, perr, out.Stderr()); err != nil {
		return n.Load(), err
	}
	ccode, cerr := in.Wait(ctx)
	if err := exitErr("restore", ccode, cerr, in.Stderr()); err != nil {
		return n.Load(), err
	}
	if copyErr != nil {
		return n.Load(), fmt.Errorf("stream: %w", copyErr)
	}
	if progress != nil {
		progress(n.Load())
	}
	return n.Load(), nil
}
