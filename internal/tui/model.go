package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/BangBOOM/port-fwd-tools/internal/core"
	"github.com/BangBOOM/port-fwd-tools/internal/daemon"
	"github.com/charmbracelet/x/ansi"
)

type result struct {
	state      daemon.Snapshot
	err        error
	mutation   bool
	action     string
	selectName string
}
type tick time.Time
type editor struct {
	fields   []textinput.Model
	focus    int
	auto     bool
	original core.Rule
}

type Model struct {
	client          *daemon.Client
	state           daemon.Snapshot
	selected        int
	width, height   int
	form            *editor
	message         string
	busy            bool
	connected       bool
	deleting        *core.Rule
	logs, help      bool
	logOffset       int
	theme           string
	preferencesPath string
	styles          styles
	noticeUntil     time.Time
	traffic         map[string]trafficSample
	trafficBusy     bool
	nextTraffic     time.Time
	focused         bool
}

func New(client *daemon.Client) Model {
	return NewWithPreferences(client, core.UIPreferences{Theme: core.ThemeTerminal}, "")
}

func NewWithPreferences(client *daemon.Client, prefs core.UIPreferences, path string) Model {
	theme, _ := core.NormalizeTheme(prefs.Theme)
	return Model{client: client, width: 80, height: 24, connected: true, focused: true, theme: theme, preferencesPath: path, styles: newStyles(theme)}
}
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.request(daemon.Request{Action: "state"}), nextTick())
}
func nextTick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tick(t) }) }
func (m Model) request(req daemon.Request) tea.Cmd {
	return func() tea.Msg {
		s, err := m.client.Do(req)
		return result{state: s, err: err, mutation: req.Action != "state", action: req.Action, selectName: req.Rule.Name}
	}
}
func (m *Model) chosen() *daemon.Tunnel {
	if len(m.state.Tunnels) == 0 {
		return nil
	}
	m.selected = max(0, min(m.selected, len(m.state.Tunnels)-1))
	return &m.state.Tunnels[m.selected]
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.FocusMsg:
		m.focused = true
	case tea.BlurMsg:
		m.focused = false
	case trafficResult:
		m.receiveTraffic(v)
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		if m.form != nil {
			for i := range m.form.fields {
				m.form.fields[i].SetWidth(max(8, m.width-24))
			}
		}
	case tick:
		if !m.noticeUntil.IsZero() && time.Now().After(m.noticeUntil) {
			if m.message == "Saved." || strings.HasPrefix(m.message, "Theme: ") || strings.HasPrefix(m.message, "Deleted ") || strings.HasPrefix(m.message, "Tunnel ") {
				m.message = ""
			}
			m.noticeUntil = time.Time{}
		}
		if m.busy {
			return m, nextTick()
		}
		traffic := m.pollTraffic()
		return m, tea.Batch(m.request(daemon.Request{Action: "state"}), traffic, nextTick())
	case result:
		if v.mutation {
			m.busy = false
		}
		if v.err != nil {
			m.message = v.err.Error()
			m.noticeUntil = time.Time{}
			if v.action == "delete" {
				m.message = "Delete failed: " + m.message
			}
			if !v.mutation {
				m.connected = false
			}
			return m, nil
		}
		id := ""
		if t := m.chosen(); t != nil {
			id = t.Rule.ID
		}
		m.state = v.state
		m.connected = true
		for i, t := range m.state.Tunnels {
			if t.Rule.ID == id || (v.mutation && v.selectName != "" && t.Rule.Name == v.selectName) {
				m.selected = i
			}
		}
		m.chosen()
		m.pruneTraffic()
		if v.mutation {
			m.form = nil
			m.message = "Saved."
			if v.action == "delete" && m.deleting != nil {
				m.message = fmt.Sprintf("Deleted %q.", m.deleting.Name)
			}
			m.noticeUntil = time.Now().Add(3 * time.Second)
			m.deleting = nil
		} else if strings.HasPrefix(m.message, "cannot reach daemon") {
			m.message = ""
		}
		if m.deleting != nil && !m.busy && !m.deleteTargetExists() {
			m.message = fmt.Sprintf("Tunnel %q no longer exists.", m.deleting.Name)
			m.noticeUntil = time.Now().Add(3 * time.Second)
			m.deleting = nil
		}
		traffic := m.pollTraffic()
		return m, traffic
	case tea.KeyPressMsg:
		key := v.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.busy {
			return m, nil
		}
		if m.help {
			m.help = false
			return m, nil
		}
		if m.form != nil {
			return m.updateForm(v)
		}
		if m.deleting != nil {
			switch key {
			case "y", "Y":
				if !m.connected || m.width < 54 || m.height < 20 {
					return m, nil
				}
				m.busy = true
				m.message = ""
				return m, m.request(daemon.Request{Action: "delete", ID: m.deleting.ID})
			case "n", "N", "esc", "enter":
				m.deleting = nil
				m.message = ""
			case "q":
				return m, tea.Quit
			}
			return m, nil
		}
		if m.logs {
			switch key {
			case "q", "esc", "l":
				m.logs = false
			case "up", "k":
				m.logOffset++
			case "down", "j":
				m.logOffset = max(0, m.logOffset-1)
			case "pgup":
				m.logOffset += max(1, m.height-8)
			case "pgdown":
				m.logOffset = max(0, m.logOffset-m.height+8)
			case "end":
				m.logOffset = 0
			}
			return m, nil
		}
		switch key {
		case "q":
			return m, tea.Quit
		case "?":
			m.help = true
		case "t":
			m.toggleTheme()
		case "up", "k":
			m.selected = max(0, m.selected-1)
		case "down", "j":
			m.selected = min(len(m.state.Tunnels)-1, m.selected+1)
		case "a":
			m.openForm(nil)
			return m, m.form.fields[0].Focus()
		case "e":
			if t := m.chosen(); t != nil {
				m.openForm(&t.Rule)
				return m, m.form.fields[0].Focus()
			}
		case "l", "enter":
			if m.chosen() != nil {
				m.logs = true
				m.logOffset = 0
			}
		case "d":
			if m.width < 54 || m.height < 20 {
				return m, nil
			}
			if t := m.chosen(); t != nil {
				rule := t.Rule // Freeze the identity and route while state refreshes.
				m.deleting = &rule
				m.message = ""
				m.noticeUntil = time.Time{}
			}
		case "space", " ", "r":
			if t := m.chosen(); t != nil {
				action := "restart"
				if key != "r" {
					action = "start"
					if t.Rule.Enabled {
						action = "stop"
					}
				}
				m.busy = true
				m.message = "Applying…"
				return m, m.request(daemon.Request{Action: action, ID: t.Rule.ID})
			}
		}
	}
	if m.form != nil && m.form.focus < 6 {
		var cmd tea.Cmd
		m.form.fields[m.form.focus], cmd = m.form.fields[m.form.focus].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) openForm(r *core.Rule) {
	f := &editor{auto: true, original: core.Rule{Enabled: true}}
	values := []string{"", "", "15432", "my-server", "localhost", "5432"}
	if r != nil {
		f.original = *r
		f.auto = r.AutoReconnect
		values = []string{"", r.Name, strconv.Itoa(r.LocalPort), r.SSHHost, r.RemoteHost, strconv.Itoa(r.RemotePort)}
	}
	for i, value := range values {
		input := textinput.New()
		input.SetStyles(m.styles.inputs)
		input.Prompt = ""
		input.CharLimit = 512
		input.SetWidth(max(8, m.width-24))
		input.SetValue(value)
		if i == 0 {
			input.Placeholder = "ssh -N -L 15432:localhost:5432 my-server"
		}
		f.fields = append(f.fields, input)
	}
	f.fields[0].Focus()
	m.form = f
	m.message = ""
}

