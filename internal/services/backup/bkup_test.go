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
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/storage/blob"
)

// Output of a *-bkup v2 backup with retention: the old artifact it deleted is
// printed before the one it uploaded, which ends the output.
const v2Output = `time=2026-10-27T10:00:00Z level=INFO msg="Backup started" database=shop storage=s3 compression=gzip encryption=gpg
time=2026-10-27T10:00:01Z level=INFO msg="Deleted old backup" file=shop_20261020_100000.sql.gz.gpg completed_at=2026-10-20T10:00:00Z
time=2026-10-27T10:00:01Z level=INFO msg="Backup completed" database=shop file=shop_20261027_100000.sql.gz.gpg size="1.5 MiB" bytes=1572864 sha256=0f3c duration=1.2s storage=s3 location=s3://bucket/backups
`

func TestV2Output(t *testing.T) {
	name, encrypted, err := ArtifactName(v2Output, DBArtifactRe)
	if err != nil || name != "shop_20261027_100000.sql.gz.gpg" || !encrypted {
		t.Fatalf("ArtifactName = %q %v %v", name, encrypted, err)
	}
	if got := ArtifactSize(v2Output); got != 1572864 {
		t.Fatalf("ArtifactSize = %d", got)
	}
	if got := ArtifactSize("level=INFO msg=done file=x.sql.gz"); got != 0 {
		t.Fatalf("1.x output has no size, got %d", got)
	}
}

func TestVolumeArtifacts(t *testing.T) {
	out := `level=INFO msg="Backup completed" database=data file=data_20261027_100000.tar.zst.age bytes=10 location=s3://b/p`
	name, encrypted, err := ArtifactName(out, VolumeArtifactRe)
	if err != nil || name != "data_20261027_100000.tar.zst.age" || !encrypted {
		t.Fatalf("%q %v %v", name, encrypted, err)
	}
}

func TestIndexEnv(t *testing.T) {
	cases := map[string]string{
		"shop":         "BACKUP_INDEX_NAME={engine}-shop-metadata.json",
		"My DB/prod.1": "BACKUP_INDEX_NAME={engine}-My-DB-prod-1-metadata.json",
		"":             "BACKUP_INDEX_NAME={engine}-default-metadata.json",
	}
	for in, want := range cases {
		if got := IndexEnv(in); got != want {
			t.Errorf("IndexEnv(%q) = %q, want %q", in, got, want)
		}
	}
	// The index file must never look like an artifact to the parsers.
	name := strings.TrimPrefix(IndexEnv("shop.sql.gz"), "BACKUP_INDEX_NAME=")
	if DBArtifactRe.MatchString(name) || VolumeArtifactRe.MatchString(name) {
		t.Fatalf("index name %q matches an artifact regex", name)
	}
}

func TestArtifactSHA256AndEncryption(t *testing.T) {
	if got := ArtifactSHA256(v2Output); got != "" {
		t.Fatalf("short sha must not match: %q", got)
	}
	sha := strings.Repeat("ab", 32)
	out := `level=INFO msg="Backup completed" file=x_20261027_100000.sql.gz.age bytes=10 sha256=` + sha
	if ArtifactSHA256(out) != sha {
		t.Fatal("sha256 not parsed")
	}
	age := Destination{AgeRecipients: "age1x", GPGPassphrase: "pw"}
	if env := strings.Join(age.EncryptionEnv(), " "); !strings.Contains(env, "AGE_RECIPIENTS=age1x") || strings.Contains(env, "GPG_PASSPHRASE") {
		t.Fatalf("age wins over the passphrase for new backups: %s", env)
	}
	if env := (Destination{GPGPassphrase: "pw"}).EncryptionEnv(); len(env) != 1 || env[0] != "GPG_PASSPHRASE=pw" {
		t.Fatal(env)
	}
	if (Destination{}).Encrypting() {
		t.Fatal("no keys, no encryption")
	}
}

func TestRestoreKeyEnv(t *testing.T) {
	cases := []struct {
		file, pass, id, want string
		err                  bool
	}{
		{"a.sql.gz.gpg", "pw", "", "GPG_PASSPHRASE=pw", false},
		{"a.sql.gz.age", "pw", "AGE-SECRET-KEY-1X", "AGE_IDENTITY=AGE-SECRET-KEY-1X", false},
		{"a.sql.gz.age", "pw", "", "", true},
		{"a.sql.gz", "pw", "AGE-SECRET-KEY-1X", "", false},
	}
	for _, c := range cases {
		env, err := RestoreKeyEnv(c.file, c.pass, c.id)
		if (err != nil) != c.err || strings.Join(env, "") != c.want {
			t.Errorf("%s: env=%v err=%v", c.file, env, err)
		}
	}
}

func TestVerifyOutcome(t *testing.T) {
	if s, _ := VerifyOutcome(0, "ok", nil); s != VerifyOK {
		t.Fatal(s)
	}
	if s, _ := VerifyOutcome(1, `Error: unknown command "verify" for "mysql-bkup"`, nil); s != "" {
		t.Fatal("1.x images are skipped, not failed")
	}
	s, r := VerifyOutcome(5, "level=ERROR msg=x\nError: decryption failed: wrong key", nil)
	if s != VerifyFailed || r != "decryption failed: wrong key" {
		t.Fatalf("%s %q", s, r)
	}
}

func TestIndex(t *testing.T) {
	objs := []blob.Object{{Key: "db/set1/pg-metadata.json"}, {Key: "db/set1/a.sql.gz"}, {Key: "db/other/pg-metadata.json"}}
	files := map[string]string{
		"db/set1/pg-metadata.json":  `{"entries":[{"file":"a.sql.gz","status":"success","size":10,"sha256":"aa"},{"file":"b.sql.gz","status":"deleted","size":5}]}`,
		"db/other/pg-metadata.json": `{"entries":[{"file":"z.sql.gz","status":"success"}]}`,
	}
	get := func(_ context.Context, k string) ([]byte, error) { return []byte(files[k]), nil }
	idx := ReadIndex(context.Background(), get, objs, "/db/set1/")
	if len(idx) != 2 || idx["z.sql.gz"].File != "" {
		t.Fatalf("only the index in the directory itself: %+v", idx)
	}
	checks := []struct{ got, want string }{
		{CheckIndexed(idx, "a.sql.gz", 10, "aa"), ""},
		{CheckIndexed(idx, "a.sql.gz", 11, "aa"), IndexResized},
		{CheckIndexed(idx, "a.sql.gz", 10, "bb"), IndexChanged},
		{CheckIndexed(idx, "b.sql.gz", 5, ""), IndexDeleted},
		{CheckIndexed(idx, "c.sql.gz", 5, "zz"), ""},
		{CheckIndexed(nil, "a.sql.gz", 99, "zz"), ""},
	}
	for i, c := range checks {
		if c.got != c.want {
			t.Errorf("case %d: got %q want %q", i, c.got, c.want)
		}
	}
	if ReadIndex(context.Background(), get, []blob.Object{{Key: "db/set1/a.sql.gz"}}, "db/set1") != nil {
		t.Fatal("no index file means nil (1.x)")
	}
}
