package core

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"
)

type Rule struct {
	ID            string `toml:"id" json:"id"`
	Name          string `toml:"name" json:"name"`
	SSHHost       string `toml:"ssh_host" json:"ssh_host"`
	LocalPort     int    `toml:"local_port" json:"local_port"`
	RemoteHost    string `toml:"remote_host" json:"remote_host"`
	RemotePort    int    `toml:"remote_port" json:"remote_port"`
	Enabled       bool   `toml:"enabled" json:"enabled"`
	AutoReconnect bool   `toml:"auto_reconnect" json:"auto_reconnect"`
}

type Config struct {
	Version int    `toml:"version"`
	Rules   []Rule `toml:"tunnels"`
}

func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func clean(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) }) < 0
}

func ValidateRule(r Rule) error {
	if r.ID != "" && !regexp.MustCompile(`^[a-f0-9]{16}$`).MatchString(r.ID) {
		return errors.New("invalid tunnel ID")
	}
	if strings.TrimSpace(r.Name) == "" || len(r.Name) > 80 || !clean(r.Name) {
		return errors.New("name must contain 1–80 characters without control characters")
	}
	if !regexp.MustCompile(`^(?:[a-zA-Z0-9_.+-]+@)?(?:[a-zA-Z0-9_][a-zA-Z0-9_.:-]*|\[[a-zA-Z0-9_.:%-]+\])$`).MatchString(r.SSHHost) {
		return errors.New("SSH host must be a host or alias, such as my-server or user@host")
	}
	if r.LocalPort < 1 || r.LocalPort > 65535 {
		return errors.New("local port must be between 1 and 65535")
	}
	if r.RemotePort < 1 || r.RemotePort > 65535 {
		return errors.New("remote port must be between 1 and 65535")
	}
	h := strings.TrimPrefix(strings.TrimSuffix(r.RemoteHost, "]"), "[")
	if h == "" || (net.ParseIP(h) == nil && !regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$`).MatchString(h)) {
		return errors.New("remote host must be a hostname or IP address as reached from the SSH host")
	}
	return nil
}

func Validate(c Config) error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version %d (expected 1)", c.Version)
	}
	ids, ports, names := map[string]bool{}, map[int]bool{}, map[string]bool{}
	for _, r := range c.Rules {
		if err := ValidateRule(r); err != nil {
			return fmt.Errorf("tunnel %q: %w", r.Name, err)
		}
		if r.ID == "" || ids[r.ID] {
			return errors.New("missing or duplicate tunnel ID")
		}
		if ports[r.LocalPort] {
			return fmt.Errorf("local port %d is already assigned to a tunnel", r.LocalPort)
		}
		if names[r.Name] {
			return fmt.Errorf("tunnel name %q is already in use", r.Name)
		}
		ids[r.ID], ports[r.LocalPort], names[r.Name] = true, true, true
	}
	return nil
}

func Load(path string) (Config, error) {
	c := Config{Version: 1, Rules: []Rule{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err = toml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("read config: %w", err)
	}
	return c, Validate(c)
}

// Save atomically replaces the config, so an interrupted write cannot truncate it.
func Save(path string, c Config) error {
	if err := Validate(c); err != nil {
		return err
	}
	b, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	return writeAtomic(path, b)
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (r Rule) ForwardSpec() string {
	host := strings.TrimPrefix(strings.TrimSuffix(r.RemoteHost, "]"), "[")
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("127.0.0.1:%d:%s:%d", r.LocalPort, host, r.RemotePort)
}
