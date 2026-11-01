package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/BangBOOM/port-fwd-tools/internal/core"
	"github.com/BangBOOM/port-fwd-tools/internal/daemon"
	"github.com/charmbracelet/x/ansi"
)

func TestParseLsofDedupesAndSorts(t *testing.T) {
	// Shape observed on macOS: unrequested f/P fields, and dual-stack sockets
	// repeating the same wildcard listener on two file descriptors.
	out := []byte("p648\ncControlCenter\nf11\nPTCP\nn*:7000\nf12\nPTCP\nn*:7000\n" +
		"f13\nPTCP\nn*:5000\np3546\nccpython3.13\nf3\nPTCP\nn127.0.0.1:5195\n")
	got := parseLsof(out)
	want := []portListener{
		{port: 5000, address: "*", pid: 648, process: "ControlCenter"},
		{port: 5195, address: "127.0.0.1", pid: 3546, process: "cpython3.13"},
		{port: 7000, address: "*", pid: 648, process: "ControlCenter"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d listeners, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("listener %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseLsofToleratesMalformedInput(t *testing.T) {
	// c after its n lines still names the socket; bad p lines reset the
	// current pid so later sockets are not attributed to the wrong process.
	out := []byte("p1\nn[::]:22\nchttpd\nnabc\nn*:99999\nn*:ssh\npX\nn*:9999\np2\nn*:80\n\n")
	got := parseLsof(out)
	want := []portListener{
		{port: 22, address: "[::]", pid: 1, process: "httpd"},
		{port: 80, address: "*", pid: 2, process: "—"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("listener %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(parseLsof(nil)) != 0 {
		t.Fatal("empty input must yield no listeners")
	}
}

func TestParsePs(t *testing.T) {
	out := []byte(" 3546 /Users/x/.venv/bin/python -m serve --port 5195\n36452 node ./vite --host 127.0.0.1\r\n\njunk line\n")
	got := parsePs(out)
	if len(got) != 2 {
		t.Fatalf("got %d commands, want 2: %v", len(got), got)
	}
	if got[3546] != "/Users/x/.venv/bin/python -m serve --port 5195" {
		t.Fatalf("command misparsed: %q", got[3546])
	}
	if got[36452] != "node ./vite --host 127.0.0.1" {
		t.Fatalf("carriage return not stripped: %q", got[36452])
	}
	if len(parsePs(nil)) != 0 {
		t.Fatal("empty input must yield no commands")
	}
}

func TestPortsOpenScanRefreshClose(t *testing.T) {
	data := []portListener{
		{port: 5173, address: "127.0.0.1", pid: 36452, process: "node"},
		{port: 15432, address: "127.0.0.1", pid: 39940, process: "ssh"},
	}
	m, cmd := New(nil).Update(key('p'))
	model := m.(Model)
	if model.tab != tabPorts || cmd == nil || !model.portsBusy {
		t.Fatal("p must switch to the Ports tab and issue one scan marked busy")
	}
	if again, cmd := model.Update(key('r')); cmd != nil || !again.(Model).portsBusy {
		t.Fatal("r while busy must not start a second scan")
	}
	m, _ = model.Update(portsResult{listeners: data})
	model = m.(Model)
	if model.portsBusy || len(model.portsData) != 2 {
		t.Fatal("result must store listeners and clear busy")
	}
	got, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if got.(Model).portsCursor != 1 {
		t.Fatalf("End must move the cursor to the last row, got %d", got.(Model).portsCursor)
	}
	got, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got.(Model).portsCursor != 0 {
		t.Fatal("Up must move the cursor up")
	}
	// Tab keys replace the old Esc-to-close: p and ← both return to Tunnels.
	got, cmd = model.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if got.(Model).tab != tabTunnels || cmd != nil {
		t.Fatal("Left must switch back to Tunnels without extra work")
	}
	// Results that land on the other tab are discarded, busy still clears.
	tunnels := got.(Model)
	tunnels.portsBusy = true
	got, _ = tunnels.Update(portsResult{listeners: data})
	if len(got.(Model).portsData) != 0 || got.(Model).portsBusy {
		t.Fatal("off-tab results must be discarded with busy cleared")
	}
}

func TestPortsScanErrorKeepsViewUsable(t *testing.T) {
	m, _ := New(nil).Update(key('p'))
	model := m.(Model)
	got, _ := model.Update(portsResult{err: errors.New("lsof is not installed or not in PATH")})
	model = got.(Model)
	if model.portsBusy || model.portsErr == "" {
		t.Fatal("scan errors must surface while clearing busy")
	}
	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "Port scan failed: lsof is not installed") {
		t.Fatalf("error missing from view:\n%s", view)
	}
}

func TestPortsViewRendering(t *testing.T) {
	for _, size := range [][2]int{{54, 20}, {80, 24}} {
		m := New(nil)
		m.width, m.height = size[0], size[1]
		m.state.Tunnels = []daemon.Tunnel{{
			Rule:   core.Rule{ID: "0123456789abcdef", Name: "database", SSHHost: "my-server", LocalPort: 15432, RemoteHost: "localhost", RemotePort: 5432, Enabled: true},
			Status: "connected",
		}}
		m.tab = tabPorts
		m.portsData = []portListener{
			{port: 5173, address: "127.0.0.1", pid: 36452, process: "node", command: "node /long/path/node_modules/.bin/vite --host 127.0.0.1 --strictPort"},
			{port: 15432, address: "127.0.0.1", pid: 39940, process: "ssh", command: "ssh -N -L 15432:localhost:5432 my-server"},
		}
		m.portsCursor = 1
		view := ansi.Strip(m.View().Content)
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("line overflows %v: %q", size, line)
			}
		}
		wants := []string{"TUNNELS", "PORTS", "2 listening ports", "Port", "Address", "Process", "5173", "15432", "node (36452)", "ssh (39940)", "pf", "pf tunnel \"database\"", "[r] Refresh"}
		if size[0] >= 66 { // The full route only fits on wider terminals.
			wants = append(wants, "→ localhost:5432 via my-server")
		}
		for _, want := range wants {
			if !strings.Contains(view, want) {
				t.Fatalf("%v missing %q:\n%s", size, want, view)
			}
		}
		if strings.ContainsFunc(view, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
			t.Fatal("UI contains Chinese text")
		}
		// The unselected row must explain it is not a pf port.
		m.portsCursor = 0
		if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Not used by any pf tunnel") {
			t.Fatalf("non-pf detail missing:\n%s", view)
		}
	}
}

func TestPortsViewEmptyStates(t *testing.T) {
	m := New(nil)
	m.width, m.height = 80, 24
	m.tab = tabPorts
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "No TCP listeners found.") {
		t.Fatalf("empty list missing placeholder:\n%s", view)
	}
	m.portsBusy = true
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Scanning…") {
		t.Fatalf("busy state missing:\n%s", view)
	}
	m.portsBusy = false
	m.portsData = []portListener{{port: 22, address: "*", pid: 1, process: "sshd"}}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "1 listening port") {
		t.Fatalf("singular count missing:\n%s", view)
	}
}

