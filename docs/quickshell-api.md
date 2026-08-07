# QuickShell integration API

Regalia exposes newline-delimited JSON over the private Unix socket
`$XDG_RUNTIME_DIR/regalia/regaliad.sock`. A desktop shell should talk to this
socket rather than launching `systemctl` or sing-box itself.

## Persistent VPN toggle

Request:

```json
{"id":1,"method":"vpn.setEnabled","params":{"enabled":true}}
```

Response:

```json
{
  "id": 1,
  "result": {
    "apiVersion": 5,
    "enabled": true,
    "connected": true,
    "engine": "connected",
    "tun": true
  }
}
```

`enabled` is persistent user intent. `connected` is the actual current engine
state. The shell must not treat them as synonyms: for example, after login the
toggle can be enabled while the engine is still starting or while a restore
attempt has failed.

`vpn.connect` and `vpn.disconnect` remain supported aliases for enabling and
disabling. All three methods are idempotent.

## Status

Request:

```json
{"id":2,"method":"status"}
```

Important response fields:

| Field | Meaning |
| --- | --- |
| `apiVersion` | Current protocol version, presently `5` |
| `capabilities` | Features the client may expose |
| `enabled` | Persisted VPN toggle state |
| `connected` | Engine is currently connected |
| `engine` | `unavailable`, `stopped`, `starting`, `connected`, `stopping`, or `failed` |
| `engineAvailable` | Required engine files and unit are available |
| `engineError` | Current engine failure, when present |
| `restoreError` | Previous login restore failure, when present |
| `configuration` | `ready` or `incomplete` |
| `configurationError` | Reason configuration is incomplete |
| `activeServer` | Safe server summary without credentials |
| `activeRoute` | Active route profile summary |

The shell can poll `status` while the VPN page is visible. Event streaming is
not part of API version 5 yet.

## Setup screens

Use the remaining methods to build the settings UI:

- subscriptions: `profiles.list`, `profiles.create`, `profiles.refresh`,
  `profiles.delete`;
- server picker: `servers.list`, `servers.select`;
- installed application picker: `apps.list`;
- exact running-process picker: `apps.processes` (paths come directly from
  `/proc/PID/exe` and duplicate executable paths are grouped);
- route profiles: `routes.list`, `routes.create`, `routes.delete`,
  `routes.activate`, `routes.app.set`, `routes.app.remove`.

Responses never include imported subscription URLs, passwords, UUIDs, private
keys, or raw sing-box outbound objects.

## On-demand connection test

API version 5 adds a bounded connection-quality test. It uses Cloudflare's
public speed-test endpoints and runs only after an explicit user action.
`direct` works without an active VPN. `proxy` and `compare` require the Regalia
engine to be connected. When connected, a private authenticated loopback proxy
routes the test explicitly through `direct` or `proxy`; the route of every
other application remains unchanged.

Start a test:

```json
{"id":10,"method":"network.test.start","params":{"mode":"compare","network":{"kind":"wifi","interface":"wlan0","name":"Home"}}}
```

Poll `network.test.status` with the returned test `id`, or cancel it with
`network.test.cancel`. Phases are `latency`, `download`, and `upload`. A result
reports Mbps, median HTTP latency, jitter, and HTTP request error rate for each
route. The error rate is intentionally not called packet loss because proxy
protocols do not provide a comparable ICMP measurement.

History methods are `network.test.history` and
`network.test.history.clear`. The private history file is capped at 20 results
and automatically removes entries older than 30 days. No response bodies,
proxy credentials, or verbose per-test logs are stored.
