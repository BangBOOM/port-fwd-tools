package tui

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/BangBOOM/port-fwd-tools/internal/daemon"
	"github.com/charmbracelet/x/ansi"
)

type tableColumns struct{ name, remote, ssh, traffic int }

func columnsFor(width int) tableColumns {
	// Reserve both port numbers and the complete status before allocating
	// space to names and hosts. Even at 54 columns, neither port is clipped.
	traffic := 0
	if width >= 66 {
		traffic = 15
		width -= traffic + 1
	}
	extra := max(0, width-50)
	name, ssh := 5+extra/3, 3+extra/5
	return tableColumns{name: name, remote: width - 28 - name - ssh, ssh: ssh, traffic: traffic}
}

func endpoint(host string, port int) string {
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
}

func endpointCell(host string, port, width int) string {
	full := endpoint(host, port)
	suffix := ":" + strconv.Itoa(port)
	address := strings.TrimSuffix(full, suffix)
	return fit(ansi.Truncate(address, max(1, width-len(suffix)), "…")+suffix, width)
}

func (m Model) rowStatus(t daemon.Tunnel) string {
	switch t.Status {
	case "connected":
		return "Connected"
	case "retrying":
		return m.styles.warn.Render("Retrying")
	case "connecting":
		return m.styles.warn.Render("Connecting")
	case "needs_attention":
		return m.styles.bad.Render("Needs action")
	default:
		return m.styles.dim.Render("Stopped")
	}
}

func (m Model) listHeader(width int) string {
	counts := map[string]int{}
	for _, t := range m.state.Tunnels {
		counts[t.Status]++
	}
	var parts []string
	for _, item := range []struct{ status, label string }{{"connected", "connected"}, {"connecting", "connecting"}, {"retrying", "retrying"}, {"needs_attention", "need action"}, {"stopped", "stopped"}} {
		if n := counts[item.status]; n > 0 {
			if item.status == "needs_attention" && n == 1 {
				item.label = "needs action"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, item.label))
		}
	}
	summary := strings.Join(parts, " · ")
	if len(parts) == 0 {
		summary = "No tunnels"
	}
	if ansi.StringWidth(summary) > width-5 {
		summary = fmt.Sprintf("%d tunnels", len(m.state.Tunnels))
		if counts["needs_attention"] > 0 {
			label := "need action"
			if counts["needs_attention"] == 1 {
				label = "needs action"
			}
			summary += fmt.Sprintf(" · %d %s", counts["needs_attention"], label)
		} else if counts["retrying"] > 0 {
			summary += fmt.Sprintf(" · %d retrying", counts["retrying"])
		}
	}
	color := m.styles.dim
	if counts["retrying"] > 0 {
		color = m.styles.warn
	}
	if counts["needs_attention"] > 0 {
		color = m.styles.bad
	}
	if !m.connected {
		summary = "Daemon offline · Last known state"
		color = m.styles.bad
	}
	return m.styles.accent.Render("pf") + strings.Repeat(" ", max(1, width-2-ansi.StringWidth(summary))) + color.Render(summary)
}

func briefError(t daemon.Tunnel) string {
	s := strings.ToLower(t.LastError)
	switch {
	case strings.Contains(s, "address already in use"):
		return fmt.Sprintf("Local port %d is already in use.", t.Rule.LocalPort)
	case strings.Contains(s, "bind") && strings.Contains(s, "permission denied"):
		return fmt.Sprintf("Permission denied binding local port %d.", t.Rule.LocalPort)
	case strings.Contains(s, "permission denied"):
		return "SSH authentication failed. Check your key / agent."
	case strings.Contains(s, "host key verification failed"), strings.Contains(s, "remote host identification has changed"):
		return "SSH host key verification failed. Check known_hosts."
	case strings.Contains(s, "network is unreachable"), strings.Contains(s, "network unreachable"):
		return "Network unreachable."
	case strings.Contains(s, "connection refused"):
		return "SSH connection refused."
	case strings.Contains(s, "could not resolve hostname"):
		return "Could not resolve the SSH hostname."
	case strings.Contains(s, "timed out"):
		return "SSH connection timed out."
	case strings.Contains(s, "connection reset"), strings.Contains(s, "broken pipe"):
		return "SSH connection lost."
	case t.LastError != "":
		return strings.TrimSpace(t.LastError)
	default:
		return "SSH connection interrupted."
	}
}

func connectedAge(since time.Time) string {
	if since.IsZero() {
		return "Connected"
	}
	minutes := max(0, int(time.Since(since).Minutes()))
	if minutes == 0 {
		return "Connected for <1m"
	}
	if minutes < 60 {
		return fmt.Sprintf("Connected for %dm", minutes)
	}
	return fmt.Sprintf("Connected for %dh %dm", minutes/60, minutes%60)
}

