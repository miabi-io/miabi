// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package transfer

import (
	"strings"
	"testing"
)

func TestParseStats(t *testing.T) {
	out := `Number of files: 1,204 (reg: 1,100, dir: 104)
Number of regular files transferred: 12
Total file size: 2,147,483,648 bytes
Total transferred file size: 8,388,608 bytes
Literal data: 4,194,304 bytes
sent 1,024 bytes  received 4,200,000 bytes  1,400,341.33 bytes/sec`
	st := parseStats(out)
	if st.Total != 2147483648 {
		t.Errorf("Total = %d", st.Total)
	}
	if st.Transferred != 8388608 {
		t.Errorf("Transferred = %d", st.Transferred)
	}
}

func TestParseStatsMissing(t *testing.T) {
	if st := parseStats("rsync error: some files could not be transferred"); st.Total != 0 || st.Transferred != 0 {
		t.Errorf("want zero stats, got %+v", st)
	}
}

func TestRsyncOK(t *testing.T) {
	cases := []struct {
		code  int
		final bool
		want  bool
	}{
		{0, false, true},
		{0, true, true},
		{24, false, true}, // a file vanished while the app wrote
		{23, false, true}, // a file changed mid-read
		{24, true, false}, // on the final pass a writer is still running
		{23, true, false},
		{10, false, false}, // socket error
		{12, false, false}, // protocol error
	}
	for _, c := range cases {
		if got := rsyncOK(c.code, c.final); got != c.want {
			t.Errorf("rsyncOK(%d, final=%v) = %v, want %v", c.code, c.final, got, c.want)
		}
	}
}

func TestClientScript(t *testing.T) {
	s := clientScript("mb-sync-7-v3-l", 0)
	for _, want := range []string{"--delete", "--numeric-ids", "rsync://mb@mb-sync-7-v3-l:873/data/", "/data/"} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "--bwlimit") {
		t.Error("no bandwidth limit was asked for")
	}
	if !strings.Contains(clientScript("x", 5000), "--bwlimit=5000") {
		t.Error("bandwidth limit missing")
	}
}
