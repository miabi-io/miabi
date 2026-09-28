// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package transfer

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
)

// oneEngine plays two nodes with the local engine, so a copy crosses the same splice a real move does
// without needing a second machine.
type oneEngine struct{ dc docker.Client }

func (o oneEngine) For(uint) (docker.Client, error) { return o.dc, nil }

func syncImage() string {
	if v := os.Getenv("MIABI_SYNC_IMAGE"); v != "" {
		return v
	}
	return "miabi/sync:latest"
}

func setup(t *testing.T) (*Service, docker.Client) {
	t.Helper()
	dc, err := docker.New()
	if err != nil {
		t.Skipf("no docker: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := dc.Ping(ctx); err != nil {
		t.Skipf("no docker: %v", err)
	}
	if ok, _ := dc.ImageExists(ctx, syncImage()); !ok {
		t.Skipf("sync image %s not present; build it from github.com/miabi-io/sync first", syncImage())
	}
	svc := New(oneEngine{dc}, syncImage, func() string { return "alpine/socat:latest" })
	return svc, dc
}

func volume(t *testing.T, dc docker.Client, name string) {
	t.Helper()
	ctx := context.Background()
	if _, err := dc.CreateVolume(ctx, name, nil, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dc.RemoveVolume(context.Background(), name, true) })
}

func sh(t *testing.T, dc docker.Client, vol, script string) string {
	t.Helper()
	code, out, err := dc.RunOneShot(context.Background(), docker.RunSpec{
		Image: syncImage(), Cmd: []string{"sh", "-c", script}, Mounts: map[string]string{vol: "/data"},
	})
	if err != nil || code != 0 {
		t.Fatalf("sh %q: %d %v %s", script, code, err, out)
	}
	return strings.TrimSpace(out)
}

func TestSyncVolumeCopiesAndConverges(t *testing.T) {
	svc, dc := setup(t)
	id := fmt.Sprintf("it%d", time.Now().UnixNano()%1_000_000)
	src, dst := "mb-it-src-"+id, "mb-it-dst-"+id
	volume(t, dc, src)
	volume(t, dc, dst)
	sh(t, dc, src, `mkdir -p /data/a/b && head -c 5000000 /dev/urandom > /data/a/big && echo hello > /data/a/b/small && ln -s b/small /data/a/link && chown 1234:5678 /data/a/b/small`)
	sh(t, dc, dst, `echo stale > /data/stale`)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	first, err := svc.SyncVolume(ctx, VolumeEnd{ServerID: 1, Volume: src}, VolumeEnd{ServerID: 2, Volume: dst}, SyncOptions{Name: id})
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if first.Transferred < 5000000 || first.Streamed < 5000000 {
		t.Errorf("first pass should carry the whole volume: %+v", first)
	}

	sum := `cd /data && find . -type f -o -type l | sort | xargs -I{} sh -c 'stat -c "%n %s %u:%g" "{}"; [ -L "{}" ] || md5sum "{}"'`
	if a, b := sh(t, dc, src, sum), sh(t, dc, dst, sum); a != b {
		t.Fatalf("copy differs:\nsrc:\n%s\ndst:\n%s", a, b)
	}

	sh(t, dc, src, `echo more >> /data/a/b/small && rm /data/a/big`)
	second, err := svc.SyncVolume(ctx, VolumeEnd{ServerID: 1, Volume: src}, VolumeEnd{ServerID: 2, Volume: dst}, SyncOptions{Name: id, Final: true})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if second.Transferred > 1000 {
		t.Errorf("second pass should send only the delta: %+v", second)
	}
	if a, b := sh(t, dc, src, sum), sh(t, dc, dst, sum); a != b {
		t.Fatalf("delta pass left a difference:\nsrc:\n%s\ndst:\n%s", a, b)
	}
}

func TestStreamPipesStdoutIntoStdin(t *testing.T) {
	svc, dc := setup(t)
	id := fmt.Sprintf("it%d", time.Now().UnixNano()%1_000_000)
	vol := "mb-it-stream-" + id
	volume(t, dc, vol)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	n, err := svc.Stream(ctx,
		StreamEnd{ServerID: 1, Spec: docker.RunSpec{Name: "mb-it-p-" + id, Image: syncImage(), Cmd: []string{"sh", "-c", "head -c 3000000 /dev/zero"}}},
		StreamEnd{ServerID: 2, Spec: docker.RunSpec{Name: "mb-it-c-" + id, Image: syncImage(), Cmd: []string{"sh", "-c", "wc -c > /data/n"}, Mounts: map[string]string{vol: "/data"}}},
		nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3000000 {
		t.Errorf("streamed %d bytes", n)
	}
	if got := sh(t, dc, vol, "cat /data/n"); got != "3000000" {
		t.Errorf("consumer read %s bytes", got)
	}

	_, err = svc.Stream(ctx,
		StreamEnd{ServerID: 1, Spec: docker.RunSpec{Name: "mb-it-p2-" + id, Image: syncImage(), Cmd: []string{"sh", "-c", "echo boom >&2; exit 3"}}},
		StreamEnd{ServerID: 2, Spec: docker.RunSpec{Name: "mb-it-c2-" + id, Image: syncImage(), Cmd: []string{"sh", "-c", "cat > /dev/null"}}},
		nil)
	if err == nil || !strings.Contains(err.Error(), "dump exited 3") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a failing producer must surface its exit and stderr: %v", err)
	}
}
