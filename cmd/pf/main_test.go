package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/BangBOOM/port-fwd-tools/internal/daemon"
)

// The helper behaves exactly like the installed binary, including daemon respawn.
func TestMain(m *testing.M) {
	if os.Getenv("PF_TEST_CLI") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestDetachedDaemonCLI(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "config")
	call := func(args ...string) ([]byte, error) {
		cmd := exec.Command(exe, append([]string{"--home", home}, args...)...)
		cmd.Env = append(os.Environ(), "PF_TEST_CLI=1")
		return cmd.CombinedOutput()
	}
	t.Cleanup(func() {
		_, _ = call("daemon", "stop")
		paths, _ := daemon.ResolvePaths(home)
		_ = os.RemoveAll(paths.Runtime)
	})
	must := func(args ...string) []byte {
		t.Helper()
		out, err := call(args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}
	must("import", "--stopped", "--name", "database", "ssh -N -L 15432:localhost:5432 my-server")
	var s daemon.Snapshot
	if err = json.Unmarshal(must("list", "--json"), &s); err != nil {
		t.Fatal(err)
	}
	if s.PID == os.Getpid() || len(s.Tunnels) != 1 || s.Tunnels[0].Rule.LocalPort != 15432 || s.Tunnels[0].Rule.RemotePort != 5432 || s.Tunnels[0].Rule.Enabled {
		t.Fatalf("bad detached daemon state: %+v", s)
	}
	if _, err = call("import", "--stopped", "ssh -N -L 15432:localhost:9999 my-server"); err == nil {
		t.Fatal("accepted duplicate local port")
	}
	must("daemon", "stop")
	must("daemon", "start")
	if err = json.Unmarshal(must("list", "--json"), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Tunnels) != 1 || s.Tunnels[0].Rule.Enabled || s.Tunnels[0].Status != "stopped" {
		t.Fatal("stopped intent changed after daemon restart")
	}
	must("delete", "database")
	if err = json.Unmarshal(must("list", "--json"), &s); err != nil || len(s.Tunnels) != 0 {
		t.Fatal("delete failed")
	}
}

func TestThemeSettingsWorkWithoutStartingDaemon(t *testing.T) {
	home := t.TempDir()
	paths, err := daemon.ResolvePaths(home)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(paths.Runtime)
	for _, args := range [][]string{{"theme"}, {"theme", "dracula"}, {"theme", "terminal"}} {
		if err = run(append([]string{"--home", home}, args...)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = os.Stat(paths.Config); !os.IsNotExist(err) {
		t.Fatal("theme command touched daemon configuration")
	}
	if _, err = os.Stat(filepath.Join(home, "daemon.pid")); !os.IsNotExist(err) {
		t.Fatal("theme command started daemon")
	}
	if err = run([]string{"--home", home, "theme", "invalid"}); err == nil {
		t.Fatal("invalid theme was accepted")
	}
	if err = run([]string{"--home", home, "--theme", "invalid"}); err == nil {
		t.Fatal("invalid theme flag was accepted")
	}
	if err = run([]string{"--home", home, "tui", "--theme", "invalid"}); err == nil {
		t.Fatal("invalid TUI theme flag was accepted")
	}
}
