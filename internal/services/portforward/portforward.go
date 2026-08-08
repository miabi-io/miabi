// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package portforward gives on-demand external access to a managed database without publishing a host port.
// Opening a session binds an ephemeral TCP listener on the control plane; each accepted connection is
// bridged over the node's Docker API tunnel to an in-network socat relay. Sessions expire after a TTL.
package portforward

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/services/platformimage"
)

var (
	// ErrNotFound is returned when a session id is unknown.
	ErrNotFound = errors.New("forward session not found")
	// ErrNodeOffline is returned when the target node's agent is not connected.
	ErrNodeOffline = errors.New("node is offline")
)

// Resolver resolves the Docker client for a node id (0 = local). Satisfied by
// the nodes.Clients registry.
type Resolver interface {
	For(serverID uint) (docker.Client, error)
}

// Config tunes how forward listeners are bound and how relays are run.
type Config struct {
	BindAddr      string        // interface the listeners bind to (e.g. 127.0.0.1)
	AdvertiseHost string        // host shown to users (falls back to BindAddr)
	RelayImage    string        // socat image run as the in-network relay
	Network       string        // Docker network the relay attaches to (app network)
	TTL           time.Duration // session lifetime before it is reaped
}

// Target identifies the database an external client wants to reach.
type Target struct {
	InstanceID  uint
	WorkspaceID uint
	ServerID    uint   // node the instance runs on (0 = local)
	Host        string // in-network alias, e.g. mb-db-<token>-<id>
	Port        int
	Network     string // Docker network the instance is on (empty = configured default)
}

// Session is a live forward. The exported fields are returned to the API; the
// listener/cancel are internal.
type Session struct {
	ID          string    `json:"id"`
	InstanceID  uint      `json:"instance_id"`
	WorkspaceID uint      `json:"workspace_id"`
	Host        string    `json:"host"` // advertised host:port the client connects to
	Port        int       `json:"port"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`

	target  Target
	allowIP string // if non-empty, only this client IP may connect
	ln      net.Listener
	cancel  context.CancelFunc
	timer   *time.Timer
}

// Service manages forward sessions.
// ImageResolver resolves a deployment-config catalog key to an image ref.
type ImageResolver interface {
	Ref(key string) string
}

type Service struct {
	cfg     Config
	clients Resolver
	images  ImageResolver

	mu       sync.Mutex
	sessions map[string]*Session
}

func NewService(cfg Config, clients Resolver) *Service {
	if cfg.TTL <= 0 {
		cfg.TTL = 30 * time.Minute
	}
	if cfg.BindAddr == "" {
		cfg.BindAddr = "127.0.0.1"
	}
	return &Service{cfg: cfg, clients: clients, sessions: map[string]*Session{}}
}

// SetImageResolver wires the deployment-config resolver for the relay image.
func (s *Service) SetImageResolver(r ImageResolver) { s.images = r }

// relayImage returns the socat relay image: catalog override when set, else the
// configured/env default.
func (s *Service) relayImage() string {
	if s.images != nil {
		if r := s.images.Ref(platformimage.KeyRelay); r != "" {
			return r
		}
	}
	return s.cfg.RelayImage
}

