# pf — persistent SSH port forwarding

A small, local TUI for managing SSH `-L` tunnels on macOS and Linux. The English
interface uses a stacked layout: a spacious tunnel list above, a compact selected
tunnel panel below. Both local ports and remote destinations are visible alongside
the SSH host and connection status.
Closing the TUI or terminal leaves tunnels running in a detached background process.

## Installation

Requires Go 1.27.1 or newer to build, and the OpenSSH `ssh` client at runtime.

```sh
go install github.com/BangBOOM/port-fwd-tools/cmd/pf@latest
pf
```

Make sure your Go binary directory is in `PATH`. With the default Go settings,
fish users can add it persistently with:

```fish
fish_add_path (go env GOPATH)/bin
```

If you have configured `GOBIN`, add that directory instead.

To build from source:

```sh
git clone https://github.com/BangBOOM/port-fwd-tools.git
cd port-fwd-tools
make build
./bin/pf
```

The first invocation starts the daemon automatically. No root permissions or
login startup registration are needed. A plain `go run` is useful for development,
but use the built binary for everyday use: it must remain available to start
the daemon again.

## Your first tunnel

Your existing host aliases, identities, SSH login ports, and jump hosts come from
your SSH configuration. In the examples, `my-server` is a placeholder for your
hostname or SSH host alias. Verify that passwordless authentication already works:

```sh
ssh my-server
```

For a new host, verify and accept its host key there first. `pf` never automatically
accepts unknown or changed host keys, and never asks for a password, passphrase, or
verification code. Load encrypted keys into `ssh-agent` before using `pf`.

Open `pf`, press **a**, then paste:

```sh
ssh -N -L 3430:localhost:3430 my-server
```

Press **Enter** to parse, review the endpoints, then **Ctrl+S** to save and start.
You can also fill the fields directly. **Tab / Shift+Tab** moves between fields;
**Space / Enter** toggles the auto reconnect checkbox when it has focus.

The two ports can be different. For example:

```text
LOCAL (this machine)      VIA SSH       REMOTE (destination)
127.0.0.1:15432       →   my-server  →   localhost:5432
```

- **Local port** is the port your local application connects to, here `15432`.
- **Remote host / port** is the service address as reached from the SSH host,
  here `localhost:5432` on my-server.
- **Remote port** is not the SSH login port. Configure that login port in
  `~/.ssh/config` using `Port`.
- Local listeners always bind to `127.0.0.1` in this MVP.

Existing SSH processes in other terminal tabs cannot be adopted. Save the rule,
stop the old SSH process to release its local port, then start the rule in `pf`.
If the port is occupied, `pf` reports the error and waits for you to reconnect.

## TUI keys

| Key | Action |
| --- | --- |
| Up / Down, k / j | Select a tunnel |
| a | Add a tunnel or paste an SSH command |
| e | Edit the selected tunnel |
| Space | Start / stop; the choice is saved |
| r | Start or reconnect, including after a paused error |
| d | Delete a rule and stop its tunnel, with confirmation |
| l / Enter | Open the live event log |
| t | Switch between terminal and Dracula themes; save selection |
| ? | Show help |
| q / Ctrl+C | Detach TUI; tunnels keep running |

In the log view, use arrows or Page Up / Page Down to scroll, **End** for latest,
and **Esc / l / q** to return. Terminal minimum: 54 columns × 20 rows;
80 × 24 or larger is recommended. The list expands with the terminal height;
the selected tunnel and action hints stay visible at the bottom. Long hostnames
are shortened while local and remote port numbers remain intact. List order stays
stable as connection states change.

Normal connections show connection age and auto reconnect settings in the detail
panel. Connection issues show a short error reason, retry countdown, and reconnect
action. Full SSH output is available through **l / Enter**, rather than appearing
inline. The footer shows frequently used actions and changes **Start / Stop** to
match the selected tunnel. **d Delete** stays visible even in narrow windows;
**?** lists all keys, including themes and actions omitted at smaller widths.

Press **d** to replace the selected tunnel's detail panel with a deletion
confirmation showing its name, local port, SSH host, and remote destination.
Press **y** to stop forwarding, cancel automatic retries, and remove the saved
rule. **n / Esc / Enter** cancels; no action runs until you confirm. The selected
rule's identity and displayed route stay fixed while background state refreshes.
If another client removes that rule, its confirmation closes without targeting
another row. Failures show the reason and allow retrying or cancelling.
Successful deletion briefly shows `Deleted "NAME".` and selects the next row,
or the previous row when deleting the last item. Deleting the only rule returns
to the empty list. Confirmation reuses the existing panel and adds no background
workers or dependencies.

## Traffic totals

At 70 columns or wider, **Total ↓ / ↑** shows cumulative received / sent bytes
for each visible connected tunnel, for example `137KB / 102KB`. At smaller widths,
the selected tunnel's totals appear in the detail panel so both ports stay visible.
Download is SSH host → this machine; upload is this machine → SSH host.

