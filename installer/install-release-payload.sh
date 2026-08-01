#!/usr/bin/env bash

set -euo pipefail

payload_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source_root="$payload_root/payload"
sing_box_binary=${REGALIA_SING_BOX_BINARY:-}
uid=$(id -u)

[[ $EUID -ne 0 ]] || {
    echo "Run this installer as your desktop user, not as root." >&2
    exit 1
}
[[ -x "$source_root/usr/bin/regalia" &&
   -x "$source_root/usr/bin/regaliad" &&
   -x "$source_root/usr/lib/regalia/regalia-engine" ]] || {
    echo "Invalid Regalia release payload." >&2
    exit 1
}
[[ -n "$sing_box_binary" && -x "$sing_box_binary" ]] || {
    echo "The verified sing-box executable was not supplied." >&2
    exit 1
}

# Stop both halves before replacing their executables. The desired VPN state is
# kept in ~/.config/regalia/state.json and is restored after the daemon starts.
systemctl --user stop regaliad.service 2>/dev/null || true
sudo systemctl stop "regalia-engine@$uid.service" 2>/dev/null || true

sudo install -Dm755 "$source_root/usr/bin/regalia" /usr/bin/regalia
sudo install -Dm755 "$source_root/usr/bin/regaliad" /usr/bin/regaliad
sudo install -Dm755 \
    "$source_root/usr/lib/regalia/regalia-engine" \
    /usr/lib/regalia/regalia-engine
sudo install -Dm755 "$sing_box_binary" /usr/lib/regalia/sing-box
sudo install -Dm644 \
    "$source_root/usr/lib/systemd/system/regalia-engine@.service" \
    /usr/lib/systemd/system/regalia-engine@.service
sudo install -Dm644 \
    "$source_root/usr/lib/systemd/user/regaliad.service" \
    /usr/lib/systemd/user/regaliad.service
sudo install -Dm644 \
    "$source_root/usr/share/polkit-1/rules.d/50-regalia-engine.rules" \
    /usr/share/polkit-1/rules.d/50-regalia-engine.rules

sudo systemctl daemon-reload
systemctl --user daemon-reload
systemctl --user enable --now regaliad.service

