package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func (m Model) deleteTargetExists() bool {
	for _, t := range m.state.Tunnels {
		if t.Rule.ID == m.deleting.ID {
			return true
		}
	}
	return false
}

// Reuse the selected tunnel's three lines; confirmation never reduces list space.
func (m Model) deletePanel(width int) []string {
	r := m.deleting
	prefix := fmt.Sprintf("Local %d → ", r.LocalPort)
	available := width - ansi.StringWidth(prefix+" → ")
	ssh := ansi.Truncate(r.SSHHost, max(3, available/3), "…")
	remoteWidth := width - ansi.StringWidth(prefix+ssh+" → ")
	route := prefix + ssh + " → " + endpointCell(r.RemoteHost, r.RemotePort, remoteWidth)
	detail := m.styles.dim.Render("Stops forwarding and removes the saved rule.")
	switch {
	case m.busy:
		detail = m.styles.warn.Render("Deleting…")
	case !m.connected:
		detail = m.styles.bad.Render("Daemon offline. Cancel or wait for reconnect.")
	case m.message != "":
		detail = m.styles.bad.Render(m.message)
	}
	return []string{m.styles.warn.Render(fmt.Sprintf("Delete %q?", r.Name)), route, detail}
}

func (m Model) deleteFooter() string {
	switch {
	case m.busy:
		return "Deleting… · Ctrl+C Detach"
	case !m.connected:
		return "n / Esc Cancel · q Detach"
	case strings.HasPrefix(m.message, "Delete failed: "):
		return "y Retry delete · n / Esc Cancel"
	default:
		return "y Delete · n / Esc Cancel"
	}
}
