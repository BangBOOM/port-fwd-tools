package tui

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/BangBOOM/port-fwd-tools/internal/core"
	"github.com/BangBOOM/port-fwd-tools/internal/daemon"
	"github.com/charmbracelet/x/ansi"
)

func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }
func TestFormPasteAndDistinctPorts(t *testing.T) {
	m := New(nil)
	m.openForm(nil)
	updated, _ := m.Update(tea.PasteMsg{Content: "ssh -N -L 15432:localhost:5432 my-server"})
	m = updated.(Model)
	if m.form.fields[0].Value() != "ssh -N -L 15432:localhost:5432 my-server" {
		t.Fatalf("paste was lost: %q", m.form.fields[0].Value())
	}
	updated, _ = m.Update(key(tea.KeyEnter))
	m = updated.(Model)
	if m.form.fields[2].Value() != "15432" || m.form.fields[5].Value() != "5432" {
		t.Fatal("parsed local and remote ports were swapped")
	}
	view := ansi.Strip(m.View().Content)
	for _, label := range []string{"LOCAL", "REMOTE", "127.0.0.1:15432 → my-server → localhost:5432", "Ctrl+S"} {
		if !strings.Contains(view, label) {
			t.Fatalf("missing %q in form:\n%s", label, view)
		}
	}
}

func TestLayoutKeepsActionsVisibleAndEnglish(t *testing.T) {
	for _, size := range [][2]int{{54, 20}, {80, 24}, {100, 32}} {
		m := New(nil)
		m.width, m.height = size[0], size[1]
		m.message = "Deleted \"database\"."
		for i := 0; i < 15; i++ {
			m.state.Tunnels = append(m.state.Tunnels, daemon.Tunnel{Rule: core.Rule{ID: fmt.Sprintf("%016x", i), Name: fmt.Sprintf("database-%d", i), SSHHost: "my-server", LocalPort: 15432 + i, RemoteHost: "localhost", RemotePort: 5432, Enabled: true, AutoReconnect: true}, Status: "connected", Logs: []string{"Connected", "Listener ready"}})
		}
		m.selected = 14
		view := ansi.Strip(m.View().Content)
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("line overflows %v: %q", size, line)
			}
		}
		remoteHeader := "Remote destination"
		if size[0] < 68 {
			remoteHeader = "Remote target"
		}
		for _, s := range []string{"Local port", remoteHeader, "SSH", "Status", "15446", "localhost:5432", "my-server", "d Delete", "q Detach", "Deleted \"database\"."} {
			if !strings.Contains(view, s) {
				t.Fatalf("%v missing %q:\n%s", size, s, view)
			}
		}
		if strings.ContainsFunc(view, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
			t.Fatal("UI contains Chinese text")
		}
	}
}

func TestEditingStoppedRulePreservesIntent(t *testing.T) {
	r := core.Rule{ID: "0123456789abcdef", Name: "stopped", SSHHost: "my-server", LocalPort: 15432, RemoteHost: "localhost", RemotePort: 5432, Enabled: false, AutoReconnect: true}
	m := New(nil)
	m.openForm(&r)
	if m.form.original.Enabled {
		t.Fatal("editing would unexpectedly start a stopped tunnel")
	}
}
