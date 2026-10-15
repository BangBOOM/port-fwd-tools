package daemon

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Traffic contains OpenSSH's rounded, connection-lifetime transport counters.
// It intentionally does not expose rates derived from rounded values.
type Traffic struct{ Received, Sent string }

var ErrTrafficUnsupported = errors.New("OpenSSH connection statistics are not supported")

const (
	muxHello               = 0x00000001
	muxConnectionInfo      = 0x20000001
	muxConnectionInfoReply = 0x90000001
	maxStatsPacket         = 32 << 10
)

var trafficLine = regexp.MustCompile(`(?m)^[ \t]*traffic[ \t]+\d+[ \t]+pkts[ \t]+\d+[ \t]+blks[ \t]+([0-9]+(?:\.[0-9]+)?[BKMGTEP]+)[ \t]+in,[ \t]+\d+[ \t]+pkts[ \t]+\d+[ \t]+blks[ \t]+([0-9]+(?:\.[0-9]+)?[BKMGTEP]+)[ \t]+out[ \t]*\r?$`)

func parseTraffic(info string) (Traffic, error) {
	parts := trafficLine.FindStringSubmatch(info)
	if len(parts) != 3 {
		return Traffic{}, errors.New("unrecognized OpenSSH traffic counters")
	}
	// Some OpenSSH versions append B to fmt_scaled's byte-sized B suffix.
	return Traffic{Received: normalizeCounter(parts[1]), Sent: normalizeCounter(parts[2])}, nil
}

func normalizeCounter(value string) string {
	if strings.HasSuffix(value, "BB") {
		return strings.TrimSuffix(value, "B")
	}
	return value
}

func muxWords(words ...uint32) []byte {
	b := make([]byte, 4*len(words))
	for i, v := range words {
		binary.BigEndian.PutUint32(b[i*4:], v)
	}
	return b
}
func writeMuxPacket(w io.Writer, payload []byte) error {
	packet := append(muxWords(uint32(len(payload))), payload...)
	for len(packet) > 0 {
		n, err := w.Write(packet)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		packet = packet[n:]
	}
	return nil
}
func readMuxPacket(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size < 4 || size > maxStatsPacket {
		return nil, errors.New("invalid SSH control packet size")
	}
	packet := make([]byte, size)
	_, err := io.ReadFull(r, packet)
	return packet, err
}
func muxString(packet []byte) (string, []byte, error) {
	if len(packet) < 4 {
		return "", nil, io.ErrUnexpectedEOF
	}
	size := binary.BigEndian.Uint32(packet[:4])
	if uint64(size) > uint64(len(packet)-4) {
		return "", nil, io.ErrUnexpectedEOF
	}
	return string(packet[4 : 4+size]), packet[4+size:], nil
}

// readSSHTraffic queries the existing master over its local Unix socket. No
// subprocess, packet capture, proxy, or data-path instrumentation is involved.
func readSSHTraffic(ctx context.Context, path string) (Traffic, error) {
	conn, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "unix", path)
	if err != nil {
		return Traffic{}, err
	}
	defer conn.Close()
	deadline := time.Now().Add(100 * time.Millisecond)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return Traffic{}, err
	}
	if err = writeMuxPacket(conn, muxWords(muxHello, 4)); err != nil {
		return Traffic{}, err
	}
	hello, err := readMuxPacket(conn)
	if err != nil {
		return Traffic{}, err
	}
	if len(hello) < 8 || binary.BigEndian.Uint32(hello) != muxHello || binary.BigEndian.Uint32(hello[4:]) != 4 {
		return Traffic{}, ErrTrafficUnsupported
	}
	supportsInfo := false
	for extensions := hello[8:]; len(extensions) > 0; {
		var name string
		name, extensions, err = muxString(extensions)
		if err != nil {
			return Traffic{}, err
		}
		_, extensions, err = muxString(extensions)
		if err != nil {
			return Traffic{}, err
		}
		if name == "info" {
			supportsInfo = true
		}
	}
	if !supportsInfo {
		return Traffic{}, ErrTrafficUnsupported
	}
	request := append(muxWords(muxConnectionInfo, 1, 10), []byte("connection")...)
	if err = writeMuxPacket(conn, request); err != nil {
		return Traffic{}, err
	}
	reply, err := readMuxPacket(conn)
	if err != nil {
		return Traffic{}, err
	}
	if len(reply) < 12 || binary.BigEndian.Uint32(reply) != muxConnectionInfoReply || binary.BigEndian.Uint32(reply[4:]) != 1 {
		return Traffic{}, errors.New("unexpected SSH statistics reply")
	}
	if binary.BigEndian.Uint32(reply[8:]) != 1 {
		return Traffic{}, ErrTrafficUnsupported
	}
	info, _, err := muxString(reply[12:])
	if err != nil {
		return Traffic{}, err
	}
	return parseTraffic(info)
}

func (c *Client) Traffic(ctx context.Context, id string) (Traffic, error) {
	if len(id) != 16 || strings.Trim(id, "0123456789abcdef") != "" {
		return Traffic{}, fmt.Errorf("invalid tunnel ID")
	}
	return readSSHTraffic(ctx, filepath.Join(c.runtime, "ssh-"+id))
}