func TestTabSwitching(t *testing.T) {
	m := New(nil)
	m.width, m.height = 80, 24
	m.state.Tunnels = []daemon.Tunnel{{
		Rule:   core.Rule{ID: "0123456789abcdef", Name: "database", SSHHost: "my-server", LocalPort: 15432, RemoteHost: "localhost", RemotePort: 5432, Enabled: true},
		Status: "connected",
	}}
	// Both directions cycle between the two tabs; entering Ports starts a scan.
	got, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got.(Model).tab != tabPorts || cmd == nil || !got.(Model).portsBusy {
		t.Fatal("Right must enter the Ports tab with a scan")
	}
	got, cmd = got.(Model).Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if got.(Model).tab != tabTunnels || cmd != nil {
		t.Fatal("Left must return to Tunnels without extra work")
	}
	got, _ = got.(Model).Update(portsResult{}) // Resolve the in-flight scan.
	got, cmd = got.(Model).Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if got.(Model).tab != tabPorts || cmd == nil {
		t.Fatal("Left must wrap around to Ports")
	}
	got, _ = got.(Model).Update(portsResult{}) // Clear busy with an empty scan.
	// The header renders both tabs and the active one is visible on both tabs.
	if view := ansi.Strip(got.(Model).View().Content); !strings.Contains(view, "TUNNELS") || !strings.Contains(view, "PORTS") || !strings.Contains(view, "0 listening ports") {
		t.Fatalf("ports tab header missing tabs or count:\n%s", view)
	}
	got, _ = got.(Model).Update(key('p'))
	if view := ansi.Strip(got.(Model).View().Content); !strings.Contains(view, "TUNNELS") || !strings.Contains(view, "PORTS") || !strings.Contains(view, "1 connected") {
		t.Fatalf("tunnels tab header missing tabs or summary:\n%s", view)
	}
	// q detaches from the Ports tab like everywhere else.
	got, cmd = got.(Model).Update(key('p'))
	got, cmd = got.(Model).Update(key('q'))
	if cmd == nil {
		t.Fatal("q on the Ports tab must detach")
	}
	// Modal overlays block switching.
	m2 := New(nil)
	m2.width, m2.height = 80, 24
	opened, _ := m2.Update(key('a'))
	got, _ = opened.(Model).Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got.(Model).tab != tabTunnels {
		t.Fatal("Right must not switch tabs while the form is open")
	}
	m3 := New(nil)
	m3.width, m3.height = 80, 24
	m3.state.Tunnels = m2.state.Tunnels
	m3.portsData = []portListener{{port: 22, address: "*", pid: 1, process: "sshd"}}
	m3.tab = tabPorts
	m3.portsKilling = &m3.portsData[0]
	got, _ = m3.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if got.(Model).tab != tabPorts || got.(Model).portsKilling == nil {
		t.Fatal("Left must not switch tabs during a kill confirmation")
	}
}

