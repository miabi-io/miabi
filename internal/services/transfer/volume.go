// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package transfer

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
)

// VolumeEnd is one side of a volume copy: a Docker volume on a node.
type VolumeEnd struct {
	ServerID uint
	Volume   string
}

// SyncOptions tunes one rsync pass.
type SyncOptions struct {
	// Name makes the helper containers and the sync network unique; one migration item per name.
	Name string
	// Final marks the pass run with every writer stopped. Files that vanish or change mid-copy are
	// expected on a live pass and fatal on the final one, where they would mean a writer is still running.
	Final bool
	// BandwidthKBps caps the copy (rsync --bwlimit), so a live pass does not saturate a node's uplink.
	BandwidthKBps int
	// Progress receives the bytes streamed so far, every few seconds.
	Progress func(streamed int64)
}

// SyncStats is what one pass did.
type SyncStats struct {
	// Transferred is the size of the files that changed and were sent: the delta a next pass would face.
	Transferred int64 `json:"transferred"`
	// Total is the size of every file in the volume.
	Total int64 `json:"total"`
	// Streamed is the bytes that crossed the control plane, after rsync's delta encoding.
	Streamed int64         `json:"streamed"`
	Duration time.Duration `json:"duration"`
}

const rsyncPort = 873

// daemonScript runs rsync as a read-only daemon over /data. The secret stays in the environment and a mode
// 600 file: rsync refuses a secrets file anyone else can read.
const daemonScript = `set -e
umask 077
printf 'mb:%s\n' "$SYNC_SECRET" > /tmp/rsyncd.secrets
printf '%s\n' 'uid = 0' 'gid = 0' 'use chroot = no' 'max connections = 4' 'pid file = /tmp/rsyncd.pid' \
  '[data]' 'path = /data' 'read only = yes' 'auth users = mb' 'secrets file = /tmp/rsyncd.secrets' > /tmp/rsyncd.conf
exec rsync --daemon --no-detach --port=873 --config=/tmp/rsyncd.conf`

// SyncVolume makes dst's volume a copy of src's with one rsync pass. dst must already exist.
//
// The daemon runs next to the source on a throwaway internal network, the client next to the target on
// another, and the control plane joins the two: a relay dialled into the source network on one side, a
// relay listening on the target network on the other. The internal networks and a per-pass secret keep
// workspace containers away from the daemon; that is access control, not encryption.
func (s *Service) SyncVolume(ctx context.Context, src, dst VolumeEnd, opts SyncOptions) (SyncStats, error) {
	started := time.Now()
	if src.ServerID == dst.ServerID {
		return SyncStats{}, ErrSameNode
	}
	sdc, err := s.engines.For(src.ServerID)
	if err != nil {
		return SyncStats{}, fmt.Errorf("source node: %w", err)
	}
	tdc, err := s.engines.For(dst.ServerID)
	if err != nil {
		return SyncStats{}, fmt.Errorf("target node: %w", err)
	}
	syncImage, relayImage := s.syncImage(), s.relayImage()
	for _, dc := range []docker.Client{sdc, tdc} {
		for _, img := range []string{syncImage, relayImage} {
			if err := pull(ctx, dc, img); err != nil {
				return SyncStats{}, err
			}
		}
	}

	name := "mb-sync-" + opts.Name
	daemon, listener, client := name+"-d", name+"-l", name+"-c"
	for _, dc := range []docker.Client{sdc, tdc} {
		cleanup(dc, name, daemon, listener, client)
		if _, err := dc.CreateNetwork(ctx, name, "bridge", true); err != nil {
			return SyncStats{}, fmt.Errorf("create sync network: %w", err)
		}
	}
	defer cleanup(sdc, name, daemon)
	defer cleanup(tdc, name, listener, client)

	secret := randomSecret()
	if _, err := sdc.RunContainer(ctx, docker.RunSpec{
		Name:           daemon,
		Image:          syncImage,
		Cmd:            []string{"sh", "-c", daemonScript},
		Env:            []string{"SYNC_SECRET=" + secret},
		Networks:       []string{name},
		Mounts:         map[string]string{src.Volume: "/data"},
		ReadOnlyMounts: []string{src.Volume},
		RestartPolicy:  "no",
	}); err != nil {
		return SyncStats{}, fmt.Errorf("start rsync daemon: %w", err)
	}
	if err := awaitDaemon(ctx, sdc, name, relayImage, daemon); err != nil {
		return SyncStats{}, err
	}

	lis, err := tdc.RunAttached(ctx, docker.RunSpec{
		Name:     listener,
		Image:    relayImage,
		Cmd:      []string{fmt.Sprintf("TCP-LISTEN:%d,reuseaddr", rsyncPort), "STDIO"},
		Networks: []string{name},
	})
	if err != nil {
		return SyncStats{}, fmt.Errorf("start target relay: %w", err)
	}
	defer lis.Close()
	conn, err := sdc.DialNetwork(ctx, name, relayImage, daemon, rsyncPort)
	if err != nil {
		return SyncStats{}, fmt.Errorf("dial rsync daemon: %w", err)
	}
	defer conn.Close()

	var streamed atomic.Int64
	spliced := make(chan struct{})
	go func() {
		splice(conn, lis, &streamed)
		close(spliced)
	}()
	stop := make(chan struct{})
	if opts.Progress != nil {
		go ticker(3*time.Second, stop, func() { opts.Progress(streamed.Load()) })
	}

	var out strings.Builder
	code, runErr := tdc.RunOneShotStream(ctx, docker.RunSpec{
		Name:     client,
		Image:    syncImage,
		Cmd:      []string{"sh", "-c", clientScript(listener, opts.BandwidthKBps)},
		Env:      []string{"RSYNC_PASSWORD=" + secret},
		Networks: []string{name},
		Mounts:   map[string]string{dst.Volume: "/data"},
	}, func(l docker.LogLine) error {
		out.WriteString(l.Text)
		out.WriteByte('\n')
		return nil
	})
	close(stop)
	_ = lis.Close()
	_ = conn.Close()
	<-spliced

	stats := parseStats(out.String())
	stats.Streamed = streamed.Load()
	stats.Duration = time.Since(started)
	if opts.Progress != nil {
		opts.Progress(stats.Streamed)
	}
	if runErr != nil {
		return stats, fmt.Errorf("rsync: %w", runErr)
	}
	if !rsyncOK(code, opts.Final) {
		return stats, fmt.Errorf("rsync exited %d: %s", code, tail(out.String(), 5))
	}
	return stats, nil
}

