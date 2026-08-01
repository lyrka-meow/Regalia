# Regalia

Regalia is an independent, Linux-first VPN control service designed for
terminal use, automation, and desktop-shell integration.

The project consists of:

- `regaliad` — a per-user daemon with a versioned JSON API over a Unix socket;
- `regalia` — a numbered terminal interface and a non-interactive CLI;
- `regalia-engine` — a validating wrapper for the capability-restricted TUN
  service;
- internal packages for subscriptions, server selection, application discovery,
  route profiles, persistent state, and sing-box configuration generation.

Regalia is implemented as a clean Go project and does not inherit another
project's source tree or Git history.

## Current capabilities

- create, refresh, list, and delete subscription profiles;
- automatically send the Throne-compatible `x-hwid`, `x-device-os`,
  `x-ver-os`, and `x-device-model` headers when downloading subscriptions;
- parse common VMess, VLESS, Trojan, Shadowsocks, AnyTLS, Hysteria2, TUIC,
  SOCKS, HTTP, and JSON server formats;
- keep the previous valid server list when a subscription refresh fails;
- select a connection-ready server;
- discover installed Linux applications from `.desktop` files while reading
  exact routing paths only from `/proc/PID/exe` after the process is running;
- persist route profiles with `proxy` or `direct` application rules;
- generate a sing-box TUN configuration;
- validate configurations with `sing-box check` before every connection;
- start and stop sing-box through a capability-restricted systemd service;
- remember whether VPN is enabled and restore it when the user service starts;
- expose the state through a private per-user Unix socket.

The engine controller reports `unavailable`, `stopped`, `starting`,
`connected`, `stopping`, or `failed`. A connection is reported as active only
after sing-box survives its startup window. Early failures and the tail of the
private engine log are exposed through the status API.

`regaliad` and the shell remain ordinary user processes. Only the
`regalia-engine@UID.service` instance receives `CAP_NET_ADMIN`, and it executes
the engine as that same UID. A polkit rule permits a user to manage only the
instance matching their own numeric UID. Before sing-box is started, the
wrapper reads the private configuration once, validates the restricted Regalia
schema, verifies the root-owned sing-box binary, and feeds those same bytes to
both `sing-box check` and `sing-box run`.

## Build and test

Regalia currently uses only the Go standard library.

```bash
go test ./...
go build -o bin/regaliad ./cmd/regaliad
go build -o bin/regalia ./cmd/regalia
go build -o bin/regalia-engine ./cmd/regalia-engine
```

For development without TUN privileges, start the daemon in direct process
mode:

```bash
go run ./cmd/regaliad --engine-mode process --engine /path/to/sing-box
```

Open the terminal interface in another terminal:

```bash
go run ./cmd/regalia
```

Non-interactive examples:

```bash
go run ./cmd/regalia status
go run ./cmd/regalia connect
go run ./cmd/regalia disconnect
go run ./cmd/regalia apps
go run ./cmd/regalia processes
go run ./cmd/regalia profile add Main https://example.com/subscription
go run ./cmd/regalia profiles
go run ./cmd/regalia servers
```

By default, the API socket is
`$XDG_RUNTIME_DIR/regalia/regaliad.sock`, and persistent state is stored in
`$XDG_CONFIG_HOME/regalia/state.json` or `~/.config/regalia/state.json`.
The generated engine configuration and log live in the same private runtime
directory and are created with mode `0600`.

Subscription refreshes always send the local machine ID, operating-system
name, kernel version, and distribution name in the compatibility headers used
by device-bound subscription providers. This is automatic and intentionally
has no desktop toggle, so a subscription behaves the same in the TUI and in a
desktop shell.

Application routing intentionally does not support AppImage. AppImage mounts
its executable below a different temporary `/tmp/.mount_*` directory on every
launch, so it cannot provide a stable exact `process_path` rule.

Arch packaging paths and the privilege boundary are documented in
[`packaging/README.md`](packaging/README.md). The repository does not bundle a
sing-box executable; packaging must install an official, root-owned binary at
`/usr/lib/regalia/sing-box`.

The API and trust boundaries are described in
[`docs/architecture.md`](docs/architecture.md).
The shell-facing methods and status fields are documented in
[`docs/quickshell-api.md`](docs/quickshell-api.md).

## License

Regalia is licensed under the GNU General Public License, version 3. See
[`LICENSE`](LICENSE).
