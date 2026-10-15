package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/BangBOOM/port-fwd-tools/internal/core"
	"github.com/charmbracelet/x/ansi"
)

func TestThemeSwitchPersistsWithoutLeakingBetweenModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui.toml")
	m := NewWithPreferences(nil, core.UIPreferences{Theme: core.ThemeTerminal}, path)
	other := New(nil)
	before := other.View().Content
	updated, cmd := m.Update(key('t'))
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("theme switch must not issue a daemon command")
	}
	if m.theme != core.ThemeDracula {
		t.Fatal("theme did not switch")
	}
	prefs, err := core.LoadUIPreferences(path)
	if err != nil || prefs.Theme != core.ThemeDracula {
		t.Fatalf("theme not saved: %+v %v", prefs, err)
	}
	if other.theme != core.ThemeTerminal || other.View().Content != before {
		t.Fatal("theme leaked into another TUI instance")
	}
	reopened := NewWithPreferences(nil, prefs, path)
	if reopened.theme != core.ThemeDracula {
		t.Fatal("reopened UI did not restore theme")
	}
	updated, _ = m.Update(key('t'))
	m = updated.(Model)
	if m.theme != core.ThemeTerminal {
		t.Fatal("cannot switch back to terminal")
	}
	prefs, err = core.LoadUIPreferences(path)
	if err != nil || prefs.Theme != core.ThemeTerminal {
		t.Fatal("terminal theme was not saved")
	}
}

func TestTerminalThemeLeavesDefaultsAndFormsUnforced(t *testing.T) {
	m := New(nil)
	m.openForm(nil)
	view := m.View()
	if view.ForegroundColor != nil || view.BackgroundColor != nil {
		t.Fatal("terminal theme must not override terminal defaults")
	}
	if strings.Contains(view.Content, "38;2;") || strings.Contains(view.Content, "48;2;") {
		t.Fatal("terminal theme uses fixed RGB colors")
	}
	before := ansi.Strip(view.Content)
	m.toggleTheme()
	view = m.View()
	if view.ForegroundColor != nil || view.BackgroundColor != nil {
		t.Fatal("Dracula must paint app cells without changing terminal defaults")
	}
	if !strings.Contains(view.Content, "48;2;") {
		t.Fatal("Dracula canvas background was not applied")
	}
	if !strings.Contains(ansi.Strip(view.Content), "REMOTE") {
		t.Fatal("theme switch lost form content")
	}
	m.toggleTheme()
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "LOCAL") || !strings.Contains(before, "LOCAL") {
		t.Fatal("restoring terminal theme lost form")
	}
	if strings.Contains(m.View().Content, "48;2;") {
		t.Fatal("Dracula background leaked into terminal theme")
	}
}
