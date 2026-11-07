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

package blob

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"

	"github.com/aws/smithy-go"
)

// Not a HeadBucket: a bucket existing says nothing about whether these credentials may write to it.
type Probe struct {
	// Key is the object the probe used, for the message shown to the operator.
	Key string
	// Wrote, ReadBack and Removed record how far it got.
	Wrote    bool
	ReadBack bool
	Removed  bool
}

// Anyone finding a left-behind probe object can tell what it is and that removing it is safe.
const probeNotice = "Miabi connection test. Safe to delete."

// Delete is never fatal: a missing delete permission costs retention, not data.
func RunProbe(ctx context.Context, cfg Config, prefix string) (Probe, error) {
	var p Probe
	store, err := New(cfg)
	if err != nil {
		return p, err
	}
	suffix := make([]byte, 6)
	if _, err := crand.Read(suffix); err != nil {
		return p, fmt.Errorf("generate a test object name: %w", err)
	}
	name := ".miabi-connection-test-" + hex.EncodeToString(suffix) + ".txt"
	if clean := strings.Trim(strings.TrimSpace(prefix), "/"); clean != "" {
		p.Key = path.Join(clean, name)
	} else {
		p.Key = name
	}

	body := []byte(probeNotice)
	if err := store.Put(ctx, p.Key, body); err != nil {
		return p, fmt.Errorf("could not write %s: %w", p.Key, explain(err, cfg))
	}
	p.Wrote = true

	got, err := store.GetBytes(ctx, p.Key)
	if err != nil {
		// Best-effort cleanup: the object exists even though reading it failed.
		_ = store.Delete(context.WithoutCancel(ctx), p.Key)
		return p, fmt.Errorf("wrote %s but could not read it back: %w", p.Key, explain(err, cfg))
	}
	if !bytes.Equal(got, body) {
		_ = store.Delete(context.WithoutCancel(ctx), p.Key)
		return p, fmt.Errorf("%s read back as %d bytes instead of %d: the endpoint is not storing what it accepts",
			p.Key, len(got), len(body))
	}
	p.ReadBack = true

	if err := store.Delete(ctx, p.Key); err == nil {
		p.Removed = true
	}
	return p, nil
}

// SDK errors name the symptom, not the setting, so the common mistakes say which field to check.
func explain(err error, cfg Config) error {
	if err == nil {
		return nil
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchBucket":
			return fmt.Errorf("bucket %q does not exist at this endpoint", cfg.Bucket)
		case "InvalidAccessKeyId":
			return errors.New("the access key is not recognized by this endpoint")
		case "SignatureDoesNotMatch":
			return errors.New("the secret key does not match the access key (or the region is wrong)")
		case "AccessDenied", "Forbidden":
			return fmt.Errorf("these credentials are not allowed to write here: check the policy on %q", cfg.Bucket)
		case "AuthorizationHeaderMalformed", "IllegalLocationConstraintException", "PermanentRedirect":
			return fmt.Errorf("the bucket is not in region %q, or it needs path-style URLs", cfg.Region)
		}
	}
	// A DNS failure on a virtual-host address is almost always a self-hosted store needing path-style URLs.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && !cfg.ForcePathStyle {
		return fmt.Errorf("could not resolve %s. Self-hosted stores (MinIO and friends) need \"Force path-style URLs\" enabled: %w",
			hostOf(cfg, dnsErr.Name), err)
	}
	return err
}

// hostOf names the address the client tried, for the path-style hint.
func hostOf(cfg Config, fallback string) string {
	if ep := strings.TrimSpace(cfg.Endpoint); ep != "" {
		if u, err := url.Parse(normalizeEndpoint(ep, cfg.UseSSL)); err == nil && u.Host != "" {
			return u.Host
		}
		return ep
	}
	return fallback
}
