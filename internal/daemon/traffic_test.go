package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sampleTraffic = "Connection to fixture\n traffic 239 pkts 8602 blks 137KB in, 226 pkts 6342 blks 102KB out\n"

func TestParseTraffic(t *testing.T) {
	for _, tc := range []struct{ info, received, sent string }{
		{sampleTraffic, "137KB", "102KB"},
		{"traffic 1 pkts 1 blks 64BB in, 2 pkts 3 blks 1.5MB out\r\n", "64B", "1.5MB"},
	} {
		got, err := parseTraffic(tc.info)
		if err != nil || got.Received != tc.received || got.Sent != tc.sent {
			t.Fatalf("%+v: %+v %v", tc, got, err)
		}
	}
	for _, info := range []string{"", "garbage", "traffic 1 pkts 1 blks ?? in, 2 pkts 3 blks 1KB out"} {
		if _, err := parseTraffic(info); err == nil {
			t.Fatalf("accepted malformed stats %q", info)
		}
	}
}

func TestMuxPacketBounds(t *testing.T) {
	for _, size := range []uint32{0, 3, maxStatsPacket + 1, ^uint32(0)} {
		if _, err := readMuxPacket(bytes.NewReader(muxWords(size))); err == nil {
			t.Fatalf("accepted frame length %d", size)
		}
	}
	if _, err := readMuxPacket(bytes.NewReader(append(muxWords(8), muxWords(1)...))); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated frame: %v", err)
	}
	if _, _, err := muxString(muxWords(^uint32(0))); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("unbounded string: %v", err)
	}
}

func statsServer(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	// Keep Unix socket paths below macOS's small path limit.
	dir, err := os.MkdirTemp("/tmp", "pf-stats-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		serve(conn)
	}()
	return path
}

func TestReadSSHTrafficProtocol(t *testing.T) {
	path := statsServer(t, func(conn net.Conn) {
		hello, err := readMuxPacket(conn)
		if err != nil || !bytes.Equal(hello, muxWords(muxHello, 4)) {
			return
		}
		greeting := append(muxWords(muxHello, 4, 4), []byte("info")...)
		greeting = append(greeting, muxWords(0)...)
		if writeMuxPacket(conn, greeting) != nil {
			return
		}
		request, err := readMuxPacket(conn)
		expected := append(muxWords(muxConnectionInfo, 1, 10), []byte("connection")...)
		if err != nil || !bytes.Equal(request, expected) {
			return
		}
		reply := append(muxWords(muxConnectionInfoReply, 1, 1, uint32(len(sampleTraffic))), []byte(sampleTraffic)...)
		_ = writeMuxPacket(conn, reply)
	})
	got, err := readSSHTraffic(context.Background(), path)
	if err != nil || got != (Traffic{Received: "137KB", Sent: "102KB"}) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestReadSSHTrafficUnsupportedAndTimeout(t *testing.T) {
	path := statsServer(t, func(conn net.Conn) {
		if _, err := readMuxPacket(conn); err != nil {
			return
		}
		_ = writeMuxPacket(conn, muxWords(muxHello, 4))
	})
	if _, err := readSSHTraffic(context.Background(), path); !errors.Is(err, ErrTrafficUnsupported) {
		t.Fatalf("old SSH: %v", err)
	}
	path = statsServer(t, func(conn net.Conn) {
		// A stalled master must not block the TUI worker indefinitely.
		_, _ = io.Copy(io.Discard, conn)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := readSSHTraffic(ctx, path); err == nil {
		t.Fatal("stalled master succeeded")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("deadline ignored: %s", elapsed)
	}
}

func TestClientTrafficRejectsInvalidID(t *testing.T) {
	c := NewClient(Paths{Runtime: t.TempDir()})
	for _, id := range []string{"../sock", "", "0123456789abcde/"} {
		if _, err := c.Traffic(context.Background(), id); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
}
