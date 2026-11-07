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
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/aws/smithy-go"
)

// apiErr fakes the shape the SDK reports a service error in.
type apiErr struct{ code string }

func (e apiErr) Error() string                 { return e.code }
func (e apiErr) ErrorCode() string             { return e.code }
func (e apiErr) ErrorMessage() string          { return e.code }
func (e apiErr) ErrorFault() smithy.ErrorFault { return smithy.FaultServer }

func TestExplainNamesTheSettingToFix(t *testing.T) {
	cfg := Config{Bucket: "acme-backups", Region: "eu-central-1"}
	cases := []struct{ code, want string }{
		{"NoSuchBucket", "does not exist"},
		{"InvalidAccessKeyId", "access key is not recognized"},
		{"SignatureDoesNotMatch", "secret key does not match"},
		{"AccessDenied", "not allowed to write"},
		{"PermanentRedirect", "not in region"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			got := explain(apiErr{code: tc.code}, cfg)
			if got == nil || !strings.Contains(got.Error(), tc.want) {
				t.Fatalf("explain(%s) = %v, want it to mention %q", tc.code, got, tc.want)
			}
		})
	}
}

func TestExplainSuggestsPathStyleOnDNSFailure(t *testing.T) {
	dnsErr := &net.DNSError{Err: "no such host", Name: "acme-backups.minio.internal"}
	cfg := Config{Bucket: "acme-backups", Endpoint: "minio.internal:9000"}

	got := explain(dnsErr, cfg)
	if got == nil || !strings.Contains(got.Error(), "path-style") {
		t.Fatalf("explain(dns) = %v, want a path-style hint", got)
	}
	if !strings.Contains(got.Error(), "minio.internal:9000") {
		t.Fatalf("the hint does not name the endpoint tried: %v", got)
	}

	// With path-style already on, a DNS failure is a genuine networking problem, not a setting.
	cfg.ForcePathStyle = true
	got = explain(dnsErr, cfg)
	if strings.Contains(got.Error(), "path-style") {
		t.Fatalf("path-style was suggested when it is already enabled: %v", got)
	}
	if !errors.Is(got, dnsErr) {
		t.Fatalf("the underlying error was swallowed: %v", got)
	}
}

func TestExplainPassesThroughWhatItCannotImprove(t *testing.T) {
	base := errors.New("connection reset by peer")
	if got := explain(base, Config{}); !errors.Is(got, base) {
		t.Fatalf("explain rewrote an error it does not understand: %v", got)
	}
	if explain(nil, Config{}) != nil {
		t.Fatal("explain invented an error from nil")
	}
}
