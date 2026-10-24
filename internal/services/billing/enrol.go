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

package billing

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// CodePrefix starts a console enrolment code: mce1. + base64url(no padding) of {"u","i","t"}.
const CodePrefix = "mce1."

// enrollTimeout bounds the call to the console.
const enrollTimeout = 15 * time.Second

// Setting keys of the console connection. The "billing." prefix is reserved: only the admin
// Billing page writes them.
const (
	SettingConsoleURL            = "billing.console_url"
	SettingConsoleInstanceID     = "billing.console_instance_id"
	SettingNotifyURL             = "billing.notify_url"
	SettingNotifySecret          = "billing.notify_secret"
	SettingPortalMode            = "billing.portal_mode"
	SettingPortalSessionEndpoint = "billing.portal_session_endpoint"
	SettingPortalSigningSecret   = "billing.portal_signing_secret"
	SettingPortalAllowedHosts    = "billing.portal_allowed_hosts"
	SettingAPIKeyID              = "billing.api_key_id"
	SettingConnectedAt           = "billing.connected_at"
	SettingConnectedBy           = "billing.connected_by"
)

var connectionKeys = []string{SettingConsoleURL, SettingConsoleInstanceID, SettingNotifyURL, SettingNotifySecret, SettingPortalMode,
	SettingPortalSessionEndpoint, SettingPortalSigningSecret, SettingPortalAllowedHosts, SettingAPIKeyID, SettingConnectedAt, SettingConnectedBy}

// CodedError is a refusal with a stable code for the API envelope.
type CodedError struct {
	Status  int
	code    string
	Message string
}

func (e *CodedError) Error() string { return e.Message }

// Code is the stable machine code.
func (e *CodedError) Code() string { return e.code }

func coded(status int, code, msg string) *CodedError {
	return &CodedError{Status: status, code: code, Message: msg}
}

// NewCodedError builds a refusal with a stable code.
func NewCodedError(status int, code, msg string) *CodedError { return coded(status, code, msg) }

// Enrolment errors.
var (
	ErrMalformedCode    = coded(http.StatusBadRequest, "ENROLL_CODE_INVALID", "this is not a Miabi Console enrolment code (mce1.…)")
	ErrConsoleURL       = coded(http.StatusBadRequest, "ENROLL_CODE_INVALID", "the console address in the code must be https (http only for localhost)")
	ErrAlreadyConnected = coded(http.StatusConflict, "BILLING_ALREADY_CONNECTED", "this Miabi is already connected to a billing console; disconnect it first")
	ErrNotConnected     = coded(http.StatusConflict, "BILLING_NOT_CONNECTED", "this Miabi is not connected to a billing console")
	ErrInvalidResponse  = coded(http.StatusBadGateway, "BILLING_CONSOLE_INVALID_RESPONSE", "the console answered with addresses outside its own host")
)

// Code is a decoded enrolment code.
type Code struct {
	ConsoleURL string `json:"u"`
	InstanceID string `json:"i"`
	Token      string `json:"t"`
}

// DecodeCode parses an enrolment code; surrounding whitespace from copy and paste is tolerated.
func DecodeCode(s string) (Code, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, CodePrefix) {
		return Code{}, ErrMalformedCode
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, CodePrefix))
	if err != nil {
		return Code{}, ErrMalformedCode
	}
	var c Code
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil || c.ConsoleURL == "" || c.InstanceID == "" || c.Token == "" {
		return Code{}, ErrMalformedCode
	}
	if !validConsoleURL(c.ConsoleURL) {
		return Code{}, ErrConsoleURL
	}
	c.ConsoleURL = strings.TrimRight(c.ConsoleURL, "/")
	return c, nil
}

func validConsoleURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return false
}

// Connection is the console this Miabi is enrolled with.
type Connection struct {
	Connected          bool       `json:"connected"`
	ConsoleURL         string     `json:"console_url,omitempty"`
	ConsoleInstanceID  string     `json:"console_instance_id,omitempty"`
	NotifyURL          string     `json:"notify_url,omitempty"`
	PortalAllowedHosts []string   `json:"portal_allowed_hosts,omitempty"`
	KeyPrefix          string     `json:"key_prefix,omitempty"`
	KeyLastUsedAt      *time.Time `json:"key_last_used_at,omitempty"`
	ConnectedAt        *time.Time `json:"connected_at,omitempty"`
	ConnectedBy        uint       `json:"connected_by,omitempty"`
}