func startChild(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", args, err)
	}
	go cmd.Wait() // Reap so the dead child disappears instead of becoming a zombie.
	return cmd
}

func TestKillListenerLifecycle(t *testing.T) {
	t.Run("sigterm path", func(t *testing.T) {
		cmd := startChild(t, "sleep", "30")
		time.Sleep(150 * time.Millisecond)
		res := killListener(portListener{pid: cmd.Process.Pid, process: "sleep"})().(killResult)
		if res.err != nil || res.stale {
			t.Fatalf("sleep should die on SIGTERM: %+v", res)
		}
	})
	t.Run("escalates to sigkill", func(t *testing.T) {
		cmd := startChild(t, "bash", "-c", "trap '' TERM; sleep 30")
		time.Sleep(250 * time.Millisecond) // Let bash install the trap.
		start := time.Now()
		res := killListener(portListener{pid: cmd.Process.Pid, process: "bash"})().(killResult)
		if res.err != nil {
			t.Fatalf("trap-ignoring process should be SIGKILLed: %+v", res)
		}
		if elapsed := time.Since(start); elapsed < 2*time.Second {
			t.Fatalf("escalated after only %v; the grace period was skipped", elapsed)
		}
	})
	t.Run("already dead is stale success", func(t *testing.T) {
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		cmd.Wait()
		res := killListener(portListener{pid: cmd.Process.Pid, process: "sleep"})().(killResult)
		if res.err != nil || !res.stale {
			t.Fatalf("dead pid should be stale success: %+v", res)
		}
	})
	t.Run("refuses itself", func(t *testing.T) {
		res := killListener(portListener{pid: os.Getpid(), process: "pf"})().(killResult)
		if res.err == nil {
			t.Fatal("must refuse to kill pf itself")
		}
	})
}

func TestPortsKillConfirmationFlow(t *testing.T) {
	data := []portListener{{port: 5173, address: "127.0.0.1", pid: 111, process: "node", command: "node vite"}}
	m := New(nil)
	m.width, m.height = 80, 24
	m.tab = tabPorts
	m.portsData = data

	got, _ := m.Update(key('d'))
	model := got.(Model)
	if model.portsKilling == nil || model.portsKilling.pid != 111 {
		t.Fatal("d must freeze the selected row for confirmation")
	}
	view := ansi.Strip(model.View().Content)
	for _, want := range []string{"Kill node (pid 111)?", "node vite", "Frees port 5173", "[y] Kill · [n/Esc] Cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirm panel missing %q:\n%s", want, view)
		}
	}
	if got, _ := model.Update(key('d')); got.(Model).portsKilling.pid != 111 {
		t.Fatal("d during confirmation must not re-freeze")
	}
	got, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if got.(Model).portsKilling != nil {
		t.Fatal("Esc must cancel the confirmation")
	}

	got, _ = model.Update(key('d'))
	model = got.(Model)
	got, cmd := model.Update(key('y'))
	model = got.(Model)
	if model.portsKilling != nil || !model.portsBusy || cmd == nil || model.message != "Killing…" {
		t.Fatal("y must start the kill marked busy")
	}
	if got, _ := model.Update(key('d')); got.(Model).portsKilling != nil {
		t.Fatal("d must be blocked while the kill is in flight")
	}
	got, cmd = model.Update(killResult{name: "node", pid: 111})
	model = got.(Model)
	if model.message != "Killed node (111)." || !model.portsBusy || cmd == nil {
		t.Fatal("success must report and trigger a rescan")
	}
	got, _ = model.Update(portsResult{})
	model = got.(Model)
	if model.portsBusy {
		t.Fatal("busy must clear after the rescan")
	}

	// Failure reports the reason and does not rescan.
	model.portsData = data
	got, _ = model.Update(key('d'))
	model = got.(Model)
	got, _ = model.Update(key('y'))
	got, cmd = model.Update(killResult{name: "node", pid: 111, err: errors.New("operation not permitted")})
	model = got.(Model)
	if model.message != "Kill failed: operation not permitted" || model.portsBusy || cmd != nil {
		t.Fatal("failure must surface the error without a rescan")
	}
}

