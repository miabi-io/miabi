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

// Package telemetry sends one minimal, anonymous event per startup and per day so the project can count
// installations. It is fire-and-forget: a failure is logged at debug and never affects the platform.
package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi/client"
	"github.com/redis/go-redis/v9"
	"golang.org/x/mod/semver"
)

const Endpoint = "https://miabi.io/api/v1/telemetry/installations"

// SchemaVersion is the payload format the receiver validates against.
const SchemaVersion = 1

// Events a payload can carry.
const (
	EventStartup = "startup"
	EventDaily   = "daily"
)

// Platforms the control plane reports.
const (
	PlatformStandalone = "standalone"
	PlatformSwarm      = "swarm"
)

const (
	httpTimeout     = 5 * time.Second
	idPrefix        = "miabi-telemetry:v1:"
	startupGuardTTL = time.Hour
	startupGuardKey = "miabi:telemetry:startup:"
)

// Payload is everything a telemetry event contains.
type Payload struct {
	Schema         int    `json:"schema"`
	InstallationID string `json:"installation_id"`
	Event          string `json:"event"`
	Version        string `json:"version"`
	Edition        string `json:"edition"`
	Platform       string `json:"platform"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
}

// Options wires the service to the platform. The funcs are read at send time so a licence install or a
// swarm change is reflected without a restart.
type Options struct {
	URL       string
	Version   string
	InstallID string
	Enabled   bool
	Edition   func() string
	Platform  func() string
	Redis     redis.Cmdable
}

// Service builds and sends telemetry events.
type Service struct {
	opts   Options
	client *client.Client
}

// New builds the service.
func New(opts Options) *Service {
	if opts.URL == "" {
		opts.URL = Endpoint
	}
	return &Service{
		opts: opts,
		client: client.New(opts.URL,
			client.WithTimeout(httpTimeout),
			client.WithUserAgent("miabi/"+opts.Version),
		),
	}
}

// InstallationID derives the anonymous id sent for an Install ID.
func InstallationID(installID string) string {
	sum := sha256.Sum256([]byte(idPrefix + installID))
	return hex.EncodeToString(sum[:])
}

// URL is where events are sent.
func (s *Service) URL() string { return s.opts.URL }

// Enabled reports whether events are sent at all. Non-release builds never send so development and CI
// don't count as installations.
func (s *Service) Enabled() bool {
	return s.opts.Enabled && s.opts.InstallID != "" && IsRelease(s.opts.Version)
}

// IsRelease reports whether v is a semver release stamp rather than "dev" or a commit sha.
func IsRelease(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return semver.IsValid(v)
}

// Payload builds the event exactly as it would be sent.
func (s *Service) Payload(event string) Payload {
	p := Payload{
		Schema:         SchemaVersion,
		InstallationID: InstallationID(s.opts.InstallID),
		Event:          event,
		Version:        strings.TrimPrefix(strings.TrimSpace(s.opts.Version), "v"),
		Edition:        "community",
		Platform:       PlatformStandalone,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
	}
	if s.opts.Edition != nil {
		if e := s.opts.Edition(); e != "" {
			p.Edition = e
		}
	}
	if s.opts.Platform != nil {
		if pl := s.opts.Platform(); pl != "" {
			p.Platform = pl
		}
	}
	return p
}

// Send posts one event when enabled. The response is never interpreted beyond its status.
func (s *Service) Send(ctx context.Context, event string) error {
	if !s.Enabled() {
		return nil
	}
	resp, err := s.client.Post("").WithContext(ctx).JSONBody(s.Payload(event)).Do()
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("telemetry endpoint returned %d", resp.StatusCode)
	}
	return nil
}

// SendStartup sends the startup event after a random 1–5 minute delay, at most once per version per hour
// across every control plane sharing Redis. Run it in a goroutine; it returns when ctx is done or the send
// finished.
func (s *Service) SendStartup(ctx context.Context) {
	delay := time.Minute + rand.N(4*time.Minute)
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	if !s.Enabled() || !s.claimStartup(ctx) {
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	if err := s.Send(sendCtx, EventStartup); err != nil {
		logger.Debug("telemetry startup event not sent", "error", err)
	}
}

func (s *Service) claimStartup(ctx context.Context) bool {
	if s.opts.Redis == nil {
		return true
	}
	ok, err := s.opts.Redis.SetNX(ctx, startupGuardKey+s.opts.Version, "1", startupGuardTTL).Result()
	if err != nil {
		return true
	}
	return ok
}