// EnrolmentInfo is what this Miabi tells the console about itself.
type EnrolmentInfo struct {
	MiabiURL  string
	InstallID string
	Version   string
}

type enrollRequest struct {
	InstanceID          string `json:"instance_id"`
	Token               string `json:"token"`
	MiabiURL            string `json:"miabi_url"`
	InstallID           string `json:"install_id"`
	Version             string `json:"version"`
	APIKey              string `json:"api_key"`
	NotifySecret        string `json:"notify_secret"`
	PortalSigningSecret string `json:"portal_signing_secret"`
	Timestamp           string `json:"timestamp"`
}

type enrollResponse struct {
	NotifyURL             string   `json:"notify_url"`
	PortalSessionEndpoint string   `json:"portal_session_endpoint"`
	PortalAllowedHosts    []string `json:"portal_allowed_hosts"`
	ConsoleURL            string   `json:"console_url"`
}

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Enroll connects this Miabi to the console that issued code: it mints the billing key and the
// two secrets, hands them to the console, and stores the connection. The key is revoked when the
// console refuses or answers with something unusable, so a failed attempt leaves nothing behind.
func (s *Service) Enroll(ctx context.Context, rawCode string, actorID uint) (*Connection, error) {
	code, err := DecodeCode(rawCode)
	if err != nil {
		return nil, err
	}
	if cur, err := s.Connection(); err != nil {
		return nil, err
	} else if cur.Connected {
		return nil, ErrAlreadyConnected
	}
	consoleHost := mustHost(code.ConsoleURL)
	plaintext, key, err := s.MintKey("Miabi Console ("+consoleHost+")", nil)
	if err != nil {
		return nil, err
	}
	notifySecret, portalSecret := randomHex(), randomHex()
	resp, err := s.callConsole(ctx, code, enrollRequest{
		InstanceID: code.InstanceID, Token: code.Token, MiabiURL: s.info.MiabiURL, InstallID: s.info.InstallID, Version: s.info.Version,
		APIKey: plaintext, NotifySecret: notifySecret, PortalSigningSecret: portalSecret, Timestamp: s.now().UTC().Format(time.RFC3339),
	})
	if err == nil {
		err = sameHost(consoleHost, resp.NotifyURL, resp.PortalSessionEndpoint, resp.ConsoleURL)
	}
	if err == nil {
		err = s.storeConnection(code, resp, notifySecret, portalSecret, key.ID, actorID)
	}
	if err != nil {
		if rerr := s.keys.Revoke(key.ID); rerr != nil {
			return nil, errors.Join(err, fmt.Errorf("revoke the unused billing key %d: %w", key.ID, rerr))
		}
		return nil, err
	}
	return s.Connection()
}

func (s *Service) callConsole(ctx context.Context, code Code, body enrollRequest) (*enrollResponse, error) {
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, enrollTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, code.ConsoleURL+"/api/v1/instances/enroll", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "miabi/"+s.info.Version)
	res, err := s.client().Do(req)
	if err != nil {
		return nil, coded(http.StatusBadGateway, "BILLING_CONSOLE_UNREACHABLE", "the console could not be reached: "+err.Error())
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var env envelope
	_ = json.Unmarshal(data, &env)
	if res.StatusCode >= 300 || !env.Success {
		msg, c := "the console refused the enrolment (HTTP "+strconv.Itoa(res.StatusCode)+")", "BILLING_CONSOLE_REFUSED"
		if env.Error != nil && env.Error.Code != "" {
			msg, c = env.Error.Message, env.Error.Code
		}
		status := http.StatusBadGateway
		if res.StatusCode >= 400 && res.StatusCode < 500 {
			status = http.StatusConflict
		}
		return nil, coded(status, c, msg)
	}
	var out enrollResponse
	if err := json.Unmarshal(env.Data, &out); err != nil || out.NotifyURL == "" || out.ConsoleURL == "" {
		return nil, ErrInvalidResponse
	}
	return &out, nil
}

