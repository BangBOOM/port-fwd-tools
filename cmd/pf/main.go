package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/BangBOOM/port-fwd-tools/internal/core"
	"github.com/BangBOOM/port-fwd-tools/internal/daemon"
	"github.com/BangBOOM/port-fwd-tools/internal/tui"
)

const version = "0.1.2"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pf:", err)
		os.Exit(1)
	}
}
func usage() {
	fmt.Print(`pf — Persistent SSH local port forwarding

Usage: pf [--home DIR] [--theme terminal|dracula] [command]

  (no command) / tui    Open the English TUI; starts daemon if needed
  add                  Add a tunnel using named flags (see pf add --help)
  import 'ssh -N -L LOCAL:REMOTE_HOST:REMOTE_PORT SSH_HOST'
                       Import one local forwarding; optional --name NAME
  list [--json]        Show tunnel IDs, both ports, and connection status
  start NAME_OR_ID     Enable and start a saved tunnel
  stop NAME_OR_ID      Stop a tunnel and keep it stopped
  restart NAME_OR_ID   Start / reconnect, including after an error
  delete NAME_OR_ID    Delete a rule and stop its tunnel
  logs NAME_OR_ID      Print recent in-memory events
  daemon start         Start the detached background service
  daemon status        Show status without starting anything
  daemon stop          Stop the service and its SSH children
  config               Print config and runtime paths
  theme [NAME]         Show / save UI theme: terminal (default) or dracula
  version              Print version

TUI: a Add · e Edit · Space Start/Stop · r Reconnect · d Delete · l Logs · t Theme
     q / Ctrl+C detaches; tunnels keep running in the background.

Passwordless SSH only. Uses ~/.ssh/config and ssh-agent. Verify new hosts
with ssh HOST first. Local listeners bind to 127.0.0.1. No login autostart
is installed. Use PF_HOME or --home to choose an isolated config directory.
`)
}

