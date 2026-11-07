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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

func (e *engineClient) DialNetwork(ctx context.Context, netName, image, host string, port int) (net.Conn, error) {
	cfg := &container.Config{
		Image: image,
		// image's entrypoint is `socat`; bridge stdin/stdout to the target TCP.
		Cmd:          []string{"STDIO", fmt.Sprintf("TCP:%s:%d", host, port)},
		OpenStdin:    true,
		StdinOnce:    true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false, // keep stdout/stderr framed so the byte stream stays clean
		Labels:       map[string]string{ManagedLabel: "true"},
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{netName: {}},
	}
	name := fmt.Sprintf("mb-fwd-%s", randHex(6))
	// AutoRemove and explicit removal on Close race harmlessly; removing a gone container is ignored.
	created, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       &container.HostConfig{AutoRemove: true},
		NetworkingConfig: netCfg,
		Name:             name,
	})
	if err != nil {
		return nil, fmt.Errorf("create relay: %w", err)
	}
	remove := func() {
		_, _ = e.cli.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	}

	// Attach before start so no early bytes are missed.
	att, err := e.cli.ContainerAttach(ctx, created.ID, client.ContainerAttachOptions{
		Stream: true, Stdin: true, Stdout: true, Stderr: true,
	})
	if err != nil {
		remove()
		return nil, fmt.Errorf("attach relay: %w", err)
	}
	hj := att.HijackedResponse
	if _, err := e.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		hj.Close()
		remove()
		return nil, fmt.Errorf("start relay: %w", err)
	}

	c := &relayConn{hj: hj, remove: remove}
	pr, pw := io.Pipe()
	c.rd = pr
	go func() {
		_, err := stdcopy.StdCopy(pw, io.Discard, hj.Reader)
		_ = pw.CloseWithError(err)
	}()
	return c, nil
}

type relayConn struct {
	hj     client.HijackedResponse
	rd     *io.PipeReader
	remove func()
	once   sync.Once
}

func (c *relayConn) Read(p []byte) (int, error)  { return c.rd.Read(p) }
func (c *relayConn) Write(p []byte) (int, error) { return c.hj.Conn.Write(p) }

func (c *relayConn) Close() error {
	c.once.Do(func() {
		_ = c.rd.Close()
		c.hj.Close()
		c.remove()
	})
	return nil
}

func (c *relayConn) LocalAddr() net.Addr  { return relayAddr{} }
func (c *relayConn) RemoteAddr() net.Addr { return relayAddr{} }

// The read side is buffered through a pipe, so a read deadline is best-effort.
func (c *relayConn) SetDeadline(t time.Time) error      { return c.hj.Conn.SetDeadline(t) }
func (c *relayConn) SetReadDeadline(t time.Time) error  { return c.hj.Conn.SetReadDeadline(t) }
func (c *relayConn) SetWriteDeadline(t time.Time) error { return c.hj.Conn.SetWriteDeadline(t) }

type relayAddr struct{}

func (relayAddr) Network() string { return "docker-relay" }
func (relayAddr) String() string  { return "docker-relay" }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
