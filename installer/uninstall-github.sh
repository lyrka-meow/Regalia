#!/usr/bin/env bash

set -euo pipefail

privilege_mode=${REGALIA_PRIVILEGE_MODE:-sudo}
target_uid=${REGALIA_TARGET_UID:-$(id -u)}

remove_system_files()
{
    [[ "$target_uid" =~ ^[0-9]+$ ]] || {
        echo "Invalid target UID." >&2
        exit 1
    }
    systemctl stop "regalia-engine@$target_uid.service" 2>/dev/null || true
    rm -f -- \
        /usr/bin/regalia \
        /usr/bin/regaliad \
        /usr/lib/regalia/regalia-engine \
        /usr/lib/regalia/sing-box \
        /usr/lib/systemd/system/regalia-engine@.service \
        /usr/lib/systemd/user/regaliad.service \
        /usr/share/polkit-1/rules.d/50-regalia-engine.rules
    rmdir /usr/lib/regalia 2>/dev/null || true
    systemctl daemon-reload
}

if [[ $EUID -eq 0 && ${REGALIA_ROOT_UNINSTALL:-0} == 1 ]]; then
    remove_system_files
    exit 0
fi

[[ $EUID -ne 0 ]] || {
    echo "Run this uninstaller as your desktop user, not as root." >&2
    exit 1
}

systemctl --user disable --now regaliad.service 2>/dev/null || true
case "$privilege_mode" in
    pkexec)
        pkexec /usr/bin/env \
            REGALIA_ROOT_UNINSTALL=1 \
            REGALIA_TARGET_UID="$target_uid" \
            "$0"
        ;;
    sudo)
        sudo /usr/bin/env \
            REGALIA_ROOT_UNINSTALL=1 \
            REGALIA_TARGET_UID="$target_uid" \
            "$0"
        ;;
    *)
        echo "Unsupported privilege mode: $privilege_mode" >&2
        exit 1
        ;;
esac
systemctl --user daemon-reload
systemctl --user reset-failed 2>/dev/null || true

echo "Regalia was removed. Personal profiles remain in ~/.config/regalia."
