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
    "apiVersion": 3,
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
| `apiVersion` | Current protocol version, presently `3` |
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
not part of API version 3 yet.

## Setup screens

Use the remaining methods to build the settings UI:

- subscriptions: `profiles.list`, `profiles.create`, `profiles.refresh`,
  `profiles.delete`;
- server picker: `servers.list`, `servers.select`;
- installed application picker: `apps.list`;
- route profiles: `routes.list`, `routes.create`, `routes.delete`,
  `routes.activate`, `routes.app.set`, `routes.app.remove`.

Responses never include imported subscription URLs, passwords, UUIDs, private
keys, or raw sing-box outbound objects.
