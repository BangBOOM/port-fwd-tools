package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportPortsAndValidation(t *testing.T) {
	for _, command := range []string{
		"ssh -N -L 15432:localhost:5432 my-server",
		"ssh -nNT -L15432:localhost:5432 my-server",
		"ssh my-server -L 127.0.0.1:15432:localhost:5432 -N",
	} {
		r, err := ParseCommand(command)
		if err != nil {
			t.Fatal(err)
		}
		if r.LocalPort != 15432 || r.RemotePort != 5432 || r.SSHHost != "my-server" || r.ForwardSpec() != "127.0.0.1:15432:localhost:5432" {
			t.Fatalf("ports or host reversed: %+v", r)
		}
	}
	r, err := ParseCommand("ssh -N -L 15432:[::1]:5432 my-server")
	if err != nil || r.ForwardSpec() != "127.0.0.1:15432:[::1]:5432" {
		t.Fatalf("IPv6: %+v %v", r, err)
	}
	for _, command := range []string{
		"ssh -N -L 0:localhost:5432 my-server", "ssh -N -L 15432:localhost:70000 my-server",
		"ssh -N -L 0.0.0.0:15432:localhost:5432 my-server", "ssh -N -L 15432:localhost:5432 my-server ; touch /tmp/pf-unwanted",
		"ssh -N -L 15432:localhost:5432 -L 1111:localhost:2222 my-server", "ssh -p 22 -L 15432:localhost:5432 my-server",
	} {
		if _, err = ParseCommand(command); err == nil {
			t.Errorf("accepted unsupported input: %s", command)
		}
	}
}

func TestAtomicConfigAndStoppedIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	r, err := ParseCommand("ssh -N -L 15432:localhost:5432 my-server")
	if err != nil {
		t.Fatal(err)
	}
	r.ID = NewID()
	r.Enabled = false
	c := Config{Version: 1, Rules: []Rule{r}}
	if err = Save(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Rules[0].Enabled {
		t.Fatal("manual stop was not persisted")
	}
	duplicate := r
	duplicate.ID = NewID()
	duplicate.Name = "other"
	c.Rules = append(c.Rules, duplicate)
	if err = Save(path, c); err == nil || !strings.Contains(err.Error(), "local port") {
		t.Fatalf("expected local port conflict: %v", err)
	}
	loaded, err = Load(path)
	if err != nil || len(loaded.Rules) != 1 {
		t.Fatalf("failed write changed config: %+v %v", loaded, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions: %v %v", info, err)
	}
}
