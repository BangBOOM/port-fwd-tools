package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/BangBOOM/port-fwd-tools/internal/core"
	"github.com/BangBOOM/port-fwd-tools/internal/daemon"
	"github.com/charmbracelet/x/ansi"
)

func deletionModel() Model {
	m := New(nil)
	for i := range 3 {
		m.state.Tunnels = append(m.state.Tunnels, daemon.Tunnel{
			Rule:   core.Rule{ID: fmt.Sprintf("%016x", i+1), Name: fmt.Sprintf("database-%d", i), SSHHost: "my-server", LocalPort: 15432 + i, RemoteHost: "localhost", RemotePort: 5432, Enabled: true, AutoReconnect: true},
			Status: "connected",
		})
	}
	return m
}

func beginDelete(t *testing.T, m Model) Model {
	t.Helper()
	updated, cmd := m.Update(key('d'))
	m = updated.(Model)
	if cmd != nil || m.deleting == nil || m.busy {
		t.Fatal("opening confirmation issued an action")
	}
	return m
}

func deletionClient(t *testing.T, state daemon.Snapshot, failure string) (*daemon.Client, <-chan daemon.Request) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pf-delete-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "daemon.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan daemon.Request, 4)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req daemon.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "invalid request", 400)
			return
		}
		requests <- req
		w.Header().Set("Content-Type", "application/json")
		if failure != "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": failure})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": state})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Close() })
	return daemon.NewClient(daemon.Paths{Socket: path, Runtime: dir}), requests
}

func TestDeleteConfirmationKeepsLayoutAndPorts(t *testing.T) {
	for _, theme := range []string{core.ThemeTerminal, core.ThemeDracula} {
		for _, size := range [][2]int{{54, 20}, {80, 24}, {100, 32}} {
			m := deletionModel()
			m.width, m.height = size[0], size[1]
			m.theme = theme
			m.styles = newStyles(theme)
			for _, enabled := range []bool{true, false} {
				m.state.Tunnels[0].Rule.Enabled = enabled
				view := ansi.Strip(m.View().Content)
				if !strings.Contains(view, "d Delete") || !strings.Contains(view, "q Detach") {
					t.Fatalf("actions clipped at %v:\n%s", size, view)
				}
			}
			first, last := m.listRange(m.height - 2)
			m = beginDelete(t, m)
			afterFirst, afterLast := m.listRange(m.height - 2)
			if first != afterFirst || last != afterLast {
				t.Fatal("confirmation reduced list space")
			}
			view := ansi.Strip(m.View().Content)
			for _, label := range []string{`Delete "database-0"?`, "Local 15432 → my-server → localhost:5432", "Stops forwarding and removes the saved rule.", "y Delete · n / Esc Cancel"} {
				if !strings.Contains(view, label) {
					t.Fatalf("%s %v missing %q:\n%s", theme, size, label, view)
				}
			}
			for _, line := range strings.Split(view, "\n") {
				if ansi.StringWidth(line) > m.width {
					t.Fatalf("confirmation overflow: %q", line)
				}
			}
		}
	}
}

func TestDeleteCancellationAndNavigation(t *testing.T) {
	for _, press := range []rune{'n', 'N', 27, tea.KeyEnter} {
		m := beginDelete(t, deletionModel())
		updated, cmd := m.Update(key('j'))
		m = updated.(Model)
		if cmd != nil || m.selected != 0 || m.deleting.ID != m.state.Tunnels[0].Rule.ID {
			t.Fatal("navigation changed delete target")
		}
		updated, cmd = m.Update(key(press))
		m = updated.(Model)
		if cmd != nil || m.deleting != nil || m.busy || len(m.state.Tunnels) != 3 {
			t.Fatalf("cancel %v changed saved rules", press)
		}
	}
}

