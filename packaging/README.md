# Regalia packaging

The production layout keeps the desktop-facing daemon unprivileged and grants
only the engine service the capability needed to configure a TUN interface.

## Files

| Source | Install path | Mode |
| --- | --- | --- |
| `regalia` | `/usr/bin/regalia` | `0755`, root-owned |
| `regaliad` | `/usr/bin/regaliad` | `0755`, root-owned |
| `regalia-engine` | `/usr/lib/regalia/regalia-engine` | `0755`, root-owned |
| official sing-box executable | `/usr/lib/regalia/sing-box` | `0755`, root-owned |
| `systemd/regalia-engine@.service` | `/usr/lib/systemd/system/regalia-engine@.service` | `0644`, root-owned |
| `systemd/user/regaliad.service` | `/usr/lib/systemd/user/regaliad.service` | `0644`, root-owned |
| `polkit/50-regalia-engine.rules` | `/usr/share/polkit-1/rules.d/50-regalia-engine.rules` | `0644`, root-owned |

Do not apply `setcap` to `/usr/bin/regaliad`, `/usr/bin/regalia`, or a shared
system sing-box executable. The template unit supplies `CAP_NET_ADMIN` only to
the validated engine process. Regalia intentionally uses its own protected
sing-box copy so another package update cannot replace the executable inside
this trust boundary.

After installing or upgrading the units, run `systemctl daemon-reload` and
`systemctl --user daemon-reload`. No privileged engine instance is enabled
permanently: `regaliad` starts and stops the instance for the current UID
through systemd and the matching polkit rule.

Enable the unprivileged API daemon for the current desktop user with:

```bash
systemctl --user enable --now regaliad.service
```

When the user enables VPN, that desired state is persisted. The daemon restores
the connection when its user service starts after the next login. Disabling VPN
persists the opposite state, so it stays disconnected after restarting.

The engine service expects its private generated files at:

```text
/run/user/UID/regalia/engine.json
/run/user/UID/regalia/engine.log
```

Both files are created by `regaliad` with mode `0600`; the parent directory is
mode `0700`. The wrapper refuses configs and logs that do not preserve that
boundary.
