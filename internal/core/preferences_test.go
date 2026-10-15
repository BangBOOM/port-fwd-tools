package core

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestUIPreferencesAreIndependentOfTunnels(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "ui.toml")
	prefs, err := LoadUIPreferences(path)
	if err != nil || prefs.Theme != ThemeTerminal {
		t.Fatalf("missing preferences should follow terminal: %+v %v", prefs, err)
	}
	r, err := ParseCommand("ssh -N -L 15432:localhost:5432 my-server")
	if err != nil {
		t.Fatal(err)
	}
	r.ID = NewID()
	configPath := filepath.Join(home, "config.toml")
	if err = Save(configPath, Config{Version: 1, Rules: []Rule{r}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = SaveUIPreferences(path, UIPreferences{Theme: "Dracula"}); err != nil {
		t.Fatal(err)
	}
	prefs, err = LoadUIPreferences(path)
	if err != nil || prefs.Theme != ThemeDracula {
		t.Fatalf("theme not persisted: %+v %v", prefs, err)
	}
	if err = SaveUIPreferences(path, UIPreferences{Theme: "unknown"}); err == nil {
		t.Fatal("accepted invalid theme")
	}
	prefs, err = LoadUIPreferences(path)
	if err != nil || prefs.Theme != ThemeDracula {
		t.Fatal("invalid save replaced valid preferences")
	}
	after, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("changing UI preferences rewrote tunnel configuration")
	}
	if err = os.WriteFile(path, []byte("theme = 'unknown'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadUIPreferences(path); err == nil {
		t.Fatal("invalid saved theme should produce an actionable error")
	}
}
