#!/usr/bin/env bash

set -euo pipefail

payload_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source_root=${REGALIA_PAYLOAD_ROOT:-$payload_root/payload}
sing_box_binary=${REGALIA_SING_BOX_BINARY:-}
privilege_mode=${REGALIA_PRIVILEGE_MODE:-sudo}
target_uid=${REGALIA_TARGET_UID:-$(id -u)}

validate_payload()
{
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
}

install_system_files()
{
    [[ "$target_uid" =~ ^[0-9]+$ ]] || {
        echo "Invalid target UID." >&2
        exit 1
    }
    validate_payload
    systemctl stop "regalia-engine@$target_uid.service" 2>/dev/null || true
    install -Dm755 "$source_root/usr/bin/regalia" /usr/bin/regalia
    install -Dm755 "$source_root/usr/bin/regaliad" /usr/bin/regaliad
    install -Dm755 \
        "$source_root/usr/lib/regalia/regalia-engine" \
        /usr/lib/regalia/regalia-engine
    install -Dm755 "$sing_box_binary" /usr/lib/regalia/sing-box
    install -Dm644 \
        "$source_root/usr/lib/systemd/system/regalia-engine@.service" \
        /usr/lib/systemd/system/regalia-engine@.service
    install -Dm644 \
        "$source_root/usr/lib/systemd/user/regaliad.service" \
        /usr/lib/systemd/user/regaliad.service
    install -Dm644 \
        "$source_root/usr/share/polkit-1/rules.d/50-regalia-engine.rules" \
        /usr/share/polkit-1/rules.d/50-regalia-engine.rules
    systemctl daemon-reload
}

if [[ $EUID -eq 0 && ${REGALIA_ROOT_INSTALL:-0} == 1 ]]; then
    install_system_files
    exit 0
fi

[[ $EUID -ne 0 ]] || {
    echo "Run this installer as your desktop user, not as root." >&2
    exit 1
}
validate_payload

# Stop both halves before replacing their executables. The desired VPN state is
# kept in ~/.config/regalia/state.json and is restored after the daemon starts.
systemctl --user stop regaliad.service 2>/dev/null || true
case "$privilege_mode" in
    pkexec)
        pkexec /usr/bin/env \
            REGALIA_ROOT_INSTALL=1 \
            REGALIA_TARGET_UID="$target_uid" \
            REGALIA_PAYLOAD_ROOT="$source_root" \
            REGALIA_SING_BOX_BINARY="$sing_box_binary" \
            "$0"
        ;;
    sudo)
        sudo /usr/bin/env \
            REGALIA_ROOT_INSTALL=1 \
            REGALIA_TARGET_UID="$target_uid" \
            REGALIA_PAYLOAD_ROOT="$source_root" \
            REGALIA_SING_BOX_BINARY="$sing_box_binary" \
            "$0"
        ;;
    *)
        echo "Unsupported privilege mode: $privilege_mode" >&2
        exit 1
        ;;
esac

systemctl --user daemon-reload
systemctl --user enable --now regaliad.service
