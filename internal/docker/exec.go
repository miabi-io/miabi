// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"strings"

	"github.com/moby/moby/client"
)

// ExecOptions configures an interactive command run inside a running container.
type ExecOptions struct {
	Cmd []string // command + args; e.g. ["/bin/sh"]
	Tty bool     // allocate a pseudo-TTY (raw, un-multiplexed stream)
	Env []string // extra environment, "KEY=VALUE"
}

// ExecStream is a bidirectional stream attached to a running exec instance. With a TTY the byte
// stream is raw (no stdcopy multiplexing), so Read/Write move terminal bytes directly. Resize
// adjusts the pseudo-terminal; Close ends the session.
type ExecStream interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(ctx context.Context, height, width uint) error
	Close() error
}

// Exec creates and attaches to an interactive command inside a running
// container, returning a bidirectional stream. Callers must Close the stream.
func (e *engineClient) Exec(ctx context.Context, containerID string, opts ExecOptions) (ExecStream, error) {
	created, err := e.cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		Cmd:          opts.Cmd,
		Env:          opts.Env,
		TTY:          opts.Tty,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, wrapNotFound(err)
	}
	att, err := e.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: opts.Tty})
	if err != nil {
		return nil, wrapNotFound(err)
	}
	return &execStream{cli: e.cli, id: created.ID, hj: att.HijackedResponse}, nil
}

// Top lists the running processes in a container via the daemon's "docker top"
// (the host runs ps against the container's PIDs), so it works regardless of
// whether the image ships a ps binary.
func (e *engineClient) Top(ctx context.Context, containerID, psArgs string) (ProcessList, error) {
	var args []string
	if f := strings.Fields(psArgs); len(f) > 0 {
		args = f
	}
	body, err := e.cli.ContainerTop(ctx, containerID, client.ContainerTopOptions{Arguments: args})
	if err != nil {
		return ProcessList{}, wrapNotFound(err)
	}
	return ProcessList{Titles: body.Titles, Processes: body.Processes}, nil
}

// execStream wraps a hijacked exec attach connection. With a TTY the stream is raw:
// stdout/stderr arrive interleaved on hj.Reader and stdin goes to hj.Conn.
type execStream struct {
	cli *client.Client
	id  string
	hj  client.HijackedResponse
}

func (s *execStream) Read(p []byte) (int, error)  { return s.hj.Reader.Read(p) }
func (s *execStream) Write(p []byte) (int, error) { return s.hj.Conn.Write(p) }

func (s *execStream) Resize(ctx context.Context, height, width uint) error {
	_, err := s.cli.ExecResize(ctx, s.id, client.ExecResizeOptions{Height: height, Width: width})
	return err
}

func (s *execStream) Close() error {
	s.hj.Close()
	return nil
}
