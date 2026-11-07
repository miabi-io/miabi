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

// With a TTY the stream is raw (no stdcopy multiplexing).
type ExecStream interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(ctx context.Context, height, width uint) error
	Close() error
}

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

// Uses the daemon's docker top, so it works even when the image ships no ps binary.
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