// All selected states reserve three lines so reconnect updates do not move the
// list or footer. Detailed SSH output is available in the log view.
func (m Model) selectedPanel(t daemon.Tunnel, width int, c tableColumns) []string {
	r := t.Rule
	title := m.styles.accent.Render(r.Name) + m.styles.dim.Render(" · "+r.SSHHost)
	auto := "off"
	if r.AutoReconnect {
		auto = "on"
	}
	switch t.Status {
	case "retrying":
		retry := "Retry scheduled"
		if !t.NextRetry.IsZero() {
			seconds := max(0, int(time.Until(t.NextRetry).Seconds())+1)
			retry = fmt.Sprintf("Retry #%d in %ds", t.Retries, seconds)
		}
		return []string{title + m.styles.warn.Render(" · Connection issue"), m.styles.warn.Render(briefError(t)), retry + " · [r] Reconnect now"}
	case "needs_attention":
		return []string{title + m.styles.bad.Render(" · Needs action"), m.styles.bad.Render(briefError(t)), "Fix the issue, then [r] reconnect · [l] Full log"}
	case "connecting":
		return []string{title, "Opening SSH connection…", ""}
	case "connected":
		extra := ""
		if c.traffic == 0 {
			extra = m.styles.dim.Render(m.trafficDetail(t))
		} else if ansi.StringWidth(endpoint(r.RemoteHost, r.RemotePort)) > c.remote || ansi.StringWidth(r.SSHHost) > c.ssh {
			extra = m.styles.dim.Render("Remote: " + endpointCell(r.RemoteHost, r.RemotePort, max(1, width-8)))
		}
		return []string{title, connectedAge(t.Since) + m.styles.dim.Render(" · Auto reconnect "+auto), extra}
	default:
		return []string{title, "Stopped manually. Auto reconnect is paused.", "[Space] Start this tunnel"}
	}
}

func (m Model) listFooter(width int, selected *daemon.Tunnel) string {
	if selected == nil {
		return "a Add · ? Help · q Detach"
	}
	action := "Stop"
	if !selected.Rule.Enabled {
		action = "Start"
	}
	if width < 65 {
		return "a Add · Space " + action + " · l Logs · ? Help · q Detach"
	}
	return "a Add · e Edit · Space " + action + " · l Logs · ? Help · q Detach"
}

// Shared by rendering and sampling so offscreen rows incur no statistics work.
func (m Model) listRange(h int) (int, int) {
	bottom := 2 // blank line and footer
	count := len(m.state.Tunnels)
	if count > 0 || !m.connected {
		bottom += 4 // separator and three-line detail panel
	}
	if m.message != "" && (m.connected || !strings.HasPrefix(m.message, "cannot reach daemon")) {
		bottom++
	}
	capacity := max(1, h-3-bottom)
	selected := max(0, min(m.selected, count-1))
	start := min(max(0, selected-capacity+1), max(0, count-capacity))
	return start, min(count, start+capacity)
}

func (m Model) listView(w, h int) string {
	c := columnsFor(w)
	selected := -1
	if len(m.state.Tunnels) > 0 {
		selected = max(0, min(m.selected, len(m.state.Tunnels)-1))
	}
	var tunnel *daemon.Tunnel
	if selected >= 0 {
		tunnel = &m.state.Tunnels[selected]
	}
	// Compute layout from the actual footer and panel, leaving the rest to rows.
	var bottom []string
	if tunnel != nil {
		bottom = append(bottom, m.styles.dim.Render(strings.Repeat("─", w)))
		bottom = append(bottom, m.selectedPanel(*tunnel, w, c)...)
	}
	if !m.connected {
		bottom = []string{m.styles.bad.Render(strings.Repeat("─", w)), m.styles.bad.Render("Background service unavailable"), "Waiting for daemon. Reopen pf to restart it.", "Shown tunnel states may be out of date."}
	}
	bottom = append(bottom, "", m.styles.dim.Render(m.listFooter(w, tunnel)))
	if m.message != "" && (m.connected || !strings.HasPrefix(m.message, "cannot reach daemon")) {
		bottom = append(bottom, m.styles.warn.Render(m.message))
	}
	start, end := m.listRange(h)
	row := func(prefix, name, local, remote, ssh, state, traffic string) string {
		line := prefix + fit(name, c.name) + " " + fit(local, 10) + " " + fit(remote, c.remote) + " " + fit(ssh, c.ssh) + " " + fit(state, 12)
		if c.traffic > 0 {
			line += " " + fit(traffic, c.traffic)
		}
		return line
	}
	localHeader, remoteHeader := "Local port", "Remote destination"
	if c.remote < ansi.StringWidth(remoteHeader) {
		remoteHeader = "Remote target"
	}
	lines := []string{m.listHeader(w), "", m.styles.dim.Render(row("  ", "Name", localHeader, remoteHeader, "SSH", "Status", "Total ↓ / ↑"))}
	if selected < 0 {
		lines = append(lines, "No tunnels yet. Press a to add a local port forward.")
	} else {
		for i := start; i < end; i++ {
			t := m.state.Tunnels[i]
			r := t.Rule
			prefix := "  "
			if i == selected {
				prefix = "› "
			}
			line := row(prefix, r.Name, strconv.Itoa(r.LocalPort), endpointCell(r.RemoteHost, r.RemotePort, c.remote), r.SSHHost, m.rowStatus(t), m.trafficCell(t))
			if i == selected {
				line = m.styles.selected.Render(line)
			}
			lines = append(lines, line)
		}
	}
	// Keep detail and actions in predictable positions, with breathing room
	// rather than a permanent log block or repeated explanatory paragraphs.
	for len(lines)+len(bottom) < h {
		lines = append(lines, "")
	}
	if len(m.state.Tunnels) > end-start {
		page := m.styles.dim.Render(fmt.Sprintf("%d–%d of %d · ↑/↓", start+1, end, len(m.state.Tunnels)))
		// Paging replaces the otherwise blank line immediately above the table.
		lines[1] = page
	}
	return strings.Join(append(lines, bottom...), "\n")
}