// sameHost refuses any returned address outside the console's own host: the notify secret and
// customer portal links must never go elsewhere.
func sameHost(host string, urls ...string) error {
	for _, raw := range urls {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Host, host) || (u.Scheme != "https" && !validConsoleURL(raw)) {
			return ErrInvalidResponse
		}
	}
	return nil
}

func mustHost(raw string) string {
	u, _ := url.Parse(raw)
	return u.Host
}

func randomHex() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Service) storeConnection(code Code, resp *enrollResponse, notifySecret, portalSecret string, keyID, actorID uint) error {
	encNotify, err := crypto.Encrypt(notifySecret)
	if err != nil {
		return err
	}
	encPortal, err := crypto.Encrypt(portalSecret)
	if err != nil {
		return err
	}
	hosts, _ := json.Marshal(resp.PortalAllowedHosts)
	str := func(k, v string) models.Setting {
		return models.Setting{Key: k, Value: v, Type: models.SettingTypeString}
	}
	return s.settings.BulkUpsert([]models.Setting{
		str(SettingConsoleURL, code.ConsoleURL),
		str(SettingConsoleInstanceID, code.InstanceID),
		str(SettingNotifyURL, resp.NotifyURL),
		str(SettingNotifySecret, encNotify),
		str(SettingPortalMode, "dynamic"),
		str(SettingPortalSessionEndpoint, resp.PortalSessionEndpoint),
		str(SettingPortalSigningSecret, encPortal),
		str(SettingPortalAllowedHosts, string(hosts)),
		str(SettingAPIKeyID, strconv.FormatUint(uint64(keyID), 10)),
		str(SettingConnectedAt, s.now().UTC().Format(time.RFC3339)),
		str(SettingConnectedBy, strconv.FormatUint(uint64(actorID), 10)),
	})
}

// Connection reports the current console connection.
func (s *Service) Connection() (*Connection, error) {
	all, err := s.settings.All()
	if err != nil {
		return nil, err
	}
	v := map[string]string{}
	for _, st := range all {
		if strings.HasPrefix(st.Key, "billing.") {
			v[st.Key] = st.Value
		}
	}
	if v[SettingConsoleURL] == "" {
		return &Connection{}, nil
	}
	c := &Connection{Connected: true, ConsoleURL: v[SettingConsoleURL], ConsoleInstanceID: v[SettingConsoleInstanceID], NotifyURL: v[SettingNotifyURL]}
	_ = json.Unmarshal([]byte(v[SettingPortalAllowedHosts]), &c.PortalAllowedHosts)
	if t, err := time.Parse(time.RFC3339, v[SettingConnectedAt]); err == nil {
		c.ConnectedAt = &t
	}
	if by, err := strconv.ParseUint(v[SettingConnectedBy], 10, 64); err == nil {
		c.ConnectedBy = uint(by)
	}
	if id, err := strconv.ParseUint(v[SettingAPIKeyID], 10, 64); err == nil {
		var k models.APIKey
		if err := s.db.Select("key_prefix", "last_used_at").First(&k, id).Error; err == nil {
			c.KeyPrefix, c.KeyLastUsedAt = k.KeyPrefix, k.LastUsedAt
		}
	}
	return c, nil
}

// NotifySecret returns the decrypted secret signing Miabi's calls to the console.
func (s *Service) NotifySecret() (string, error) {
	st, err := s.settings.Get(SettingNotifySecret)
	if err != nil {
		return "", ErrNotConnected
	}
	return crypto.Decrypt(st.Value)
}

// Disconnect revokes the billing key and forgets the console. The console keeps its own record and
// sees its calls refused; reconnecting needs a new enrolment code.
func (s *Service) Disconnect() error {
	cur, err := s.Connection()
	if err != nil {
		return err
	}
	if !cur.Connected {
		return ErrNotConnected
	}
	if st, err := s.settings.Get(SettingAPIKeyID); err == nil {
		if id, err := strconv.ParseUint(st.Value, 10, 64); err == nil {
			if err := s.keys.Revoke(uint(id)); err != nil {
				return err
			}
		}
	}
	for _, k := range connectionKeys {
		if err := s.settings.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// client calls the console. The console must answer itself: a redirect could hand a key or a
// signed event to another host.
func (s *Service) client() *http.Client {
	if s.http != nil {
		return s.http
	}
	return &http.Client{Timeout: enrollTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
