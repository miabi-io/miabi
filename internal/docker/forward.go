// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
)

// DialNetwork bridges a raw TCP stream to host:port through an ephemeral socat
// relay container attached to the given network. See the Client interface.
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
	// AutoRemove so the relay self-deletes when socat exits, in addition to
	// explicit removal on Close. The two race harmlessly — removing a gone
	// container is ignored.
	created, err := e.cli.ContainerCreate(ctx, cfg, &container.HostConfig{AutoRemove: true}, netCfg, nil, name)
	if err != nil {
		return nil, fmt.Errorf("create relay: %w", err)
	}
	remove := func() {
		_ = e.cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	}

	// Attach before start so no early bytes are missed.
	hj, err := e.cli.ContainerAttach(ctx, created.ID, container.AttachOptions{
		Stream: true, Stdin: true, Stdout: true, Stderr: true,
	})
	if err != nil {
		remove()
		return nil, fmt.Errorf("attach relay: %w", err)
	}
	if err := e.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		hj.Close()
		remove()
		return nil, fmt.Errorf("start relay: %w", err)
	}

	c := &relayConn{hj: hj, remove: remove}
	pr, pw := io.Pipe()
	c.rd = pr
	// Demultiplex the attach stream: socat's payload arrives on stdout; stderr
	// (socat diagnostics) is discarded. StdCopy ends when the relay exits.
	go func() {
		_, err := stdcopy.StdCopy(pw, io.Discard, hj.Reader)
		_ = pw.CloseWithError(err)
	}()
	return c, nil
}

// relayConn is a net.Conn over a Docker attach stream: writes go to the relay's stdin (raw),
// reads come from its demultiplexed stdout.
type relayConn struct {
	hj     types.HijackedResponse
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

// Deadlines apply to the underlying attach connection where supported; the read
// side is buffered through a pipe, so a read deadline is best-effort.
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