func (f *editor) move(delta int) tea.Cmd {
	if f.focus < 6 {
		f.fields[f.focus].Blur()
	}
	f.focus = (f.focus + delta + 7) % 7
	if f.focus < 6 {
		return f.fields[f.focus].Focus()
	}
	return nil
}

func (m Model) updateForm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	f := m.form
	switch msg.String() {
	case "esc":
		m.form = nil
		m.message = ""
		return m, nil
	case "tab":
		return m, f.move(1)
	case "shift+tab":
		return m, f.move(-1)
	case "ctrl+s":
		r := f.original
		r.Name = strings.TrimSpace(f.fields[1].Value())
		r.LocalPort, _ = strconv.Atoi(f.fields[2].Value())
		r.SSHHost = strings.TrimSpace(f.fields[3].Value())
		r.RemoteHost = strings.TrimSpace(f.fields[4].Value())
		r.RemotePort, _ = strconv.Atoi(f.fields[5].Value())
		r.AutoReconnect = f.auto
		if r.Name == "" {
			r.Name = fmt.Sprintf("%s:%d", r.SSHHost, r.LocalPort)
		}
		if err := core.ValidateRule(r); err != nil {
			m.message = err.Error()
			return m, nil
		}
		action := "add"
		if r.ID != "" {
			action = "update"
		}
		m.busy = true
		m.message = "Saving…"
		return m, m.request(daemon.Request{Action: action, ID: r.ID, Rule: r})
	case "enter":
		if f.focus == 0 && strings.TrimSpace(f.fields[0].Value()) != "" {
			r, err := core.ParseCommand(f.fields[0].Value())
			if err != nil {
				m.message = err.Error()
				return m, nil
			}
			for i, value := range []string{r.Name, strconv.Itoa(r.LocalPort), r.SSHHost, r.RemoteHost, strconv.Itoa(r.RemotePort)} {
				f.fields[i+1].SetValue(value)
			}
			m.message = "Parsed. Review LOCAL and REMOTE before saving."
		}
		if f.focus == 6 {
			f.auto = !f.auto
			return m, nil
		}
		return m, f.move(1)
	case "space", " ":
		if f.focus == 6 {
			f.auto = !f.auto
			return m, nil
		}
	}
	if f.focus < 6 {
		var cmd tea.Cmd
		f.fields[f.focus], cmd = f.fields[f.focus].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) status(t daemon.Tunnel) string {
	switch t.Status {
	case "connected":
		return m.styles.good.Render("● Connected")
	case "connecting":
		return m.styles.warn.Render("◌ Connecting")
	case "retrying":
		return m.styles.warn.Render("↻ Retrying")
	case "needs_attention":
		return m.styles.bad.Render("! Needs action")
	default:
		return m.styles.dim.Render("○ Stopped")
	}
}
func fit(s string, w int) string {
	s = ansi.Truncate(s, max(0, w), "…")
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}

