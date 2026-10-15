package tui

import (
	"fmt"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/BangBOOM/port-fwd-tools/internal/core"
)

type styles struct {
	base, accent, dim, good, warn, bad, selected lipgloss.Style
	inputs                                       textinput.Styles
}

func newStyles(name string) styles {
	// ANSI indices resolve through the user's terminal palette. Leave the
	// default text/background unset, including in blurred text input fields.
	s := styles{
		base:     lipgloss.NewStyle(),
		accent:   lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true),
		dim:      lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		good:     lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		warn:     lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		bad:      lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		selected: lipgloss.NewStyle().Reverse(true),
	}
	s.inputs.Focused = textinput.StyleState{Text: s.base, Placeholder: s.dim, Suggestion: s.dim, Prompt: s.accent}
	s.inputs.Blurred = textinput.StyleState{Text: s.base, Placeholder: s.dim, Suggestion: s.dim, Prompt: s.dim}
	s.inputs.Cursor = textinput.CursorStyle{Blink: true}
	if name != core.ThemeDracula {
		return s
	}
	// Official Dracula palette: https://spec.draculatheme.com/#sec-Standard
	fg, bg := lipgloss.Color("#F8F8F2"), lipgloss.Color("#282A36")
	s.base = lipgloss.NewStyle().Foreground(fg).Background(bg)
	s.accent = lipgloss.NewStyle().Foreground(lipgloss.Color("#BD93F9")).Bold(true)
	s.dim = lipgloss.NewStyle().Foreground(lipgloss.Color("#6272A4"))
	s.good = lipgloss.NewStyle().Foreground(lipgloss.Color("#50FA7B"))
	s.warn = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFB86C"))
	s.bad = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555"))
	s.selected = lipgloss.NewStyle().Foreground(fg).Background(lipgloss.Color("#44475A"))
	s.inputs.Focused = textinput.StyleState{Text: s.base, Placeholder: s.dim.Background(bg), Suggestion: s.dim.Background(bg), Prompt: s.accent.Background(bg)}
	s.inputs.Blurred = textinput.StyleState{Text: s.base, Placeholder: s.dim.Background(bg), Suggestion: s.dim.Background(bg), Prompt: s.dim.Background(bg)}
	s.inputs.Cursor.Color = fg
	return s
}

func (m *Model) toggleTheme() {
	name := core.ThemeDracula
	if m.theme == core.ThemeDracula {
		name = core.ThemeTerminal
	}
	if m.preferencesPath != "" {
		if err := core.SaveUIPreferences(m.preferencesPath, core.UIPreferences{Theme: name}); err != nil {
			m.message = "Could not save theme: " + err.Error()
			return
		}
	}
	m.theme = name
	m.styles = newStyles(name)
	if m.form != nil {
		for i := range m.form.fields {
			m.form.fields[i].SetStyles(m.styles.inputs)
		}
	}
	m.message = fmt.Sprintf("Theme: %s", name)
	m.noticeUntil = time.Now().Add(3 * time.Second)
	if m.preferencesPath != "" {
		m.message += " (saved)"
	}
}
