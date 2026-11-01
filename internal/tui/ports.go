package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/BangBOOM/port-fwd-tools/internal/core"
	"github.com/charmbracelet/x/ansi"
)

type portListener struct {
	port    int
	address string // As reported by lsof: "*", "127.0.0.1", "[::]", "::1"…
	pid     int
	process string // Short name from lsof; "—" when the name is unavailable
	command string // Full command line from ps; empty when unavailable
}

type portsResult struct {
	listeners []portListener
	err       error
}

type killResult struct {
	name  string
	pid   int
	err   error
	stale bool // The process was already gone when we tried to signal it.
}

// Like traffic sampling, port scanning is owned entirely by the TUI: listening
// sockets exist on this machine, where the TUI runs, so the daemon is not
// involved. The scan runs lsof plus one batched ps call behind a fixed budget.
func scanPorts() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := runLsof(ctx)
		if err != nil {
			return portsResult{err: err}
		}
		listeners := parseLsof(out)
		if commands := psCommands(ctx, pidsOf(listeners)); commands != nil {
			for i := range listeners {
				listeners[i].command = commands[listeners[i].pid]
			}
		}
		return portsResult{listeners: listeners}
	}
}

func runLsof(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-FpcPn")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errors.New("lsof is not installed or not in PATH")
	}
	if ctx.Err() != nil {
		return nil, errors.New("port scan timed out")
	}
	var exitErr *exec.ExitError
	// lsof exits 1 both when it lists nothing and on fatal errors; without
	// diagnostics on stderr an empty result is the reading.
	if errors.As(err, &exitErr) && stderr.Len() == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("lsof: %w", err)
}

// parseLsof reads lsof -F field output. Unrequested field letters (f, P, …)
// appear in practice and are ignored; malformed lines are skipped. The name
// map is filled first so a c line is honored even if it follows its n lines.
func parseLsof(out []byte) []portListener {
	lines := strings.Split(string(out), "\n")
	names := make(map[int]string)
	pid := -1
	for _, line := range lines {
		if len(line) < 2 {
			continue
		}
		if line[0] == 'p' {
			if v, err := strconv.Atoi(line[1:]); err == nil {
				pid = v
			} else {
				pid = -1
			}
		} else if line[0] == 'c' && pid >= 0 {
			names[pid] = line[1:]
		}
	}
	type key struct {
		pid     int
		address string
		port    int
	}
	seen := make(map[key]bool)
	listeners := []portListener{}
	pid = -1
	for _, line := range lines {
		if len(line) < 2 || line[0] != 'n' {
			if len(line) >= 2 && line[0] == 'p' {
				if v, err := strconv.Atoi(line[1:]); err == nil {
					pid = v
				} else {
					pid = -1
				}
			}
			continue
		}
		address, port, ok := splitListenAddress(line[1:])
		if !ok || pid < 0 {
			continue
		}
		k := key{pid: pid, address: address, port: port}
		if seen[k] {
			continue // Dual-stack sockets repeat the same wildcard listener.
		}
		seen[k] = true
		process := names[pid]
		if process == "" {
			process = "—"
		}
		listeners = append(listeners, portListener{port: port, address: address, pid: pid, process: process})
	}
	sort.Slice(listeners, func(i, j int) bool {
		if listeners[i].port != listeners[j].port {
			return listeners[i].port < listeners[j].port
		}
		if listeners[i].address != listeners[j].address {
			return listeners[i].address < listeners[j].address
		}
		return listeners[i].process < listeners[j].process
	})
	return listeners
}

// splitListenAddress splits "127.0.0.1:5432", "*:22", or "[::]:22" on the last
// colon, which keeps bracketed IPv6 addresses intact.
func splitListenAddress(s string) (string, int, bool) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, false
	}
	port, err := strconv.Atoi(s[i+1:])
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, false
	}
	return s[:i], port, true
}

func pidsOf(listeners []portListener) []string {
	seen := make(map[int]bool)
	pids := []string{}
	for _, l := range listeners {
		if l.pid >= 0 && !seen[l.pid] {
			seen[l.pid] = true
			pids = append(pids, strconv.Itoa(l.pid))
		}
	}
	return pids
}