These are OpenSSH's rounded **transport totals for the current SSH connection**,
including protocol overhead. They reset on reconnect, and are not precise payload
totals, real-time rates, or a history across connections. Units are OpenSSH's own
scaled byte values (powers of 1024).

The TUI reads counters from the existing master's local control socket every two
seconds, only for visible connected rows. It pauses statistics sampling in forms,
help, logs, and when the terminal reports loss of focus. A batch has a 200 ms total
timeout. There are no extra subprocesses, packet capture, forwarding proxies,
runtime dependencies, or disk writes for statistics. Closing the TUI stops all
statistics work; the background forwarding path is unchanged.

Counters require an OpenSSH master advertising the `info` control extension
(verified with macOS OpenSSH 10.3). Older versions show **n/a** and continue
forwarding normally; unsupported masters are checked once per connection.
**…** means not yet sampled, and **—** means not connected. A temporary read error
also shows **n/a** and is retried on a later refresh. CLI `list` output is unchanged.
No daemon restart is needed to use this TUI feature.

## Appearance

The default **terminal** theme inherits your terminal's foreground, background,
and ANSI palette. It follows the colors configured in your terminal, including
light or dark terminal profiles. Form inputs use the same palette as the rest
of the interface.

**Dracula** uses the [official Dracula palette](https://spec.draculatheme.com/)
for the application's background, text, selection, forms, status colors, and logs.
Only the application's cells are colored; your terminal's global palette and
default colors are not changed.
Colors respect `NO_COLOR` and terminal color capabilities; terminals without
truecolor support display the closest available palette colors.

```sh
# Save a default for future TUI sessions; no daemon restart is needed.
./bin/pf theme dracula
./bin/pf theme terminal

# Print the saved theme (terminal if no preference exists).
./bin/pf theme

# Override the theme for one session without changing the saved preference.
./bin/pf --theme dracula
./bin/pf tui --theme terminal
```

In the main TUI view, **t** switches themes immediately and saves the choice.
The setting is stored as `theme = 'terminal'` or `theme = 'dracula'` in `ui.toml`
beside `config.toml`. Appearance settings are separate from tunnel configuration,
so changing themes does not interrupt or restart tunnels. Existing open TUI
sessions retain their theme until you switch it there or reopen the interface.

## CLI

```sh
# Import a single forwarding. Quote the command as one argument.
./bin/pf import --name server-app 'ssh -N -L 3430:localhost:3430 my-server'

# Or add through explicit fields; different local and remote ports are normal.
./bin/pf add --name database --ssh-host my-server \
  --local-port 15432 --remote-host localhost --remote-port 5432

# Save without starting, or opt out of automatic reconnect.
./bin/pf add --name notebook --ssh-host my-server \
  --local-port 18888 --remote-port 8888 --stopped
./bin/pf add --help

./bin/pf list
./bin/pf list --json
./bin/pf stop database
./bin/pf start database
./bin/pf restart database
./bin/pf logs database
./bin/pf delete database

# Control the background service separately from individual rules.
./bin/pf daemon status
./bin/pf daemon stop
./bin/pf daemon start
./bin/pf config
```

Commands accept a tunnel name, full ID, or an unambiguous ID prefix. `delete`
from the CLI acts immediately; the TUI asks for confirmation.

Import supports one `-L`, optional `-N`, `-T`, `-n` (including combined flags), and
an SSH host / alias. It supports quoted arguments and bracketed IPv6 remote
destinations. Other SSH flags, shell operators, and remote commands are rejected;
put login settings such as `Port`, `IdentityFile`, and `ProxyJump` in SSH config.
Imported commands are parsed as data and never executed by a shell.

## Background operation and reconnect

- One daemon per config directory. A private Unix socket connects the TUI/CLI to it.
- One independent OpenSSH master process per enabled rule, using a private control
  socket. It does not share your unrelated SSH masters.
- The daemon marks a tunnel connected only after its private SSH master responds
  to a control check. `ExitOnForwardFailure=yes` makes listener setup errors fatal.
- SSH heartbeats run after 15 seconds of inactivity; three missed responses normally
  close an unresponsive session after roughly 45 seconds. The daemon then reconnects.
- Network failures retry with exponential backoff, approximately 1, 2, 4, 8…60
  seconds, with jitter. A stable connection resets the backoff.
- Authentication, host key, bind, and recognized configuration errors pause retries
  with **Needs action**. Fix the issue and press **r** / `pf restart NAME`.
- Stopping an individual rule saves `enabled = false`. It stays stopped across
  TUI sessions and daemon restarts. Editing a stopped rule keeps it stopped.
- Stopping the daemon terminates its SSH process groups, including jump processes,
  and preserves rule settings. Enabled rules resume on the next daemon start.
- On reconnect attempts, the daemon uses the latest nonempty `SSH_AUTH_SOCK`
  received from an attached CLI/TUI, so a new terminal can refresh the agent path.
- **Connected** describes the SSH tunnel, not the destination service. A listening
  tunnel does not prove the remote application is healthy. Service probing is outside
  this MVP. Existing application sessions need to reconnect after a tunnel interruption.

The daemon survives terminal closure, but this MVP does not install launchd or
systemd units. After logout, reboot, or a daemon crash, open `pf` again to restart
the daemon and restore enabled rules. Sleep pauses processes; SSH detects stale
connections after the machine wakes. State shown while offline may take the
heartbeat timeout to catch up.

## Configuration and logs

`pf config` prints the exact paths, including the separate `ui.toml` appearance
preferences. Defaults follow Go's user config directory:

- macOS: `~/Library/Application Support/port-fwd-tools/config.toml`
- Linux: `${XDG_CONFIG_HOME:-~/.config}/port-fwd-tools/config.toml`

Use `PF_HOME=/some/directory` or `pf --home /some/directory` for a separate instance.
`--home` goes before the subcommand. Runtime sockets live in a private hashed
directory under `/tmp` to stay within macOS Unix socket path limits.

The TOML file stores rules and enabled / auto reconnect settings. Writes are atomic;
duplicate names and local ports are rejected, including ports assigned to stopped
rules. To edit TOML manually, stop the daemon first; running instances own their
in-memory config and may overwrite manual edits. Restart to load the changed file.

Example saved rule:

```toml
version = 1

[[tunnels]]
id = '0123456789abcdef'
name = 'database'
ssh_host = 'my-server'
local_port = 15432
remote_host = 'localhost'
remote_port = 5432
enabled = true
auto_reconnect = true
```

Each tunnel retains its latest 80 events in memory. Events reset when the daemon
restarts. `daemon.log` contains daemon startup/errors, not a persistent tunnel
event history; it rotates on daemon startup once larger than 1 MiB.

## Development and verification

```sh
make test                    # all tests with the race detector
go vet ./...
make build
GOOS=linux GOARCH=amd64 go build -o dist/pf-linux-amd64 ./cmd/pf
```

Integration tests generate temporary keys and host trust, then use the real local
OpenSSH client against a temporary in-process SSH server. They verify data forwarding,
recovery after connection loss, manual stop persistence, edits/deletion, port conflicts,
noninteractive authentication failure, and native traffic counters after a real
bidirectional transfer (when OpenSSH supports them). They never connect to your
SSH hosts or modify your SSH configuration. CLI tests also verify that the daemon survives the launching
command and preserves configuration across restarts.

Current scope: macOS and Linux, local TCP `-L` forwarding, passwordless authentication.
Remote `-R`, SOCKS `-D`, password prompts, login autostart, and persistent event history
are deferred.

## Resource measurements

A local test on an Apple M5 with 32 GiB RAM used one SSH tunnel, an 80 × 24 TUI,
and sustained upload and download of 10 MiB/s each. Each window had an 8-second
warmup followed by 40 seconds of measurement. The statistics-enabled and
statistics-disabled cases ran twice in reversed order; background-only cases ran
once. The baseline used the same source with only statistics sampling disabled.

| Scenario | pf CPU | OpenSSH CPU | Combined RSS |
| --- | ---: | ---: | ---: |
| Background only, idle | <0.01% | <0.01% | 18.1 MiB |
| TUI open, idle, statistics enabled | 0.72% | 0.01% | 38.6 MiB |
| TUI open, bidirectional transfer, statistics enabled | 0.78% | 16.34% | 40.7 MiB |
| Background only, bidirectional transfer | <0.01% | 24.22% | 19.0 MiB |

CPU percentages use 100% for one core. The pf column includes its TUI and daemon;
the SSH column contains the client OpenSSH process. RSS includes shared mappings
and is summed across those processes. The fixture server, load generator, and
terminal emulator are excluded. OpenSSH CPU varied across runs; these numbers do
not imply that closing the TUI increases CPU use.

During transfer, statistics increased pf CPU by approximately 0.10 percentage
points and combined mean RSS by approximately 1 MiB. At idle there was no
consistent pf CPU increase; combined mean RSS increased by approximately 0.7 MiB.
These measurements describe this local setup, not a cross-platform performance
guarantee.

The macOS-only measurement and analysis scripts are in
[`benchmarks/2026-10-15`](benchmarks/2026-10-15):

```sh
make build
python3 benchmarks/2026-10-15/measure.py
python3 benchmarks/2026-10-15/analyze.py
```

The measurement script defaults to `bin/pf`; use `--binary PATH` to measure a
matching installed build. It builds an isolated local SSH fixture and a temporary
baseline binary, without using your SSH configuration or existing tunnels.
Raw measurements and logs are generated locally and are not tracked.
