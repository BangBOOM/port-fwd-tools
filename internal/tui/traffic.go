package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/BangBOOM/port-fwd-tools/internal/daemon"
)

type trafficTarget struct {
	id    string
	since time.Time
}
type trafficSample struct {
	target                   trafficTarget
	counters                 daemon.Traffic
	unavailable, unsupported bool
}
type trafficResult []trafficSample

// Sampling is owned entirely by the TUI, not the daemon. With no open TUI there
// are no statistics timers, workers, socket queries, or counter storage.
func (m *Model) pollTraffic() tea.Cmd {
	if m.trafficBusy || !m.focused || !m.connected || m.client == nil || m.form != nil || m.deleting != nil || m.logs || m.help || m.busy || m.width < 54 || m.height < 20 {
		return nil
	}
	if time.Now().Before(m.nextTraffic) {
		return nil
	}
	m.nextTraffic = time.Now().Add(2 * time.Second)
	start, end := m.listRange(max(1, m.height-2))
	var targets []trafficTarget
	for _, t := range m.state.Tunnels[start:end] {
		if t.Status != "connected" {
			continue
		}
		cached, ok := m.traffic[t.Rule.ID]
		if ok && cached.target.since.Equal(t.Since) && cached.unsupported {
			continue
		}
		targets = append(targets, trafficTarget{id: t.Rule.ID, since: t.Since})
	}
	if len(targets) == 0 {
		return nil
	}
	m.trafficBusy = true
	client := m.client
	return func() tea.Msg {
		// One serial batch with a fixed time budget, not one goroutine or process
		// per tunnel. A stuck master cannot stall rendering or SSH supervision.
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		samples := make(trafficResult, 0, len(targets))
		deadline, _ := ctx.Deadline()
		for i, target := range targets {
			if ctx.Err() != nil {
				break
			}
			// Share the remaining budget so an unresponsive master does not
			// repeatedly starve rows later in the visible list.
			query, done := context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(targets)-i))
			counters, err := client.Traffic(query, target.id)
			done()
			samples = append(samples, trafficSample{target: target, counters: counters, unavailable: err != nil, unsupported: errors.Is(err, daemon.ErrTrafficUnsupported)})
		}
		return samples
	}
}

func (m *Model) receiveTraffic(samples trafficResult) {
	m.trafficBusy = false
	if m.traffic == nil {
		m.traffic = make(map[string]trafficSample)
	}
	current := m.connectedGenerations()
	for _, sample := range samples {
		if since, ok := current[sample.target.id]; ok && since.Equal(sample.target.since) {
			m.traffic[sample.target.id] = sample
		}
	}
}

func (m *Model) pruneTraffic() {
	if len(m.traffic) == 0 {
		return
	}
	current := m.connectedGenerations()
	for id, sample := range m.traffic {
		if since, ok := current[id]; !ok || !since.Equal(sample.target.since) {
			delete(m.traffic, id)
		}
	}
}

func (m Model) connectedGenerations() map[string]time.Time {
	current := make(map[string]time.Time, len(m.state.Tunnels))
	for _, t := range m.state.Tunnels {
		if t.Status == "connected" {
			current[t.Rule.ID] = t.Since
		}
	}
	return current
}

func (m Model) trafficCell(t daemon.Tunnel) string {
	if !m.connected || t.Status != "connected" {
		return "—"
	}
	sample, ok := m.traffic[t.Rule.ID]
	if !ok || !sample.target.since.Equal(t.Since) {
		return "…"
	}
	if sample.unavailable {
		return "n/a"
	}
	return sample.counters.Received + " / " + sample.counters.Sent
}

func (m Model) trafficDetail(t daemon.Tunnel) string {
	sample, ok := m.traffic[t.Rule.ID]
	if !ok || !sample.target.since.Equal(t.Since) {
		return "SSH totals: sampling…"
	}
	if sample.unsupported {
		return "SSH totals: unavailable on this OpenSSH version"
	}
	if sample.unavailable {
		return "SSH totals: temporarily unavailable"
	}
	return "SSH totals ↓" + sample.counters.Received + " ↑" + sample.counters.Sent
}