func psCommands(ctx context.Context, pids []string) map[int]string {
	if len(pids) == 0 {
		return nil
	}
	args := append([]string{"-o", "pid=,args=", "-p"}, pids...)
	out, err := exec.CommandContext(ctx, "ps", args...).Output()
	if err != nil {
		return nil // The short process name is still useful without ps.
	}
	return parsePs(out)
}

// parsePs reads `ps -o pid=,args=` output into pid → full command line.
func parsePs(out []byte) map[int]string {
	commands := make(map[int]string)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimLeft(line, " \t")
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		pid, err := strconv.Atoi(parts[0])
		if err != nil || len(parts) < 2 {
			continue
		}
		commands[pid] = parts[1]
	}
	return commands
}

// killListener terminates the process behind one listening row: SIGTERM
// first, escalating to SIGKILL after a two-second grace period. Like the
// scan, this runs entirely in the TUI process; the daemon is not involved.
func killListener(l portListener) tea.Cmd {
	return func() tea.Msg {
		name := l.process
		if name == "" {
			name = "process"
		}
		if l.pid <= 0 {
			return killResult{name: name, err: errors.New("unknown process id")}
		}
		if l.pid == os.Getpid() {
			return killResult{name: name, err: errors.New("refusing to kill pf itself")}
		}
		if err := syscall.Kill(l.pid, 0); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return killResult{name: name, pid: l.pid, stale: true}
			}
			return killResult{name: name, err: fmt.Errorf("cannot signal pid %d: %w", l.pid, err)}
		}
		if err := syscall.Kill(l.pid, syscall.SIGTERM); err != nil {
			return killResult{name: name, err: err}
		}
		if waitDead(l.pid, 2*time.Second, 200*time.Millisecond) {
			return killResult{name: name, pid: l.pid}
		}
		// The process is ignoring or delaying SIGTERM; force it.
		if err := syscall.Kill(l.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return killResult{name: name, err: err}
		}
		waitDead(l.pid, time.Second, 100*time.Millisecond)
		return killResult{name: name, pid: l.pid}
	}
}

// waitDead polls with signal 0 until the pid disappears or the budget is spent.
func waitDead(pid int, timeout, step time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(step)
	}
}

// pfClaims maps local ports claimed by saved rules, regardless of whether the
// listener is pf's own ssh: the daemon snapshot does not carry child PIDs.
func (m Model) pfClaims() map[int][]core.Rule {
	claims := make(map[int][]core.Rule)
	for _, t := range m.state.Tunnels {
		claims[t.Rule.LocalPort] = append(claims[t.Rule.LocalPort], t.Rule)
	}
	return claims
}

