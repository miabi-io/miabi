// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Attached is a container whose stdio is wired to the caller: writes go to its stdin, reads come from its
// demultiplexed stdout, and stderr is kept (bounded) for error messages.
type Attached interface {
	io.ReadWriter
	// CloseWrite ends the container's stdin, which is how a consumer like pg_restore learns the input is done.
	CloseWrite() error
	// Wait blocks until the container exits and returns its exit code.
	Wait(ctx context.Context) (int, error)
	// Stderr is the tail of what the container wrote to stderr.
	Stderr() string
	// Close detaches and removes the container.
	Close() error
}

// stderrLimit caps the stderr kept per attached container; a noisy tool must not grow it without bound.
const stderrLimit = 16 << 10

// RunAttached creates a container with stdin open, attaches to it before starting it so no early bytes are
// lost, and returns its stdio as a stream. Unlike RunOneShot nothing is buffered, so gigabytes can flow
// through it. The container is not auto-removed: Wait must be able to read its exit code.
func (e *engineClient) RunAttached(ctx context.Context, spec RunSpec) (Attached, error) {
	id, err := e.createOneShot(ctx, spec, func(c *container.Config) {
		c.OpenStdin = true
		c.StdinOnce = true
		c.AttachStdin = true
		c.AttachStdout = true
		c.AttachStderr = true
		c.Tty = false
	})
	if err != nil {
		return nil, err
	}
	remove := func() {
		_, _ = e.cli.ContainerRemove(context.Background(), id, client.ContainerRemoveOptions{Force: true})
	}
	att, err := e.cli.ContainerAttach(ctx, id, client.ContainerAttachOptions{
		Stream: true, Stdin: true, Stdout: true, Stderr: true,
	})
	if err != nil {
		remove()
		return nil, fmt.Errorf("attach: %w", err)
	}
	hj := att.HijackedResponse
	// Registered before start, or a container that exits at once could be missed.
	wait := e.cli.ContainerWait(context.Background(), id, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := e.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		hj.Close()
		remove()
		return nil, fmt.Errorf("start: %w", err)
	}
	a := &attached{hj: hj, remove: remove, wait: wait}
	pr, pw := io.Pipe()
	a.rd = pr
	go func() {
		_, err := stdcopy.StdCopy(pw, &a.stderr, hj.Reader)
		_ = pw.CloseWithError(err)
	}()
	return a, nil
}

type attached struct {
	hj     client.HijackedResponse
	rd     *io.PipeReader
	remove func()
	wait   client.ContainerWaitResult
	stderr boundedBuffer
	once   sync.Once
}

func (a *attached) Read(p []byte) (int, error)  { return a.rd.Read(p) }
func (a *attached) Write(p []byte) (int, error) { return a.hj.Conn.Write(p) }
func (a *attached) CloseWrite() error           { return a.hj.CloseWrite() }
func (a *attached) Stderr() string              { return a.stderr.String() }

func (a *attached) Wait(ctx context.Context) (int, error) {
	select {
	case err := <-a.wait.Error:
		return -1, err
	case st := <-a.wait.Result:
		if st.Error != nil && st.Error.Message != "" {
			return int(st.StatusCode), fmt.Errorf("%s", st.Error.Message)
		}
		return int(st.StatusCode), nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

func (a *attached) Close() error {
	a.once.Do(func() {
		_ = a.rd.Close()
		a.hj.Close()
		a.remove()
	})
	return nil
}

// boundedBuffer keeps the last stderrLimit bytes written to it.
type boundedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Write(p)
	if over := b.buf.Len() - stderrLimit; over > 0 {
		b.buf.Next(over)
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
