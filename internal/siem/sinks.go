// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package siem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

// eventJSON renders one audit event as a compact JSON object — the wire form for
// the webhook sink and the syslog "json" format.
func eventJSON(e *models.AuditLog) ([]byte, error) {
	return json.Marshal(map[string]any{
		"id":           e.ID,
		"timestamp":    e.CreatedAt.UTC().Format(time.RFC3339),
		"actor_id":     e.ActorID,
		"workspace_id": e.WorkspaceID,
		"action":       e.Action,
		"target_type":  e.TargetType,
		"target_id":    e.TargetID,
		"ip":           e.IPAddress,
		"metadata":     e.Metadata,
	})
}

type webhookSink struct {
	url    string
	auth   string
	client *http.Client
}

func newWebhookSink(url, auth string) *webhookSink {
	return &webhookSink{url: url, auth: auth, client: &http.Client{Timeout: 15 * time.Second}}
}

func (s *webhookSink) Ship(events []models.AuditLog) error {
	if strings.TrimSpace(s.url) == "" {
		return fmt.Errorf("webhook endpoint is empty")
	}
	var buf bytes.Buffer
	for i := range events {
		line, err := eventJSON(&events[i])
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	req, err := http.NewRequest(http.MethodPost, s.url, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	if s.auth != "" {
		req.Header.Set("Authorization", s.auth)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}

type syslogSink struct {
	network string // "tcp" | "udp"
	addr    string
	cef     bool
}

// newSyslogSink parses an endpoint like "tcp://host:514", "udp://host:514", or
// "host:514" (defaults to udp).
func newSyslogSink(cfg *models.SIEMConfig) (*syslogSink, error) {
	network, addr := "udp", strings.TrimSpace(cfg.Endpoint)
	if addr == "" {
		return nil, fmt.Errorf("syslog endpoint is empty")
	}
	if i := strings.Index(addr, "://"); i >= 0 {
		network, addr = addr[:i], addr[i+3:]
	}
	if network != "tcp" && network != "udp" {
		return nil, fmt.Errorf("syslog endpoint must be tcp:// or udp://")
	}
	return &syslogSink{network: network, addr: addr, cef: cfg.Format == models.SIEMFormatCEF}, nil
}

// priority = facility(local0=16)*8 + severity(info=6).
const syslogPriority = 16*8 + 6

func (s *syslogSink) Ship(events []models.AuditLog) error {
	conn, err := net.DialTimeout(s.network, s.addr, 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	host, _, _ := net.SplitHostPort(s.addr)
	for i := range events {
		msg, err := s.format(&events[i])
		if err != nil {
			return err
		}
		// RFC 5424: <pri>1 TIMESTAMP HOST APP PROCID MSGID STRUCTURED-DATA MSG
		line := fmt.Sprintf("<%d>1 %s %s miabi - %d - %s\n",
			syslogPriority, events[i].CreatedAt.UTC().Format(time.RFC3339), nonEmpty(host, "-"), events[i].ID, msg)
		if _, err := conn.Write([]byte(line)); err != nil {
			return err
		}
	}
	return nil
}

func (s *syslogSink) format(e *models.AuditLog) (string, error) {
	if s.cef {
		// CEF:0|Vendor|Product|Version|SignatureID|Name|Severity|Extension
		return fmt.Sprintf("CEF:0|Miabi|Miabi|1|%s|%s|3|act=%s suser=%s dvchost=%s",
			e.Action, e.Action, e.Action, actorStr(e.ActorID), e.IPAddress), nil
	}
	b, err := eventJSON(e)
	return string(b), err
}

func actorStr(p *uint) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *p)
}

func nonEmpty(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
