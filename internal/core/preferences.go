package core

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	ThemeTerminal = "terminal"
	ThemeDracula  = "dracula"
)

// UI preferences are independent of daemon configuration: changing appearance
// never rewrites tunnel rules or requires interrupting a running SSH connection.
type UIPreferences struct {
	Theme string `toml:"theme"`
}

func NormalizeTheme(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "", ThemeTerminal:
		return ThemeTerminal, nil
	case ThemeDracula:
		return ThemeDracula, nil
	}
	return "", fmt.Errorf("unknown theme %q; choose terminal or dracula", name)
}

func LoadUIPreferences(path string) (UIPreferences, error) {
	prefs := UIPreferences{Theme: ThemeTerminal}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return prefs, nil
	}
	if err != nil {
		return prefs, err
	}
	if err = toml.Unmarshal(b, &prefs); err != nil {
		return prefs, fmt.Errorf("read UI preferences: %w", err)
	}
	prefs.Theme, err = NormalizeTheme(prefs.Theme)
	return prefs, err
}

func SaveUIPreferences(path string, prefs UIPreferences) error {
	var err error
	prefs.Theme, err = NormalizeTheme(prefs.Theme)
	if err != nil {
		return err
	}
	b, err := toml.Marshal(prefs)
	if err != nil {
		return err
	}
	return writeAtomic(path, b)
}
