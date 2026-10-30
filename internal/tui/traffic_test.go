package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/BangBOOM/port-fwd-tools/internal/core"
	"github.com/BangBOOM/port-fwd-tools/internal/daemon"
	"github.com/charmbracelet/x/ansi"
)

func trafficModel(t *testing.T) Model {
	t.Helper()
	m := New(daemon.NewClient(daemon.Paths{Runtime: t.TempDir()}))
	for i := range 30 {
		m.state.Tunnels = append(m.state.Tunnels, daemon.Tunnel{Rule: core.Rule{ID: fmt.Sprintf("%016x", i), Name: fmt.Sprintf("database-%d", i), SSHHost: "my-server", LocalPort: 15432 + i, RemoteHost: "localhost", RemotePort: 5432, Enabled: true}, Status: "connected", Since: time.Unix(100, 0)})
	}
	m.selected = 29
	return m
}

func TestTrafficOnlySamplesVisibleRowsAndThrottles(t *testing.T) {
	m := trafficModel(t)
	start, end := m.listRange(m.height - 2)
	// A stopped and an unsupported visible row should incur no socket queries.
	m.state.Tunnels[start].Status = "stopped"
	unsupported := m.state.Tunnels[start+1]
	m.traffic = map[string]trafficSample{unsupported.Rule.ID: {target: trafficTarget{id: unsupported.Rule.ID, since: unsupported.Since}, unsupported: true, unavailable: true}}
	cmd := m.pollTraffic()
	if cmd == nil || !m.trafficBusy {
		t.Fatal("no collector scheduled")
	}
	if m.pollTraffic() != nil {
		t.Fatal("collector overlapped an in-flight batch")
	}
	samples := cmd().(trafficResult)
	if len(samples) != end-start-2 {
		t.Fatalf("sampled %d rows, expected %d", len(samples), end-start-2)
	}
	for i, sample := range samples {
		if sample.target.id != m.state.Tunnels[start+2+i].Rule.ID {
			t.Fatalf("queried an invisible or skipped tunnel: %+v", sample)
		}
	}
	m.receiveTraffic(samples)
	if m.trafficBusy || m.pollTraffic() != nil {
		t.Fatal("two-second throttle was lost")
	}
	// Update returns the model with in-flight tracking intact (Model is a value).
	m.nextTraffic = time.Time{}
	updated, cmd := m.Update(result{state: m.state})
	if cmd == nil || !updated.(Model).trafficBusy {
		t.Fatal("Update discarded collector state")
	}
}

func TestTrafficPausesOutsideVisibleList(t *testing.T) {
	for _, mode := range []string{"blur", "form", "delete", "logs", "help", "busy", "offline", "small"} {
		t.Run(mode, func(t *testing.T) {
			m := trafficModel(t)
			switch mode {
			case "blur":
				updated, _ := m.Update(tea.BlurMsg{})
				m = updated.(Model)
			case "form":
				m.openForm(nil)
			case "delete":
				rule := m.state.Tunnels[0].Rule
				m.deleting = &rule
			case "logs":
				m.logs = true
			case "help":
				m.help = true
			case "busy":
				m.busy = true
			case "offline":
				m.connected = false
			case "small":
				m.width = 40
			}
			if m.pollTraffic() != nil || m.trafficBusy {
				t.Fatal("collector ran while list was not active")
			}
		})
	}
}

func TestTrafficDiscardsPreviousConnectionAndDeletedRules(t *testing.T) {
	m := trafficModel(t)
	tunnel := m.state.Tunnels[0]
	sample := trafficSample{target: trafficTarget{id: tunnel.Rule.ID, since: tunnel.Since}, counters: daemon.Traffic{Received: "2MB", Sent: "1MB"}}
	m.receiveTraffic(trafficResult{sample})
	if m.trafficCell(tunnel) != "2MB / 1MB" {
		t.Fatal("missing counters")
	}
	m.state.Tunnels[0].Since = tunnel.Since.Add(time.Second)
	m.pruneTraffic()
	m.receiveTraffic(trafficResult{sample})
	if _, ok := m.traffic[tunnel.Rule.ID]; ok {
		t.Fatal("accepted stale counts from previous SSH connection")
	}
	if m.trafficCell(m.state.Tunnels[0]) != "…" {
		t.Fatal("reconnected tunnel displayed old counters")
	}
	m.state.Tunnels[0] = tunnel
	m.receiveTraffic(trafficResult{sample})
	m.state.Tunnels = nil
	m.pruneTraffic()
	if len(m.traffic) != 0 {
		t.Fatal("deleted rules retained statistics")
	}
}

func TestTrafficLayoutRetainsBothPorts(t *testing.T) {
	for _, width := range []int{54, 69, 70, 80, 100} {
		m := trafficModel(t)
		m.width = width
		tunnel := m.state.Tunnels[m.selected]
		m.receiveTraffic(trafficResult{{target: trafficTarget{id: tunnel.Rule.ID, since: tunnel.Since}, counters: daemon.Traffic{Received: "2.0MB", Sent: "1.0MB"}}})
		view := ansi.Strip(m.View().Content)
		expected := "2.0MB / 1.0MB"
		if width < 70 {
			expected = "SSH totals ↓2.0MB ↑1.0MB"
		}
		for _, text := range []string{expected, "15461", "localhost:5432", "q Detach"} {
			if !strings.Contains(view, text) {
				t.Fatalf("width %d missing %q:\n%s", width, text, view)
			}
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("line overflows width %d: %q", width, line)
			}
		}
	}
}