func flags(name string) *flag.FlagSet { return flag.NewFlagSet(name, flag.ContinueOnError) }
func run(args []string) error {
	root := flags("pf")
	home := root.String("home", "", "config directory (also PF_HOME)")
	theme := root.String("theme", "", "TUI theme for this session: terminal or dracula")
	root.Usage = usage
	if err := root.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	args = root.Args()
	command := "tui"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}
	if command == "help" {
		usage()
		return nil
	}
	if command == "version" {
		fmt.Println("pf", version)
		return nil
	}
	paths, err := daemon.ResolvePaths(*home)
	if err != nil {
		return err
	}
	if command == "config" {
		fmt.Printf("Config: %s\nUI preferences: %s\nDaemon log: %s\nSocket: %s\n", paths.Config, filepath.Join(paths.Home, "ui.toml"), paths.Log, paths.Socket)
		return nil
	}
	preferencesPath := filepath.Join(paths.Home, "ui.toml")
	if command == "theme" {
		if len(args) > 1 {
			return errors.New("usage: pf theme [terminal|dracula]")
		}
		if len(args) == 1 {
			name, err := core.NormalizeTheme(args[0])
			if err != nil {
				return err
			}
			if err = core.SaveUIPreferences(preferencesPath, core.UIPreferences{Theme: name}); err != nil {
				return err
			}
			fmt.Printf("Theme saved: %s\n", name)
			return nil
		}
		prefs, err := core.LoadUIPreferences(preferencesPath)
		if err != nil {
			return err
		}
		fmt.Println(prefs.Theme)
		return nil
	}
	var preferences core.UIPreferences
	client := daemon.NewClient(paths)
	if command == "daemon" {
		if len(args) != 1 {
			return errors.New("usage: pf daemon start|status|stop|run")
		}
		switch args[0] {
		case "run":
			return daemon.Serve(paths)
		case "start":
			if err = daemon.Ensure(paths); err == nil {
				fmt.Println("Daemon running. Open pf to manage tunnels.")
			}
			return err
		case "status":
			s, e := client.Do(daemon.Request{Action: "state"})
			if e != nil {
				fmt.Println("Daemon not running.")
				return nil
			}
			fmt.Printf("Daemon running (PID %d), %d tunnels\n", s.PID, len(s.Tunnels))
			return nil
		case "stop":
			if _, err = client.Do(daemon.Request{Action: "state"}); err != nil {
				fmt.Println("Daemon not running.")
				return nil
			}
			if _, err = client.Do(daemon.Request{Action: "shutdown"}); err != nil {
				return err
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				_, pidErr := os.Stat(filepath.Join(paths.Home, "daemon.pid"))
				if _, err = client.Do(daemon.Request{Action: "state"}); err != nil && os.IsNotExist(pidErr) {
					fmt.Println("Daemon stopped. Saved enabled tunnels resume next time it starts.")
					return nil
				}
				time.Sleep(100 * time.Millisecond)
			}
			return errors.New("daemon is still shutting down")
		default:
			return errors.New("usage: pf daemon start|status|stop")
		}
	}
	var req daemon.Request
	switch command {
	case "tui":
		f := flags("tui")
		f.StringVar(theme, "theme", *theme, "theme for this session: terminal or dracula")
		if err = f.Parse(args); errors.Is(err, flag.ErrHelp) {
			return nil
		} else if err != nil {
			return err
		}
		if f.NArg() != 0 {
			return errors.New("usage: pf tui [--theme terminal|dracula]")
		}
		if *theme != "" {
			preferences.Theme, err = core.NormalizeTheme(*theme)
		} else {
			preferences, err = core.LoadUIPreferences(preferencesPath)
		}
		if err != nil {
			return err
		}
	case "add":
		f := flags("add")
		name := f.String("name", "", "optional tunnel name")
		host := f.String("ssh-host", "", "SSH host / alias, e.g. my-server")
		local := f.Int("local-port", 0, "port on this machine")
		remoteHost := f.String("remote-host", "localhost", "destination as reached from the SSH host")
		remote := f.Int("remote-port", 0, "destination service port, not SSH login port")
		stopped := f.Bool("stopped", false, "save without starting")
		noRetry := f.Bool("no-reconnect", false, "disable automatic reconnect")
		if err = f.Parse(args); errors.Is(err, flag.ErrHelp) {
			return nil
		} else if err != nil {
			return err
		}
		if f.NArg() != 0 {
			return errors.New("unexpected positional arguments for add")
		}
		r := core.Rule{Name: strings.TrimSpace(*name), SSHHost: *host, LocalPort: *local, RemoteHost: *remoteHost, RemotePort: *remote, Enabled: !*stopped, AutoReconnect: !*noRetry}
		if r.Name == "" {
			r.Name = fmt.Sprintf("%s:%d", r.SSHHost, r.LocalPort)
		}
		if err = core.ValidateRule(r); err != nil {
			return err
		}
		req = daemon.Request{Action: "add", Rule: r}
	case "import":
		f := flags("import")
		name := f.String("name", "", "optional tunnel name")
		stopped := f.Bool("stopped", false, "save without starting")
		if err = f.Parse(args); errors.Is(err, flag.ErrHelp) {
			return nil
		} else if err != nil {
			return err
		}
		if f.NArg() != 1 {
			return errors.New("usage: pf import [--name NAME] [--stopped] 'ssh -N -L LOCAL:REMOTE_HOST:REMOTE_PORT SSH_HOST'")
		}
		r, e := core.ParseCommand(f.Arg(0))
		if e != nil {
			return e
		}
		if *name != "" {
			r.Name = *name
		}
		r.Enabled = !*stopped
		req = daemon.Request{Action: "add", Rule: r}
	case "list":
		if len(args) > 1 || (len(args) == 1 && args[0] != "--json") {
			return errors.New("usage: pf list [--json]")
		}
		req.Action = "state"
	case "start", "stop", "restart", "delete", "logs":
		if len(args) != 1 {
			return fmt.Errorf("usage: pf %s NAME_OR_ID", command)
		}
		req = daemon.Request{Action: command, ID: args[0]}
		if command == "logs" {
			req.Action = "state"
		}
	default:
		return fmt.Errorf("unknown command %q; run pf --help", command)
	}
	if err = daemon.Ensure(paths); err != nil {
		return err
	}
	if command == "tui" {
		if _, err = tea.NewProgram(tui.NewWithPreferences(client, preferences, preferencesPath)).Run(); err != nil {
			return err
		}
		fmt.Println("TUI detached. Tunnels keep running. Stop background service: pf daemon stop")
		return nil
	}
	s, err := client.Do(req)
	if err != nil {
		return err
	}
	if command == "list" && len(args) == 1 {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(s)
	}
	if command == "logs" {
		var matches []daemon.Tunnel
		for _, t := range s.Tunnels {
			if t.Rule.ID == args[0] || t.Rule.Name == args[0] {
				matches = []daemon.Tunnel{t}
				break
			}
			if strings.HasPrefix(t.Rule.ID, args[0]) {
				matches = append(matches, t)
			}
		}
		if len(matches) != 1 {
			return errors.New("tunnel not found or ID is ambiguous")
		}
		fmt.Println(strings.Join(matches[0].Logs, "\n"))
		return nil
	}
	printState(s)
	return nil
}

func printState(s daemon.Snapshot) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "ID\tNAME\tLOCAL\tVIA SSH\tREMOTE\tSTATUS\tENABLED")
	for _, t := range s.Tunnels {
		r := t.Rule
		fmt.Fprintf(w, "%s\t%s\t127.0.0.1:%d\t%s\t%s:%d\t%s\t%t\n", r.ID, r.Name, r.LocalPort, r.SSHHost, r.RemoteHost, r.RemotePort, t.Status, r.Enabled)
		if t.LastError != "" {
			fmt.Fprintf(w, "\tError: %s\n", t.LastError)
		}
	}
}
