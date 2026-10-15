package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BangBOOM/port-fwd-tools/internal/core"
)

func TestRealSSHTrafficCounters(t *testing.T) {
	dir := t.TempDir()
	_, configPath := newSSHFixture(t, dir)
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	paths, err := ResolvePaths(filepath.Join(dir, "home"))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(paths.Runtime)
	manager, err := NewManager(paths, Options{SSHConfig: configPath})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	rule := core.Rule{Name: "traffic", SSHHost: "fixture", LocalPort: freePort(t), RemoteHost: "127.0.0.1", RemotePort: echo.Addr().(*net.TCPAddr).Port, Enabled: true, AutoReconnect: true}
	if err = manager.Apply(Request{Action: "add", Rule: rule}); err != nil {
		t.Fatal(err)
	}
	tunnel := waitFor(t, manager, "traffic", "connected")
	client := NewClient(paths)
	before, err := client.Traffic(context.Background(), tunnel.Rule.ID)
	if errors.Is(err, ErrTrafficUnsupported) {
		t.Skip("installed OpenSSH lacks the info mux extension")
	}
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", rule.LocalPort), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	payload := bytes.Repeat([]byte("counter-test-"), 160000)
	written := make(chan error, 1)
	go func() { _, err := io.Copy(conn, bytes.NewReader(payload)); written <- err }()
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if err = <-written; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("statistics affected forwarded payload")
	}
	after, err := client.Traffic(context.Background(), tunnel.Rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Received == after.Received || before.Sent == after.Sent {
		t.Fatalf("traffic did not increase: %+v -> %+v", before, after)
	}
	latest := manager.Snapshot().Tunnels[0]
	if latest.Status != "connected" || !latest.Since.Equal(tunnel.Since) {
		t.Fatal("statistics interrupted the SSH connection")
	}
	t.Logf("native counters after %d echo bytes: %+v -> %+v", len(payload), before, after)
	start := time.Now()
	for range 20 {
		if _, err = client.Traffic(context.Background(), tunnel.Rule.ID); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("20 local mux queries averaged %s wall time each", time.Since(start)/20)
}