// Open starts a forward to t and returns the live session. The image is pulled
// on the target node first (so per-connection relays start fast). allowIP, when
// the bind address is not loopback, restricts connections to that client IP.
func (s *Service) Open(ctx context.Context, t Target, allowIP string) (*Session, error) {
	dc, err := s.clients.For(t.ServerID)
	if err != nil {
		return nil, ErrNodeOffline
	}
	if err := dc.PullImage(ctx, s.relayImage(), nil); err != nil {
		return nil, fmt.Errorf("pull relay image: %w", err)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(s.cfg.BindAddr, "0"))
	if err != nil {
		return nil, fmt.Errorf("bind listener: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	host := s.cfg.AdvertiseHost
	if host == "" {
		host = s.cfg.BindAddr
	}

	now := time.Now()
	sessCtx, cancel := context.WithCancel(context.Background())
	sess := &Session{
		ID:          randID(),
		InstanceID:  t.InstanceID,
		WorkspaceID: t.WorkspaceID,
		Host:        host,
		Port:        port,
		CreatedAt:   now,
		ExpiresAt:   now.Add(s.cfg.TTL),
		target:      t,
		ln:          ln,
		cancel:      cancel,
	}
	// Only gate on source IP when bound to a routable interface; a loopback bind
	// is only reachable by local processes (whose IP won't match the API caller).
	if allowIP != "" && !bindIsLoopback(s.cfg.BindAddr) {
		sess.allowIP = allowIP
	}

	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()

	sess.timer = time.AfterFunc(s.cfg.TTL, func() { _ = s.Close(sess.WorkspaceID, sess.ID) })
	go s.accept(sessCtx, sess, dc)

	logger.Info("port-forward opened", "session", sess.ID, "instance", t.InstanceID,
		"node", t.ServerID, "listen", net.JoinHostPort(host, strconv.Itoa(port)))
	return sess, nil
}

func (s *Service) accept(ctx context.Context, sess *Session, dc docker.Client) {
	for {
		conn, err := sess.ln.Accept()
		if err != nil {
			return // listener closed (Close/expiry) or fatal accept error
		}
		if sess.allowIP != "" && remoteIP(conn) != sess.allowIP {
			logger.Warn("port-forward rejected connection", "session", sess.ID, "from", remoteIP(conn))
			_ = conn.Close()
			continue
		}
		go s.bridge(ctx, sess, dc, conn)
	}
}

func (s *Service) bridge(ctx context.Context, sess *Session, dc docker.Client, client net.Conn) {
	defer func() { _ = client.Close() }()
	// Reach the database on its own network (the workspace default); fall back to
	// the configured network for legacy instances with no network recorded.
	netName := sess.target.Network
	if netName == "" {
		netName = s.cfg.Network
	}
	relay, err := dc.DialNetwork(ctx, netName, s.relayImage(), sess.target.Host, sess.target.Port)
	if err != nil {
		logger.Error("port-forward relay dial failed", "session", sess.ID, "error", err)
		return
	}
	defer func() { _ = relay.Close() }()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(relay, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, relay); done <- struct{}{} }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// List returns the live sessions for a workspace, pruning any that have expired.
func (s *Service) List(workspaceID uint) []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Session, 0)
	for _, sess := range s.sessions {
		if sess.WorkspaceID == workspaceID {
			out = append(out, sess)
		}
	}
	return out
}

// Close tears down a session (idempotent). The workspace scope guards against
// closing another workspace's session.
func (s *Service) Close(workspaceID uint, id string) error {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok || sess.WorkspaceID != workspaceID {
		s.mu.Unlock()
		return ErrNotFound
	}
	delete(s.sessions, id)
	s.mu.Unlock()

	if sess.timer != nil {
		sess.timer.Stop()
	}
	sess.cancel()
	_ = sess.ln.Close()
	logger.Info("port-forward closed", "session", id, "instance", sess.InstanceID)
	return nil
}

// Shutdown closes every session (process shutdown).
func (s *Service) Shutdown() {
	s.mu.Lock()
	all := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		all = append(all, sess)
	}
	s.sessions = map[string]*Session{}
	s.mu.Unlock()
	for _, sess := range all {
		if sess.timer != nil {
			sess.timer.Stop()
		}
		sess.cancel()
		_ = sess.ln.Close()
	}
}

func remoteIP(conn net.Conn) string {
	if h, _, err := net.SplitHostPort(conn.RemoteAddr().String()); err == nil {
		return h
	}
	return ""
}

func bindIsLoopback(addr string) bool {
	ip := net.ParseIP(addr)
	return ip != nil && ip.IsLoopback()
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
