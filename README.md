# Regalia

Regalia is an independent, Linux-first VPN control service designed for
terminal use, automation, and desktop-shell integration.

The project consists of:

- `regaliad` — a per-user daemon with a versioned JSON API over a Unix socket;
- `regalia` — a numbered terminal interface and a non-interactive CLI;
- internal packages for subscriptions, server selection, application discovery,
  route profiles, persistent state, and sing-box configuration generation.

Regalia is implemented as a clean Go project and does not inherit another
project's source tree or Git history.

## Current capabilities

- create, refresh, list, and delete subscription profiles;
- parse common VMess, VLESS, Trojan, Shadowsocks, AnyTLS, Hysteria2, TUIC,
  SOCKS, HTTP, and JSON server formats;
- keep the previous valid server list when a subscription refresh fails;
- select a connection-ready server;
- discover installed Linux applications from `.desktop` files and resolve
  their executable paths;
- persist route profiles with `proxy` or `direct` application rules;
- generate a sing-box TUN configuration;
- expose the state through a private per-user Unix socket.

The engine lifecycle is not connected yet. Regalia can prepare and validate its
state and generated configuration, but it deliberately reports the VPN engine
as unavailable instead of claiming that a tunnel is active.

## Build and test

Regalia currently uses only the Go standard library.

```bash
go test ./...
go build -o bin/regaliad ./cmd/regaliad
go build -o bin/regalia ./cmd/regalia
```

Start the daemon:

```bash
go run ./cmd/regaliad
```

Open the terminal interface in another terminal:

```bash
go run ./cmd/regalia
```

Non-interactive examples:

```bash
go run ./cmd/regalia status
go run ./cmd/regalia apps
go run ./cmd/regalia profile add Main https://example.com/subscription
go run ./cmd/regalia profiles
go run ./cmd/regalia servers
```

By default, the API socket is
`$XDG_RUNTIME_DIR/regalia/regaliad.sock`, and persistent state is stored in
`$XDG_CONFIG_HOME/regalia/state.json` or `~/.config/regalia/state.json`.

The API and trust boundaries are described in
[`docs/architecture.md`](docs/architecture.md).

## License

Regalia is licensed under the GNU General Public License, version 3. See
[`LICENSE`](LICENSE).
