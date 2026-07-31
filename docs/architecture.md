# Regalia architecture

## Components

### `regaliad`

`regaliad` owns persistent VPN state and exposes API version 1 over a local Unix
socket. The socket directory is private to the current user, and the socket is
created with mode `0600`.

The daemon currently handles:

- subscription profile lifecycle and atomic refresh;
- server inventory and active-server selection;
- route profile persistence;
- application discovery and per-executable routing rules;
- validation and generation of a sing-box TUN configuration.

### `regalia`

`regalia` is both a numbered terminal interface and a scripting client. It does
not read or modify the state file directly; all operations go through the local
API.

### State

State schema version 1 is stored as JSON with mode `0600`. Updates are written
to a temporary file, synchronized, and atomically renamed over the previous
state file.

Subscription refresh is also logically atomic: a failed download or invalid
payload records the error but does not erase the previous valid servers.

## Local API

Transport: newline-delimited JSON over a per-user Unix socket.

Request:

```json
{"id":1,"method":"status","params":{}}
```

Response:

```json
{"id":1,"result":{"apiVersion":1}}
```

API version 1 currently exposes:

- `status`
- `apps.list`
- `profiles.list`
- `profiles.create`
- `profiles.delete`
- `profiles.refresh`
- `servers.list`
- `servers.select`
- `routes.list`
- `routes.create`
- `routes.delete`
- `routes.activate`
- `routes.app.set`
- `routes.app.remove`

## Routing model

Every route profile has a default outbound: `proxy` or `direct`. Application
exceptions use absolute executable paths resolved from installed `.desktop`
files. The generated sing-box configuration converts those rules to
`process_path` entries.

Only the local user can access the API and state. A future privileged engine
bridge will have a separate, narrow interface; the UI-facing daemon will not
silently acquire root privileges.

## Next implementation boundary

The next subsystem is the engine lifecycle:

1. validate the selected server and generated configuration;
2. start sing-box in TUN mode through a narrowly scoped system service;
3. expose real starting, connected, stopping, and failed states;
4. restore the requested connection after login;
5. keep the QuickShell-facing API independent from the engine implementation.
