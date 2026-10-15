//go:build darwin || linux

package daemon

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/BangBOOM/port-fwd-tools/internal/core"
	"github.com/charmbracelet/x/ansi"
)

func (m *Manager) baseArgs() []string {
	if m.options.SSHConfig != "" {
		return []string{"-F", m.options.SSHConfig}
	}
	return nil
}

func (m *Manager) sshArgs(r core.Rule) []string {
	a := append(m.baseArgs(), "-N", "-T", "-n", "-M", "-S", m.controlPath(r))
	for _, opt := range []string{
		"BatchMode=yes", "StrictHostKeyChecking=yes", "ExitOnForwardFailure=yes",
		"PasswordAuthentication=no", "KbdInteractiveAuthentication=no",
		"ServerAliveInterval=" + strconv.Itoa(m.options.AliveInterval), "ServerAliveCountMax=3",
		"ConnectTimeout=10", "ConnectionAttempts=1", "ForkAfterAuthentication=no",
		"ControlMaster=yes", "ControlPersist=no", "LogLevel=ERROR",
	} {
		a = append(a, "-o", opt)
	}
	return append(a, "-L", r.ForwardSpec(), "--", r.SSHHost)
}

func (m *Manager) control(r core.Rule, op string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a := append(m.baseArgs(), "-S", m.controlPath(r), "-O", op, "--", r.SSHHost)
	return exec.CommandContext(ctx, m.options.SSHBinary, a...).Run() == nil
}

func (m *Manager) run(ctx context.Context, e *entry) {
	r := e.tunnel.Rule
	// Reclaim only this rule's private master after a previous daemon crash.
	if _, err := os.Stat(m.controlPath(r)); err == nil {
		if m.control(r, "exit") {
			time.Sleep(100 * time.Millisecond)
		}
		os.Remove(m.controlPath(r))
	}
	defer os.Remove(m.controlPath(r))
	attempts := 0
	for ctx.Err() == nil {
		started := time.Now()
		m.set(e, "connecting", "", time.Time{}, attempts)
		m.log(e, fmt.Sprintf("Connecting: local %d -> %s:%d via %s", r.LocalPort, r.RemoteHost, r.RemotePort, r.SSHHost))
		lastError, connected := m.attempt(ctx, e)
		if ctx.Err() != nil {
			return
		}
		if lastError == "" {
			lastError = "SSH exited unexpectedly"
		}
		if !r.AutoReconnect || needsAttention(lastError) {
			m.set(e, "needs_attention", lastError, time.Time{}, attempts)
			m.log(e, "Automatic retries paused; fix the error, then press r to reconnect")
			return
		}
		if connected && time.Since(started) > 30*time.Second {
			attempts = 0
		}
		attempts++
		delay := m.options.RetryBase
		for i := 1; i < attempts && delay < m.options.RetryMax; i++ {
			delay *= 2
		}
		if delay > m.options.RetryMax {
			delay = m.options.RetryMax
		}
		// Jitter downwards so the configured maximum remains a ceiling.
		delay = time.Duration(float64(delay) * (.8 + rand.Float64()*.2))
		m.set(e, "retrying", lastError, time.Now().Add(delay), attempts)
		m.log(e, fmt.Sprintf("Retry #%d in %s: %s", attempts, delay.Round(time.Millisecond), lastError))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func terminate(cmd *exec.Cmd, done <-chan error) {
	// A separate process group also owns ProxyJump/ProxyCommand children.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-done:
		return
	case <-time.After(700 * time.Millisecond):
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-done
}

func (m *Manager) attempt(ctx context.Context, e *entry) (string, bool) {
	r := e.tunnel.Rule
	cmd := exec.Command(m.options.SSHBinary, m.sshArgs(r)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A proxy child retaining stderr must not hide the master's exit forever.
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "LC_ALL=C", "SSH_ASKPASS_REQUIRE=never")
	m.mu.Lock()
	agent := m.agentSocket
	m.mu.Unlock()
	if agent != "" {
		cmd.Env = append(cmd.Env, "SSH_AUTH_SOCK="+agent)
	}
	w := &sshOutput{emit: func(s string) { m.log(e, s) }}
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		return "Cannot start SSH: " + err.Error(), false
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	startup := time.NewTimer(35 * time.Second)
	defer startup.Stop()
	connected := false
	for {
		select {
		case <-ctx.Done():
			terminate(cmd, done)
			return "", connected
		case err := <-done:
			w.flush()
			msg := w.last()
			if msg == "" && err != nil {
				msg = err.Error()
			}
			return msg, connected
		case <-startup.C:
			if !connected {
				terminate(cmd, done)
				return "SSH startup timed out (authentication or network)", false
			}
		case <-tick.C:
			if connected {
				continue
			}
			if _, err := os.Stat(m.controlPath(r)); err == nil && m.control(r, "check") {
				connected = true
				startup.Stop()
				tick.Stop()
				m.set(e, "connected", "", time.Time{}, 0)
				m.log(e, "SSH connected; local listener ready. Service health is not checked.")
			}
		}
	}
}

func needsAttention(message string) bool {
	s := strings.ToLower(message)
	for _, part := range []string{"permission denied", "host key verification failed", "remote host identification has changed", "address already in use", "cannot assign requested address", "could not request local forwarding", "bad configuration", "bad owner", "bad permissions", "invalid configuration", "unsupported option", "cannot start ssh", "no such identity", "no matching host key", "no matching key exchange", "too many authentication failures", "load key"} {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}

// SSH stderr is bounded and stripped of terminal controls before entering the TUI.
type sshOutput struct {
	mu            sync.Mutex
	pending, tail string
	emit          func(string)
}

func sanitize(s string) string {
	s = ansi.Strip(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if len(s) > 1500 {
		s = s[:1500] + "…"
	}
	return s
}
func (w *sshOutput) line(s string) {
	s = sanitize(strings.TrimSpace(s))
	if s == "" {
		return
	}
	w.tail += s + "; "
	if len(w.tail) > 6000 {
		w.tail = w.tail[len(w.tail)-6000:]
	}
	w.emit(s)
}
func (w *sshOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending += string(p)
	for {
		i := strings.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		w.line(w.pending[:i])
		w.pending = w.pending[i+1:]
	}
	if len(w.pending) > 4096 {
		w.line(w.pending)
		w.pending = ""
	}
	return len(p), nil
}
func (w *sshOutput) flush() { w.mu.Lock(); defer w.mu.Unlock(); w.line(w.pending); w.pending = "" }
func (w *sshOutput) last() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSuffix(w.tail, "; ")
}