// Fixed chrome: header, status, column header, separator, three detail lines,
// blank line, footer. Everything between the column header and the separator
// is row space.
func (m Model) portsView(w, h int) string {
	const portW, addrW, badgeW = 5, 10, 2
	procW := max(1, w-2-portW-1-addrW-1-1-badgeW)
	row := func(prefix, port, address, process, badge string) string {
		return prefix + fit(port, portW) + " " + fit(address, addrW) + " " + fit(process, procW) + " " + badge
	}
	count := len(m.portsData)
	// The count lives in the header; this line carries transient feedback.
	status := ""
	switch {
	case strings.HasPrefix(m.message, "Kill"):
		status = m.styles.warn.Render(m.message)
	case m.portsErr != "":
		status = m.styles.bad.Render("Port scan failed: " + m.portsErr)
	}
	lines := []string{m.listHeader(w), status,
		m.styles.dim.Render(row("  ", "Port", "Address", "Process", "pf"))}
	cursor := max(0, min(m.portsCursor, count-1))
	if count == 0 && m.portsErr == "" {
		lines = append(lines, "No TCP listeners found.")
	}
	claims := m.pfClaims()
	detail := []string{""}
	if m.portsKilling != nil {
		detail = m.portsConfirmPanel(*m.portsKilling, claims, w)
	} else if count > 0 {
		detail = m.portsDetail(m.portsData[cursor], claims, w)
	}
	bottom := len(detail) + 2 // detail, blank line, footer
	capacity := max(1, h-3-bottom)
	start := min(max(0, cursor-capacity+1), max(0, count-capacity))
	end := min(count, start+capacity)
	for i := start; i < end; i++ {
		l := m.portsData[i]
		prefix := "  "
		if i == cursor {
			prefix = "› "
		}
		badge := "  "
		if len(claims[l.port]) > 0 {
			badge = m.styles.good.Render("pf")
		}
		line := row(prefix, strconv.Itoa(l.port), l.address, fmt.Sprintf("%s (%d)", l.process, l.pid), badge)
		if i == cursor {
			line = m.styles.selected.Render(line)
		}
		lines = append(lines, line)
	}
	if count > end-start {
		lines[1] = m.styles.dim.Render(fmt.Sprintf("%d–%d of %d · ↑/↓", start+1, end, count))
	}
	// Keep detail and footer bottom-anchored whatever the row count is; the
	// emitted block must match the budget above exactly (detail, blank, footer).
	for len(lines)+bottom < h {
		lines = append(lines, "")
	}
	lines = append(lines, detail...)
	lines = append(lines, "", m.styles.dim.Render(portsFooter(m)))
	return strings.Join(lines, "\n")
}

func (m Model) portsDetail(l portListener, claims map[int][]core.Rule, w int) []string {
	command := l.command
	if command == "" {
		command = l.process
	}
	wrapped := strings.Split(ansi.Wrap(command, max(1, w-2), ""), "\n")
	if len(wrapped) > 2 {
		wrapped = wrapped[:2]
		wrapped[1] = ansi.Truncate(wrapped[1]+" …", max(1, w-2), "…")
	}
	var link string
	switch rules := claims[l.port]; len(rules) {
	case 0:
		link = m.styles.dim.Render("Not used by any pf tunnel")
	case 1:
		r := rules[0]
		link = m.styles.good.Render(fmt.Sprintf("pf tunnel %q → %s via %s", r.Name, endpoint(r.RemoteHost, r.RemotePort), r.SSHHost))
	default:
		quoted := make([]string, len(rules))
		for i, r := range rules {
			quoted[i] = fmt.Sprintf("%q", r.Name)
		}
		link = m.styles.good.Render("pf tunnels " + strings.Join(quoted, ", "))
	}
	// One entry per rendered line: the height budget counts len(detail), so an
	// embedded newline here would push the footer off screen.
	detail := append([]string{m.styles.dim.Render(strings.Repeat("─", w))}, wrapped...)
	return append(detail, link)
}

// The confirmation reuses the detail panel's three lines, like tunnel
// deletion, so the row list never moves.
func (m Model) portsConfirmPanel(l portListener, claims map[int][]core.Rule, w int) []string {
	command := l.command
	if command == "" {
		command = l.process
	}
	note := m.styles.dim.Render(fmt.Sprintf("Frees port %d. [y] Kill · [n/Esc] Cancel", l.port))
	if len(claims[l.port]) > 0 {
		note = m.styles.warn.Render("Claimed by pf: pf may restart this process. Stop the tunnel (Space) instead.")
	}
	return []string{
		m.styles.warn.Render(fmt.Sprintf("Kill %s (pid %d)?", l.process, l.pid)),
		ansi.Truncate(command, max(1, w-2), "…"),
		note,
	}
}

func portsFooter(m Model) string {
	switch {
	case m.portsKilling != nil:
		return "[y] Kill · [n/Esc] Cancel"
	case m.portsBusy:
		return "Scanning… · [p/←→] Tabs · [q] Detach"
	case m.width >= 66:
		return "[r] Refresh · [d] Kill · [↑/↓] Scroll · [p/←→] Tabs · [q] Detach"
	default:
		return "[r] Refresh · [d] Kill · [p/←→] Tabs · [q] Detach"
	}
}