func (m Model) View() tea.View {
	w, h := max(1, m.width-4), max(1, m.height-2)
	var content string
	if m.width < 54 || m.height < 20 {
		content = "PORT FWD\nResize terminal to at least 54 columns × 20 rows.\n[q / Ctrl+C] Detach; tunnels keep running."
	} else if m.form != nil {
		content = m.formView(w, h)
	} else if m.help {
		content = m.helpView()
	} else if m.logs {
		content = m.logsView(w, h)
	} else {
		content = m.listView(w, h)
	}
	lines := strings.Split(content, "\n")
	for i := range lines {
		lines[i] = "  " + ansi.Truncate(lines[i], w, "…")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	canvas := "\n" + strings.Join(lines, "\n")
	if m.theme == core.ThemeDracula {
		// Paint only this application's cells. Do not change terminal-wide
		// default colors with OSC sequences; detaching restores the terminal.
		canvas = m.styles.base.Width(max(1, m.width)).Height(max(1, m.height)).Render(canvas)
	}
	v := tea.NewView(canvas)
	v.AltScreen = true
	v.ReportFocus = true
	v.WindowTitle = "pf — Port forwarding"
	return v
}

func (m Model) formView(w, h int) string {
	f := m.form
	input := func(label string, i int) string {
		p := "  "
		if f.focus == i {
			p = m.styles.accent.Render("› ")
		}
		return p + fit(label, 18) + f.fields[i].View()
	}
	lines := []string{
		input("Paste SSH command", 0), input("Name (optional)", 1),
		m.styles.accent.Render("LOCAL · This machine"), input("Local port", 2), m.styles.dim.Render("  The port your app connects to; binds to 127.0.0.1."),
		m.styles.accent.Render("SSH · Connection"), input("SSH host / alias", 3), m.styles.dim.Render("  Uses SSH config and passwordless authentication."),
		m.styles.accent.Render("REMOTE · Destination"), input("Remote host", 4), input("Remote port", 5), m.styles.dim.Render("  Service port, separate from the SSH login port."),
	}
	check := "[ ]"
	if f.auto {
		check = "[x]"
	}
	if f.focus == 6 {
		check = m.styles.accent.Render("› " + check)
	} else {
		check = "  " + check
	}
	lines = append(lines, check+" Auto reconnect after disconnect")
	// Keep the active input visible on shorter terminals while retaining the route.
	focusLines := []int{0, 1, 3, 6, 9, 10, 12}
	available := max(3, h-8)
	start := max(0, focusLines[f.focus]-available+1)
	if start+available > len(lines) {
		start = max(0, len(lines)-available)
	}
	end := min(len(lines), start+available)
	title := "Add local port forward"
	if f.original.ID != "" {
		title = "Edit local port forward"
	}
	var b strings.Builder
	fmt.Fprintln(&b, m.styles.accent.Render(title))
	for _, line := range lines[start:end] {
		fmt.Fprintln(&b, line)
	}
	sshHost := f.fields[3].Value()
	fmt.Fprintln(&b, m.styles.dim.Render("LOCAL (this machine) → VIA SSH → REMOTE (destination)"))
	fmt.Fprintln(&b, m.styles.accent.Render(fmt.Sprintf("127.0.0.1:%s → %s → %s:%s", f.fields[2].Value(), sshHost, f.fields[4].Value(), f.fields[5].Value())))
	hint := "Remote destination is reached from " + sshHost + "."
	if f.fields[4].Value() == "localhost" {
		hint = "Here, remote localhost means " + sshHost + ", not this machine."
	}
	fmt.Fprintln(&b, m.styles.dim.Render(hint))
	if m.message != "" {
		fmt.Fprintln(&b, m.styles.warn.Render(m.message))
	} else {
		fmt.Fprintln(&b)
	}
	fmt.Fprintln(&b, "[Tab/Shift+Tab] Move  [Enter] Parse/Next")
	fmt.Fprint(&b, "[Ctrl+S] Save  [Esc] Cancel  [Space] Toggle checkbox")
	return b.String()
}

func (m Model) logsView(w, h int) string {
	if len(m.state.Tunnels) == 0 {
		return "No tunnel selected. [Esc] Back"
	}
	t := m.state.Tunnels[max(0, min(m.selected, len(m.state.Tunnels)-1))]
	var lines []string
	for _, line := range t.Logs {
		lines = append(lines, strings.Split(ansi.Wrap(line, w, ""), "\n")...)
	}
	end := max(0, len(lines)-min(m.logOffset, max(0, len(lines)-1)))
	start := max(0, end-max(1, h-5))
	return m.styles.accent.Render("EVENT LOG / "+t.Rule.Name) + "\n" + m.status(t) + "\n\n" + strings.Join(lines[start:end], "\n") + "\n" + m.styles.dim.Render("[↑/↓ PgUp/PgDn] Scroll  [End] Latest  [Esc/l/q] Back")
}
func (m Model) helpView() string {
	return m.styles.accent.Render("PORT FWD / Help") + `

  a             Add a tunnel (or paste an SSH -L command)
  e             Edit selected tunnel
  Space         Start / stop (saved across daemon restarts)
  r             Reconnect, including after an authentication error
  d             Delete with confirmation
  l / Enter     Open live event log
  t             Switch terminal / Dracula theme and save
  Up / Down     Select a tunnel
  q / Ctrl+C    Detach TUI; tunnels keep running

  Form: Tab / Shift+Tab to move, Ctrl+S to save, Esc to cancel.
  Enter in the command field parses a single -L command.
  Local port is on this machine. Remote port is the service port.
  Remote localhost is evaluated on the SSH host.
  Totals: SSH bytes received / sent; reset on reconnect.
  Stop the background service: pf daemon stop
  Press any key to return.`
}
