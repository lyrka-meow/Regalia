# Regalia architecture

## Components

### `regaliad`

`regaliad` owns persistent VPN state and exposes API version 2 over a local Unix
socket. The socket directory is private to the current user, and the socket is
created with mode `0600`.

The daemon currently handles:

- subscription profile lifecycle and atomic refresh;
- server inventory and active-server selection;
- route profile persistence;
- application discovery and per-executable routing rules;
- validation and generation of a sing-box TUN configuration;
- sing-box process lifecycle and failure reporting.

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
{"id":1,"result":{"apiVersion":2}}
```

API version 2 currently exposes:

- `status`
- `vpn.connect`
- `vpn.disconnect`
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

## Engine lifecycle

Before starting the engine, Regalia:

1. builds the configuration from the selected server and route profile;
2. atomically writes it with mode `0600`;
3. runs `sing-box check` with a timeout;
4. starts `sing-box run` and waits through a startup grace period;
5. records the process ID, start time, state, and diagnostic log tail.

Configuration-changing API operations are rejected while the engine is active,
so the persisted selection cannot silently diverge from the running tunnel.
Stopping first sends an interrupt for graceful cleanup and kills the process
only if it exceeds the stop timeout. The engine is also stopped when the daemon
exits normally.

## Next implementation boundary

The next subsystem is the restricted Arch service bridge:

1. run the engine as the desktop user with only the Linux capabilities needed
   for TUN and route management;
2. authorize only the matching local user to control their service instance;
3. restore the requested connection after login;
4. keep the QuickShell-facing API independent from privilege management.