// clientScript pulls the daemon's module into /data. It retries only a refused connection: the listener
// may not be accepting yet, and nothing has been transferred when that happens.
func clientScript(listener string, bwlimit int) string {
	args := []string{"rsync", "-aH", "--numeric-ids", "--delete", "--partial", "--info=stats2", "--no-inc-recursive"}
	if bwlimit > 0 {
		args = append(args, "--bwlimit="+strconv.Itoa(bwlimit))
	}
	args = append(args, fmt.Sprintf("rsync://mb@%s:%d/data/", listener, rsyncPort), "/data/")
	return fmt.Sprintf(`for i in 1 2 3 4 5; do
  %s 2>&1 && exit 0
  code=$?
  [ "$code" -ne 10 ] && exit "$code"
  sleep 1
done
exit 10`, strings.Join(args, " "))
}

// rsyncOK accepts a clean exit, and on a live pass the two codes a running writer causes: 23 (a file
// could not be read, often because it changed) and 24 (a file vanished). The final pass must not see them.
func rsyncOK(code int, final bool) bool {
	return code == 0 || (!final && (code == 23 || code == 24))
}

// awaitDaemon waits until the daemon greets. A probe connection costs nothing: rsyncd forks per client.
func awaitDaemon(ctx context.Context, dc docker.Client, network, relayImage, daemon string) error {
	var last error
	for i := 0; i < 20; i++ {
		conn, err := dc.DialNetwork(ctx, network, relayImage, daemon, rsyncPort)
		if err == nil {
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			line, rerr := bufio.NewReader(conn).ReadString('\n')
			_ = conn.Close()
			if rerr == nil && strings.HasPrefix(line, "@RSYNCD:") {
				return nil
			}
			if rerr != nil && rerr != io.EOF {
				last = rerr
			} else {
				last = fmt.Errorf("unexpected greeting %q", strings.TrimSpace(line))
			}
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("rsync daemon did not come up: %v", last)
}

func cleanup(dc docker.Client, network string, containers ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, c := range containers {
		_ = dc.RemoveContainer(ctx, c, true)
	}
	_ = dc.RemoveNetwork(ctx, network)
}

var statsLine = regexp.MustCompile(`^(Total file size|Total transferred file size):\s*([\d,.]+)`)

// parseStats reads rsync's --info=stats2 summary. Sizes may carry thousands separators.
func parseStats(out string) SyncStats {
	var st SyncStats
	for _, line := range strings.Split(out, "\n") {
		m := statsLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		n, err := strconv.ParseInt(strings.NewReplacer(",", "", ".", "").Replace(m[2]), 10, 64)
		if err != nil {
			continue
		}
		if m[1] == "Total file size" {
			st.Total = n
		} else {
			st.Transferred = n
		}
	}
	return st
}

func randomSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