func TestDeleteUsesFrozenIdentityAndShowsSuccess(t *testing.T) {
	m := beginDelete(t, deletionModel())
	target := *m.deleting
	original := m.state.Tunnels
	refreshed := daemon.Snapshot{Tunnels: []daemon.Tunnel{original[2], original[0], original[1]}}
	refreshed.Tunnels[1].Rule.Name = "renamed-in-another-client"
	updated, _ := m.Update(result{state: refreshed})
	m = updated.(Model)
	if m.selected != 1 || m.deleting.Name != target.Name || m.deleting.ID != target.ID {
		t.Fatal("state refresh changed confirmation identity")
	}
	remaining := daemon.Snapshot{Tunnels: []daemon.Tunnel{refreshed.Tunnels[0], refreshed.Tunnels[2]}}
	client, requests := deletionClient(t, remaining, "")
	m.client = client
	updated, cmd := m.Update(key('y'))
	m = updated.(Model)
	if cmd == nil || !m.busy {
		t.Fatal("confirmation did not submit deletion")
	}
	duplicate, duplicateCmd := m.Update(key('y'))
	if duplicateCmd != nil || !duplicate.(Model).busy {
		t.Fatal("duplicate deletion submitted")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	req := <-requests
	if req.Action != "delete" || req.ID != target.ID {
		t.Fatalf("wrong target: %+v", req)
	}
	if m.busy || m.deleting != nil || m.message != `Deleted "database-0".` || m.chosen().Rule.ID != original[1].Rule.ID {
		t.Fatalf("incorrect success state: %+v", m)
	}
	m.noticeUntil = time.Now().Add(-time.Second)
	updated, _ = m.Update(tick(time.Now()))
	if updated.(Model).message != "" {
		t.Fatal("success notice did not expire")
	}
}

func TestDeleteFailureAllowsRetryAndOfflineCancel(t *testing.T) {
	m := beginDelete(t, deletionModel())
	client, requests := deletionClient(t, daemon.Snapshot{}, "configuration is read-only")
	m.client = client
	updated, cmd := m.Update(key('y'))
	m = updated.(Model)
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	<-requests
	if m.busy || m.deleting == nil || len(m.state.Tunnels) != 3 {
		t.Fatal("failure lost the confirmation or rules")
	}
	view := ansi.Strip(m.View().Content)
	for _, label := range []string{"Delete failed: configuration is read-only", "y Retry delete", "n / Esc Cancel"} {
		if !strings.Contains(view, label) {
			t.Fatalf("missing failure action %q:\n%s", label, view)
		}
	}
	updated, cmd = m.Update(key('y'))
	if cmd == nil || !updated.(Model).busy {
		t.Fatal("failure cannot be retried")
	}
	// A separate state read may discover the daemon is unreachable.
	updated, _ = m.Update(result{err: errors.New("cannot reach daemon: connection refused")})
	m = updated.(Model)
	updated, cmd = m.Update(key('y'))
	if cmd != nil || updated.(Model).busy {
		t.Fatal("offline confirmation submitted a mutation")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "Daemon offline. Cancel or wait for reconnect.") {
		t.Fatal("offline confirmation lost its reason")
	}
	updated, _ = m.Update(key(27))
	if updated.(Model).deleting != nil {
		t.Fatal("offline confirmation cannot be cancelled")
	}
}

func TestDeleteSelectionAndExternallyRemovedTarget(t *testing.T) {
	for _, index := range []int{0, 1, 2} {
		m := deletionModel()
		m.selected = index
		m = beginDelete(t, m)
		original := m.state.Tunnels
		remaining := append(append([]daemon.Tunnel{}, original[:index]...), original[index+1:]...)
		updated, _ := m.Update(result{state: daemon.Snapshot{Tunnels: remaining}, mutation: true, action: "delete"})
		m = updated.(Model)
		if m.chosen().Rule.ID != remaining[min(index, len(remaining)-1)].Rule.ID {
			t.Fatal("deletion did not select the next or previous row")
		}
	}
	m := deletionModel()
	m.state.Tunnels = m.state.Tunnels[:1]
	m = beginDelete(t, m)
	updated, _ := m.Update(result{state: daemon.Snapshot{}, mutation: true, action: "delete"})
	m = updated.(Model)
	if m.deleting != nil || m.chosen() != nil || !strings.Contains(ansi.Strip(m.View().Content), "No tunnels yet.") {
		t.Fatal("deleting last rule did not show empty state")
	}
	m = beginDelete(t, deletionModel())
	updated, _ = m.Update(result{state: daemon.Snapshot{Tunnels: m.state.Tunnels[1:]}})
	m = updated.(Model)
	if m.deleting != nil || m.message != `Tunnel "database-0" no longer exists.` {
		t.Fatal("externally removed target left an active confirmation")
	}
	updated, cmd := m.Update(key('y'))
	if cmd != nil || updated.(Model).busy {
		t.Fatal("confirmation deleted a different row after target disappeared")
	}
}

func TestDeleteCannotBeConfirmedWithHiddenPanel(t *testing.T) {
	for _, size := range [][2]int{{53, 24}, {80, 19}} {
		m := deletionModel()
		m.width, m.height = size[0], size[1]
		updated, cmd := m.Update(key('d'))
		if cmd != nil || updated.(Model).deleting != nil {
			t.Fatal("opened deletion with hidden confirmation")
		}
		m = beginDelete(t, deletionModel())
		updated, _ = m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = updated.(Model)
		updated, cmd = m.Update(key('y'))
		if cmd != nil || updated.(Model).busy {
			t.Fatal("confirmed deletion after its panel became hidden")
		}
	}
}
