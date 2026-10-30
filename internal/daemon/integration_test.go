package daemon

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BangBOOM/port-fwd-tools/internal/core"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type sshFixture struct {
	listener    net.Listener
	config      *ssh.ServerConfig
	mu          sync.Mutex
	connections map[*ssh.ServerConn]bool
}

func newSSHFixture(t *testing.T, dir string) (*sshFixture, string) {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("OpenSSH client not installed")
	}
	_, hostKey, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(hostKey)
	_, clientKey, _ := ed25519.GenerateKey(rand.Reader)
	clientSigner, _ := ssh.NewSignerFromKey(clientKey)
	block, err := ssh.MarshalPrivateKey(clientKey, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "identity")
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &sshFixture{listener: listener, connections: map[*ssh.ServerConn]bool{}, config: &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(key.Marshal(), clientSigner.PublicKey().Marshal()) {
			return nil, nil
		}
		return nil, fmt.Errorf("unknown key")
	}}}
	f.config.AddHostKey(hostSigner)
	knownPath := filepath.Join(dir, "known_hosts")
	if err = os.WriteFile(knownPath, []byte(knownhosts.Line([]string{listener.Addr().String()}, hostSigner.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	configPath := filepath.Join(dir, "ssh_config")
	config := fmt.Sprintf("Host fixture\n HostName 127.0.0.1\n Port %s\n User fixture\n IdentityFile %q\n IdentitiesOnly yes\n UserKnownHostsFile %q\n GlobalKnownHostsFile /dev/null\n", port, keyPath, knownPath)
	if err = os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	t.Cleanup(func() { listener.Close(); f.drop() })
	return f, configPath
}

func (f *sshFixture) serve(raw net.Conn) {
	conn, channels, requests, err := ssh.NewServerConn(raw, f.config)
	if err != nil {
		raw.Close()
		return
	}
	f.mu.Lock()
	f.connections[conn] = true
	f.mu.Unlock()
	defer func() { conn.Close(); f.mu.Lock(); delete(f.connections, conn); f.mu.Unlock() }()
	go func() {
		for req := range requests {
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		}
	}()
	for request := range channels {
		if request.ChannelType() != "direct-tcpip" {
			_ = request.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		var target struct {
			Host       string
			Port       uint32
			OriginHost string
			OriginPort uint32
		}
		if ssh.Unmarshal(request.ExtraData(), &target) != nil {
			_ = request.Reject(ssh.ConnectionFailed, "invalid destination")
			continue
		}
		remote, err := net.DialTimeout("tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))), time.Second)
		if err != nil {
			_ = request.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		channel, reqs, err := request.Accept()
		if err != nil {
			remote.Close()
			continue
		}
		go ssh.DiscardRequests(reqs)
		go func() {
			defer channel.Close()
			defer remote.Close()
			go func() { _, _ = io.Copy(channel, remote); channel.Close() }()
			_, _ = io.Copy(remote, channel)
		}()
	}
}
func (f *sshFixture) drop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for conn := range f.connections {
		conn.Close()
	}
}
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
func waitFor(t *testing.T, m *Manager, name, status string) Tunnel {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		for _, tunnel := range m.Snapshot().Tunnels {
			if tunnel.Rule.Name == name && tunnel.Status == status {
				return tunnel
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s / %s: %+v", name, status, m.Snapshot())
	return Tunnel{}
}

func TestRealSSHForwardReconnectStopAndReload(t *testing.T) {
	dir := t.TempDir()
	fixture, configPath := newSSHFixture(t, dir)
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	paths, err := ResolvePaths(filepath.Join(dir, "home"))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(paths.Runtime)
	options := Options{SSHConfig: configPath, RetryBase: 100 * time.Millisecond, RetryMax: 200 * time.Millisecond, AliveInterval: 1}
	m, err := NewManager(paths, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { m.Close() }()
	r := core.Rule{Name: "echo", SSHHost: "fixture", LocalPort: freePort(t), RemoteHost: "127.0.0.1", RemotePort: echo.Addr().(*net.TCPAddr).Port, Enabled: true, AutoReconnect: true}
	if err = m.Apply(Request{Action: "add", Rule: r}); err != nil {
		t.Fatal(err)
	}
	first := waitFor(t, m, "echo", "connected")
	checkForward := func() {
		t.Helper()
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", r.LocalPort), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = c.Write([]byte("port-forward-test"))
		got := make([]byte, len("port-forward-test"))
		if _, err = io.ReadFull(c, got); err != nil || string(got) != "port-forward-test" {
			t.Fatalf("forward failed: %q %v", got, err)
		}
	}
	checkForward()
	fixture.drop()
	deadline := time.Now().Add(8 * time.Second)
	reconnected := false
	for time.Now().Before(deadline) {
		s := m.Snapshot()
		if s.Tunnels[0].Status == "connected" && s.Tunnels[0].Since.After(first.Since) {
			reconnected = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !reconnected {
		t.Fatalf("did not reconnect: %+v", m.Snapshot())
	}
	checkForward()
	// Enabled intent resumes without opening the TUI after daemon restart.
	m.Close()
	m, err = NewManager(paths, options)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "echo", "connected")
	checkForward()
	if err = m.Apply(Request{Action: "stop", ID: "echo"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "echo", "stopped")
	time.Sleep(350 * time.Millisecond)
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", r.LocalPort), 100*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("manual stop left listener running")
	}
	m.Close()
	m, err = NewManager(paths, options)
	if err != nil {
		t.Fatal(err)
	}
	if s := m.Snapshot(); s.Tunnels[0].Rule.Enabled || s.Tunnels[0].Status != "stopped" {
		t.Fatalf("manual stop not durable: %+v", s)
	}
	if err = m.Apply(Request{Action: "start", ID: "echo"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "echo", "connected")
	checkForward()
	rule := m.Snapshot().Tunnels[0].Rule
	rule.LocalPort = freePort(t)
	if err = m.Apply(Request{Action: "update", ID: rule.ID, Rule: rule}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "echo", "connected")
	oldPort := r.LocalPort
	r.LocalPort = rule.LocalPort
	checkForward()
	if oldPort != r.LocalPort {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", oldPort), 100*time.Millisecond); err == nil {
			c.Close()
			t.Fatal("edit left the old listener running")
		}
	}
	if err = m.Apply(Request{Action: "delete", ID: "echo"}); err != nil {
		t.Fatal(err)
	}
	if len(m.Snapshot().Tunnels) != 0 {
		t.Fatal("delete did not remove rule")
	}
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", r.LocalPort), 100*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("delete left the local listener running")
	}
	config, err := core.Load(paths.Config)
	if err != nil || len(config.Rules) != 0 {
		t.Fatalf("deleted rule remained in saved configuration: %+v %v", config, err)
	}
}

func TestPortConflictAndAuthenticationPause(t *testing.T) {
	dir := t.TempDir()
	_, configPath := newSSHFixture(t, dir)
	paths, err := ResolvePaths(filepath.Join(dir, "home"))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(paths.Runtime)
	m, err := NewManager(paths, Options{SSHConfig: configPath, RetryBase: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	r := core.Rule{Name: "conflict", SSHHost: "fixture", LocalPort: occupied.Addr().(*net.TCPAddr).Port, RemoteHost: "localhost", RemotePort: 5432, Enabled: true, AutoReconnect: true}
	if err = m.Apply(Request{Action: "add", Rule: r}); err != nil {
		t.Fatal(err)
	}
	tunnel := waitFor(t, m, "conflict", "needs_attention")
	if !strings.Contains(strings.ToLower(tunnel.LastError), "forward") {
		t.Fatalf("wrong error: %s", tunnel.LastError)
	}
	// Replacing the trusted identity makes real OpenSSH fail noninteractively.
	_, wrongKey, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(wrongKey, "")
	if err = os.WriteFile(filepath.Join(dir, "identity"), pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	r.Name = "auth"
	r.LocalPort = freePort(t)
	if err = m.Apply(Request{Action: "add", Rule: r}); err != nil {
		t.Fatal(err)
	}
	tunnel = waitFor(t, m, "auth", "needs_attention")
	if !strings.Contains(tunnel.LastError, "Permission denied") {
		t.Fatalf("wrong auth error: %s", tunnel.LastError)
	}
	time.Sleep(200 * time.Millisecond)
	for _, x := range m.Snapshot().Tunnels {
		if x.Retries != 0 {
			t.Fatalf("nonrecoverable errors retried: %+v", x)
		}
	}
}