func TestPortsKillWarnsOnPfClaimedPort(t *testing.T) {
	m := New(nil)
	m.width, m.height = 80, 24
	m.tab = tabPorts
	m.state.Tunnels = []daemon.Tunnel{{
		Rule:   core.Rule{ID: "0123456789abcdef", Name: "database", SSHHost: "my-server", LocalPort: 5173, RemoteHost: "localhost", RemotePort: 5432},
		Status: "connected",
	}}
	m.portsData = []portListener{{port: 5173, address: "127.0.0.1", pid: 111, process: "ssh", command: "ssh -N -L 5173:localhost:5432"}}
	m.portsKilling = &m.portsData[0]
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Claimed by pf") {
		t.Fatalf("pf warning missing from confirm panel:\n%s", view)
	}
	if strings.Contains(view, "Frees port 5173") {
		t.Fatal("non-pf hint must be replaced by the pf warning")
	}
}

func TestHeaderSummaryFollowsTab(t *testing.T) {
	m := New(nil)
	m.width, m.height = 100, 30
	m.state.Tunnels = []daemon.Tunnel{{
		Rule:   core.Rule{ID: "0123456789abcdef", Name: "database", SSHHost: "my-server", LocalPort: 15432, RemoteHost: "localhost", RemotePort: 5432, Enabled: true},
		Status: "connected",
	}}
	m.portsData = []portListener{{port: 22, address: "*", pid: 1, process: "sshd"}}

	tunnels := ansi.Strip(m.View().Content)
	if !strings.Contains(tunnels, "1 connected") || strings.Contains(tunnels, "listening") {
		t.Fatalf("Tunnels header must show the tunnel summary only:\n%s", tunnels)
	}

	m.tab = tabPorts
	ports := ansi.Strip(m.View().Content)
	if !strings.Contains(ports, "1 listening port") || strings.Contains(ports, "connected") {
		t.Fatalf("Ports header must show the port summary only:\n%s", ports)
	}

	m.portsBusy = true
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Scanning…") {
		t.Fatal("busy header must say Scanning…")
	}
	m.portsBusy = false
	m.portsErr = "lsof is not installed"
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Scan failed") || !strings.Contains(view, "Port scan failed: lsof is not installed") {
		t.Fatalf("error state must show short header and full detail:\n%s", view)
	}
}

func TestFooterAnchoredAcrossTabs(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {54, 20}} {
		tunnels := New(nil)
		tunnels.width, tunnels.height = size[0], size[1]
		for i := 0; i < 15; i++ {
			tunnels.state.Tunnels = append(tunnels.state.Tunnels, daemon.Tunnel{Rule: core.Rule{ID: fmt.Sprintf("%016x", i), Name: fmt.Sprintf("db-%d", i), SSHHost: "my-server", LocalPort: 15432 + i, RemoteHost: "localhost", RemotePort: 5432, Enabled: true}, Status: "connected"})
		}
		ports := New(nil)
		ports.width, ports.height = size[0], size[1]
		ports.tab = tabPorts
		for i := 0; i < 25; i++ {
			ports.portsData = append(ports.portsData, portListener{port: 5000 + i, address: "127.0.0.1", pid: 100 + i, process: "svc", command: "svc --run"})
		}
		empty := New(nil)
		empty.width, empty.height = size[0], size[1]
		empty.tab = tabPorts

		footerRow := func(m Model) (int, int) {
			lines := strings.Split(m.View().Content, "\n")
			row := -1
			for i, line := range lines {
				if strings.Contains(line, "Detach") {
					row = i + 1
				}
			}
			return row, len(lines)
		}
		want, wantTotal := footerRow(tunnels)
		if want != wantTotal {
			t.Fatalf("%dx%d tunnels footer at row %d of %d; must be the last line", size[0], size[1], want, wantTotal)
		}
		// A wrapped command makes the detail panel four rendered lines; the
		// budget must count those lines or the footer is pushed off screen.
		wrapped := New(nil)
		wrapped.width, wrapped.height = size[0], size[1]
		wrapped.tab = tabPorts
		wrapped.portsData = ports.portsData
		wrapped.portsData[0].command = "node /very/long/path/that/wraps/across/the/detail/panel/when/rendered/at/narrow/widths --host 127.0.0.1 --strictPort"
		for name, m := range map[string]Model{"ports-many": ports, "ports-empty": empty, "ports-wrapped": wrapped} {
			got, total := footerRow(m)
			if got != want || total != wantTotal {
				t.Fatalf("%dx%d %s footer at row %d of %d; tunnels has %d of %d", size[0], size[1], name, got, total, want, wantTotal)
			}
		}
	}
}
