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

package backup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/dbenvelope"
)

const discoverPass = "a-discovery-passphrase-1"

func fakeFetch(bodies map[string][]byte) fetcher {
	return func(_ context.Context, key string) ([]byte, error) {
		b, ok := bodies[key]
		if !ok {
			return nil, errors.New("no such object")
		}
		return b, nil
	}
}

func infoJSON(t *testing.T, info SetInfo) []byte {
	t.Helper()
	info.Schema = SetInfoSchema
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func baseInfo(t *testing.T, encrypted bool, passphrase string) SetInfo {
	t.Helper()
	info := SetInfo{
		Ref: "mbdb_pg_20260909T030000Z", Instance: "pg", Engine: "postgres", Version: "17",
		CreatedAt: time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC), Encrypted: encrypted,
		Artifacts: []SetInfoArtifact{
			{Database: "orders", Filename: "orders.sql.gz.gpg", Encrypted: encrypted},
			{Database: "billing", Filename: "billing.sql.gz.gpg", Encrypted: encrypted},
		},
	}
	if encrypted {
		key, err := dbenvelope.NewDataKey()
		if err != nil {
			t.Fatal(err)
		}
		if info.Envelope, err = dbenvelope.Seal(key, passphrase); err != nil {
			t.Fatal(err)
		}
	}
	return info
}

const infoKey = "backups/databases/pg/mbdb_pg_20260909T030000Z/info.json"

func bothPresent() map[string]int64 {
	return map[string]int64{
		"backups/databases/pg/mbdb_pg_20260909T030000Z/orders.sql.gz.gpg":  100,
		"backups/databases/pg/mbdb_pg_20260909T030000Z/billing.sql.gz.gpg": 200,
	}
}

func TestDescribeSetReadsADescriptor(t *testing.T) {
	info := baseInfo(t, true, discoverPass)
	got, err := describeSet(context.Background(),
		fakeFetch(map[string][]byte{infoKey: infoJSON(t, info)}), infoKey, bothPresent(), discoverPass)
	if err != nil {
		t.Fatalf("describeSet: %v", err)
	}
	if got.Ref != info.Ref || got.Instance != "pg" || got.Engine != "postgres" || got.Version != "17" {
		t.Errorf("descriptor not carried through: %+v", got)
	}
	if got.Prefix != "backups/databases/pg/mbdb_pg_20260909T030000Z" {
		t.Errorf("prefix = %q", got.Prefix)
	}
	if !got.Openable || got.Reason != "" {
		t.Errorf("a complete set with the right passphrase is not openable: %q", got.Reason)
	}
	for _, a := range got.Artifacts {
		if !a.Present {
			t.Errorf("%s reported missing", a.Filename)
		}
	}
}

// Retention and lifecycle rules act afterwards, so presence is checked rather than trusted.
func TestDescribeSetReportsMissingArtifacts(t *testing.T) {
	info := baseInfo(t, true, discoverPass)
	body := map[string][]byte{infoKey: infoJSON(t, info)}

	partial := bothPresent()
	delete(partial, "backups/databases/pg/mbdb_pg_20260909T030000Z/billing.sql.gz.gpg")
	got, err := describeSet(context.Background(), fakeFetch(body), infoKey, partial, discoverPass)
	if err != nil {
		t.Fatal(err)
	}
	if got.Openable {
		t.Error("a set missing an artifact is reported as usable")
	}
	if !strings.Contains(got.Reason, "1 of 2") {
		t.Errorf("reason = %q, want it to name how many are missing", got.Reason)
	}

	got, err = describeSet(context.Background(), fakeFetch(body), infoKey, map[string]int64{}, discoverPass)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Reason, "none of this recovery point") {
		t.Errorf("reason = %q, want the wholly-gone wording", got.Reason)
	}
}

func TestDescribeSetReportsWhetherThePassphraseOpensIt(t *testing.T) {
	info := baseInfo(t, true, "the-original-passphrase-1")
	body := map[string][]byte{infoKey: infoJSON(t, info)}

	got, err := describeSet(context.Background(), fakeFetch(body), infoKey, bothPresent(), "a-different-passphrase-2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Openable {
		t.Error("a set sealed with another passphrase is reported openable")
	}
	if !strings.Contains(got.Reason, "different passphrase") {
		t.Errorf("reason = %q", got.Reason)
	}

	got, err = describeSet(context.Background(), fakeFetch(body), infoKey, bothPresent(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Openable || !strings.Contains(got.Reason, "set the workspace backup passphrase") {
		t.Errorf("with no passphrase: openable=%v reason=%q", got.Openable, got.Reason)
	}
}

// A user who lost their secret still has to see what they lost.
func TestDescribeSetNeedsNoPassphraseToList(t *testing.T) {
	info := baseInfo(t, true, discoverPass)
	got, err := describeSet(context.Background(),
		fakeFetch(map[string][]byte{infoKey: infoJSON(t, info)}), infoKey, bothPresent(), "")
	if err != nil {
		t.Fatalf("listing an encrypted set failed without a passphrase: %v", err)
	}
	if got.Ref == "" || len(got.Artifacts) != 2 {
		t.Errorf("the set was not described: %+v", got)
	}
}

func TestDescribeSetUnencrypted(t *testing.T) {
	info := baseInfo(t, false, "")
	got, err := describeSet(context.Background(),
		fakeFetch(map[string][]byte{infoKey: infoJSON(t, info)}), infoKey, bothPresent(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Openable {
		t.Error("an unencrypted, complete set is not usable")
	}
}

func TestDescribeSetRefusesWhatItCannotTrust(t *testing.T) {
	cases := map[string][]byte{
		"not json":       []byte("{{{"),
		"unknown schema": []byte(`{"schema":99,"ref":"x"}`),
		"no ref":         []byte(`{"schema":1,"ref":""}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := describeSet(context.Background(),
				fakeFetch(map[string][]byte{infoKey: body}), infoKey, nil, ""); err == nil {
				t.Error("accepted a descriptor it should have refused")
			}
		})
	}
}

// A restore uses this layout to find the objects, so it must be stable.
func TestSetPrefix(t *testing.T) {
	cases := []struct{ base, instance, ref, want string }{
		{"backups/databases", "pg", "mbdb_pg_1", "backups/databases/pg/mbdb_pg_1"},
		{"/backups/databases/", "pg", "mbdb_pg_1", "backups/databases/pg/mbdb_pg_1"},
		{"  ", "pg", "mbdb_pg_1", "pg/mbdb_pg_1"},
		{"", "pg", "mbdb_pg_1", "pg/mbdb_pg_1"},
	}
	for _, tc := range cases {
		if got := SetPrefix(tc.base, tc.instance, tc.ref); got != tc.want {
			t.Errorf("SetPrefix(%q, %q, %q) = %q, want %q", tc.base, tc.instance, tc.ref, got, tc.want)
		}
	}
}
