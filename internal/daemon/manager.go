package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BangBOOM/port-fwd-tools/internal/core"
)

type Tunnel struct {
	Rule      core.Rule `json:"rule"`
	Status    string    `json:"status"`
	LastError string    `json:"last_error,omitempty"`
	Since     time.Time `json:"since"`
	NextRetry time.Time `json:"next_retry,omitempty"`
	Retries   int       `json:"retries"`
	Logs      []string  `json:"logs"`
}

type Snapshot struct {
	PID        int      `json:"pid"`
	ConfigPath string   `json:"config_path"`
	Tunnels    []Tunnel `json:"tunnels"`
}

type Request struct {
	Action      string    `json:"action"`
	ID          string    `json:"id,omitempty"`
	Rule        core.Rule `json:"rule,omitempty"`
	AgentSocket string    `json:"agent_socket,omitempty"`
}

type entry struct {
	tunnel Tunnel
	cancel context.CancelFunc
	done   chan struct{}
}

type Options struct {
	SSHBinary           string
	SSHConfig           string
	RetryBase, RetryMax time.Duration
	AliveInterval       int
}

type Manager struct {
	mu          sync.Mutex
	ops         sync.Mutex
	paths       Paths
	config      core.Config
	entries     map[string]*entry
	options     Options
	agentSocket string
	closed      bool
}

func NewManager(paths Paths, options Options) (*Manager, error) {
	c, err := core.Load(paths.Config)
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(paths.Config); os.IsNotExist(err) {
		if err = core.Save(paths.Config, c); err != nil {
			return nil, err
		}
	}
	if options.SSHBinary == "" {
		options.SSHBinary = "ssh"
	}
	if options.RetryBase == 0 {
		options.RetryBase = time.Second
	}
	if options.RetryMax == 0 {
		options.RetryMax = time.Minute
	}
	if options.AliveInterval == 0 {
		options.AliveInterval = 15
	}
	m := &Manager{paths: paths, config: c, options: options, entries: map[string]*entry{}, agentSocket: os.Getenv("SSH_AUTH_SOCK")}
	for _, r := range c.Rules {
		m.entries[r.ID] = newEntry(r, nil)
	}
	for _, r := range c.Rules {
		if r.Enabled {
			m.launch(m.entries[r.ID])
		}
	}
	return m, nil
}

func newEntry(r core.Rule, logs []string) *entry {
	return &entry{tunnel: Tunnel{Rule: r, Status: "stopped", Since: time.Now(), Logs: append([]string{}, logs...)}}
}

func (m *Manager) launch(e *entry) {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel, e.done = cancel, make(chan struct{})
	e.tunnel.Status = "connecting"
	go func() { defer close(e.done); m.run(ctx, e) }()
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Snapshot{PID: os.Getpid(), ConfigPath: m.paths.Config, Tunnels: []Tunnel{}}
	for _, r := range m.config.Rules {
		if e := m.entries[r.ID]; e != nil {
			t := e.tunnel
			t.Logs = append([]string{}, t.Logs...)
			s.Tunnels = append(s.Tunnels, t)
		}
	}
	return s
}

func (m *Manager) resolve(id string) (string, error) {
	for _, r := range m.config.Rules {
		if r.ID == id || r.Name == id {
			return r.ID, nil
		}
	}
	var found string
	for _, r := range m.config.Rules {
		if id != "" && strings.HasPrefix(r.ID, id) {
			if found != "" {
				return "", errors.New("ambiguous tunnel ID; use its full ID or name")
			}
			found = r.ID
		}
	}
	if found == "" {
		return "", fmt.Errorf("tunnel %q not found", id)
	}
	return found, nil
}

// Apply serializes mutations, persists intent, then replaces only the affected worker.
func (m *Manager) Apply(req Request) error {
	m.ops.Lock()
	defer m.ops.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("daemon is shutting down")
	}
	if req.AgentSocket != "" {
		m.agentSocket = req.AgentSocket
	}
	if req.Action == "state" {
		m.mu.Unlock()
		return nil
	}
	c := core.Config{Version: 1, Rules: append([]core.Rule{}, m.config.Rules...)}
	id := req.ID
	var err error
	var old *entry
	var replacement *core.Rule
	if req.Action == "add" {
		r := req.Rule
		r.ID = core.NewID()
		id = r.ID
		if strings.TrimSpace(r.Name) == "" {
			r.Name = fmt.Sprintf("%s:%d", r.SSHHost, r.LocalPort)
		}
		c.Rules = append(c.Rules, r)
		replacement = &r
	} else {
		id, err = m.resolve(id)
		if err != nil {
			m.mu.Unlock()
			return err
		}
		old = m.entries[id]
		for i, r := range c.Rules {
			if r.ID != id {
				continue
			}
			switch req.Action {
			case "update":
				r = req.Rule
				r.ID = id
			case "start", "restart":
				r.Enabled = true
			case "stop":
				r.Enabled = false
			case "delete":
				c.Rules = append(c.Rules[:i], c.Rules[i+1:]...)
			default:
				m.mu.Unlock()
				return fmt.Errorf("unknown action %q", req.Action)
			}
			if req.Action != "delete" {
				c.Rules[i] = r
				replacement = &r
			}
			break
		}
	}
	m.mu.Unlock()
	if err = core.Save(m.paths.Config, c); err != nil {
		return err
	}
	if old != nil && old.cancel != nil {
		old.cancel()
		<-old.done
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = c
	delete(m.entries, id)
	if replacement != nil {
		var logs []string
		if old != nil {
			logs = old.tunnel.Logs
		}
		e := newEntry(*replacement, logs)
		m.entries[id] = e
		if replacement.Enabled {
			m.launch(e)
		} else {
			e.tunnel.Logs = appendLog(e.tunnel.Logs, "Stopped by user; stays stopped until manually started")
		}
	}
	return nil
}

func (m *Manager) Close() {
	m.ops.Lock()
	defer m.ops.Unlock()
	m.mu.Lock()
	m.closed = true
	var workers []*entry
	for _, e := range m.entries {
		if e.cancel != nil {
			e.cancel()
			workers = append(workers, e)
		}
	}
	m.mu.Unlock()
	for _, e := range workers {
		<-e.done
	}
}

func (m *Manager) set(e *entry, status, lastError string, retryAt time.Time, attempts int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.tunnel.Status != status {
		e.tunnel.Since = time.Now()
	}
	e.tunnel.Status, e.tunnel.LastError, e.tunnel.NextRetry, e.tunnel.Retries = status, lastError, retryAt, attempts
}

func appendLog(logs []string, line string) []string {
	logs = append(logs, time.Now().Format("15:04:05")+"  "+line)
	if len(logs) > 80 {
		logs = logs[len(logs)-80:]
	}
	return logs
}

func (m *Manager) log(e *entry, line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.tunnel.Logs = appendLog(e.tunnel.Logs, line)
}

func (m *Manager) controlPath(r core.Rule) string { return filepath.Join(m.paths.Runtime, "ssh-"+r.ID) }
