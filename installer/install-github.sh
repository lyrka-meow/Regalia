#!/usr/bin/env bash

set -euo pipefail

repo=${REGALIA_GITHUB_REPO:-lyrka-meow/Regalia}
release_tag=${REGALIA_RELEASE_TAG:-rolling}
requested_mode=${REGALIA_INSTALL_MODE:-}
privilege_mode=${REGALIA_PRIVILEGE_MODE:-sudo}
sing_box_version=${REGALIA_SING_BOX_VERSION:-1.13.15}
install_mode=
arch=$(uname -m)

case "$arch" in
    x86_64)
        sing_box_arch=amd64
        sing_box_sha256=a3a3ff223b23c3f4731d0a17cb0ef94c97ce257c70721a5b07dc7ca079203c9f
        ;;
    aarch64)
        sing_box_arch=arm64
        sing_box_sha256=f0810bbb5722ae36635687c421019defcc8b328d31a0b3c287901f331747ca93
        ;;
    *)
        echo "Regalia currently supports Arch Linux x86_64 and aarch64 only." >&2
        exit 1
        ;;
esac
command -v pacman >/dev/null 2>&1 || {
    echo "The Regalia installer currently supports Arch Linux only." >&2
    exit 1
}
[[ $EUID -ne 0 ]] || {
    echo "Run the installer as your desktop user, not as root." >&2
    exit 1
}
[[ -n "$requested_mode" || -r /dev/tty ]] || {
    echo "The installer needs a terminal. Set REGALIA_INSTALL_MODE if running non-interactively." >&2
    exit 1
}

as_root()
{
    case "$privilege_mode" in
        pkexec) pkexec "$@" ;;
        sudo) sudo "$@" ;;
        *) echo "Unsupported privilege mode: $privilege_mode" >&2; return 1 ;;
    esac
}

if [[ -t 1 ]]; then
    red=$'\e[31m'
    green=$'\e[32m'
    blue=$'\e[34m'
    bold=$'\e[1m'
    dim=$'\e[2m'
    reset=$'\e[0m'
else
    red= green= blue= bold= dim= reset=
fi

state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/regalia"
mkdir -p "$state_dir"
find "$state_dir" -maxdepth 1 -type f -name 'install-*.log' -mtime +30 -delete 2>/dev/null || true
mapfile -t stale_install_logs < <(find "$state_dir" -maxdepth 1 -type f -name 'install-*.log' -printf '%T@ %p\n' 2>/dev/null | sort -rn | tail -n +6 | cut -d' ' -f2-)
if (( ${#stale_install_logs[@]} > 0 )); then
    rm -f -- "${stale_install_logs[@]}"
fi
log_file="$state_dir/install-$(date +%Y%m%d-%H%M%S).log"
tmp_dir=$(mktemp -d)
source_dir="$tmp_dir/source"
trap 'rm -rf -- "$tmp_dir"' EXIT

line()
{
    printf '%s\n' '────────────────────────────────────────────────────────'
}

banner()
{
    [[ -t 1 ]] && printf '\033[2J\033[H'
    printf '%s%sRegalia%s\n' "$bold" "$blue" "$reset"
    printf '%sVPN-сервис для терминала и MacqueenDE%s\n' "$dim" "$reset"
    line
}

read_choice()
{
    local value
    printf '%s' "$1" >/dev/tty
    IFS= read -r value </dev/tty
    printf '%s' "$value"
}

fail_step()
{
    local label=$1
    local rc=$2
    if [[ -t 1 ]]; then
        printf '\r\033[2K  %s✗%s %s\n' "$red" "$reset" "$label" >&2
    else
        printf '  ✗ %s\n' "$label" >&2
    fi
    printf '\nПоследние строки журнала:\n' >&2
    tail -n 30 "$log_file" >&2 || true
    printf '\nПолный журнал: %s\n' "$log_file" >&2
    exit "$rc"
}

run_step()
{
    local label=$1
    shift
    local frames=('⠋' '⠙' '⠹' '⠸' '⠼' '⠴' '⠦' '⠧' '⠇' '⠏')
    local frame=0 rc

    if [[ ! -t 1 ]]; then
        printf '  … %s\n' "$label"
        set +e
        "$@" >>"$log_file" 2>&1
        rc=$?
        set -e
        ((rc == 0)) || fail_step "$label" "$rc"
        printf '  ✓ %s\n' "$label"
        return
    fi

    printf '  %s%s%s %s' "$blue" "${frames[0]}" "$reset" "$label"
    "$@" >>"$log_file" 2>&1 &
    local pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        printf '\r\033[2K  %s%s%s %s' \
            "$blue" "${frames[frame]}" "$reset" "$label"
        frame=$(((frame + 1) % ${#frames[@]}))
        sleep 0.12
    done
    set +e
    wait "$pid"
    rc=$?
    set -e
    ((rc == 0)) || fail_step "$label" "$rc"
    printf '\r\033[2K  %s✓%s %s\n' "$green" "$reset" "$label"
}

choose_mode()
{
    local choice
    if [[ -n "$requested_mode" ]]; then
        case "$requested_mode" in
            source|binary)
                install_mode=$requested_mode
                return
                ;;
            *)
                echo "Invalid REGALIA_INSTALL_MODE: $requested_mode" >&2
                exit 1
                ;;
        esac
    fi

    printf '\n%sВыберите способ установки%s\n\n' "$bold" "$reset"
    printf '  %s1)%s Установить готовый бинарник %s(рекомендуется)%s\n' \
        "$bold" "$reset" "$green" "$reset"
    printf '     Быстро, без Go и компиляции.\n\n'
    printf '  %s2)%s Собрать из исходников\n' "$bold" "$reset"
    printf '     Свежий main; потребуется Go и немного времени.\n\n'
    printf '  %s0)%s Выход\n\n' "$bold" "$reset"
    while true; do
        choice=$(read_choice 'Ваш выбор: ')
        case "$choice" in
            1) install_mode=binary; return ;;
            2) install_mode=source; return ;;
            0) exit 0 ;;
            *) printf '%sВведите 1, 2 или 0.%s\n' "$red" "$reset" >/dev/tty ;;
        esac
    done
}

download_sing_box()
{
    local name url archive extracted
    name="sing-box-$sing_box_version-linux-$sing_box_arch.tar.gz"
    url="https://github.com/SagerNet/sing-box/releases/download/v$sing_box_version/$name"
    archive="$tmp_dir/$name"
    curl -fL --retry 3 --silent --show-error "$url" -o "$archive"
    printf '%s  %s\n' "$sing_box_sha256" "$archive" | sha256sum -c -
    tar -xzf "$archive" -C "$tmp_dir"
    extracted="$tmp_dir/sing-box-$sing_box_version-linux-$sing_box_arch/sing-box"
    [[ -x "$extracted" ]] || {
        echo "sing-box archive has an unexpected layout" >&2
        return 1
    }
    printf '%s\n' "$extracted" >"$tmp_dir/sing-box-path"
}

download_payload_installer()
{
    local url
    url="https://raw.githubusercontent.com/$repo/main/installer/install-release-payload.sh"
    curl -fsSL --retry 3 "$url" -o "$tmp_dir/install-release-payload.sh"
    chmod 0755 "$tmp_dir/install-release-payload.sh"
}

resolve_binary()
{
    local api asset_url asset_name suffix version
    api="https://api.github.com/repos/$repo/releases/tags/$release_tag?cache_bust=$(date +%s)"
    asset_url=$(curl -fsSL \
        -H 'Accept: application/vnd.github+json' \
        -H 'Cache-Control: no-cache' "$api" |
        sed -n 's/.*"browser_download_url":[[:space:]]*"\([^"]*regalia-[^"]*-'"$arch"'\.tar\.zst\)".*/\1/p' |
        head -n1)
    [[ -n "$asset_url" ]] || {
        echo "No compatible Regalia rolling binary was found." >&2
        return 1
    }
    asset_name=$(basename "$asset_url")
    suffix="-$arch.tar.zst"
    version=${asset_name#regalia-}
    version=${version%"$suffix"}
    [[ -n "$version" ]] || {
        echo "Invalid Regalia release asset name: $asset_name" >&2
        return 1
    }
    printf '%s\n%s\n' "$asset_url" "$version" >"$tmp_dir/binary-info"
}

install_binary()
{
    local asset_url version archive checksum sing_box
    resolve_binary
    {
        IFS= read -r asset_url
        IFS= read -r version
    } <"$tmp_dir/binary-info"
    archive="$tmp_dir/$(basename "$asset_url")"
    checksum="$archive.sha256"
    curl -fL --retry 3 --silent --show-error "$asset_url" -o "$archive"
    curl -fsSL --retry 3 "$asset_url.sha256" -o "$checksum"
    (
        cd "$tmp_dir"
        read -r expected _ <"$checksum"
        printf '%s  %s\n' "$expected" "$(basename "$archive")" |
            sha256sum -c -
    )
    mkdir -p "$tmp_dir/release"
    tar --zstd -xf "$archive" -C "$tmp_dir/release"
    sing_box=$(<"$tmp_dir/sing-box-path")
    REGALIA_PRIVILEGE_MODE="$privilege_mode" \
        REGALIA_PAYLOAD_ROOT="$tmp_dir/release/payload" \
        REGALIA_SING_BOX_BINARY="$sing_box" \
        "$tmp_dir/install-release-payload.sh"
    printf '%s\n' "$version" >"$tmp_dir/installed-version"
}

install_source()
{
    local sing_box commit archive
    git clone --depth 1 --branch main "https://github.com/$repo.git" "$source_dir"
    commit=$(git -C "$source_dir" rev-parse --short=12 HEAD)
    archive=$("$source_dir/packaging/github/build-binary-release.sh" \
        "source-$commit" "$arch")
    mkdir -p "$tmp_dir/release"
    tar --zstd -xf "$archive" -C "$tmp_dir/release"
    sing_box=$(<"$tmp_dir/sing-box-path")
    REGALIA_PRIVILEGE_MODE="$privilege_mode" \
        REGALIA_PAYLOAD_ROOT="$tmp_dir/release/payload" \
        REGALIA_SING_BOX_BINARY="$sing_box" \
        "$source_dir/installer/install-release-payload.sh"
    printf 'source-%s\n' "$commit" >"$tmp_dir/installed-version"
}

main()
{
    banner
    choose_mode

    printf '\nЖурнал установки: %s\n' "$log_file"
    printf 'Для системных файлов потребуется подтверждение прав администратора.\n\n'
    if [[ "$privilege_mode" == sudo ]]; then
        sudo -n true 2>/dev/null || sudo -v
    fi
    run_step 'Установка системных зависимостей' \
        as_root pacman -S --needed --noconfirm curl polkit
    if [[ "$install_mode" == source ]]; then
        run_step 'Установка инструментов сборки' \
            as_root pacman -S --needed --noconfirm git go
    fi
    run_step "Загрузка и проверка sing-box $sing_box_version" download_sing_box
    run_step 'Загрузка установочного компонента' download_payload_installer
    case "$install_mode" in
        binary) run_step 'Установка готовой Regalia' install_binary ;;
        source) run_step 'Сборка и установка Regalia' install_source ;;
    esac

    printf '\n'
    line
    printf '%s%sRegalia %s успешно установлена%s\n' \
        "$bold" "$green" "$(<"$tmp_dir/installed-version")" "$reset"
    printf 'Служба: %ssystemctl --user status regaliad%s\n' "$bold" "$reset"
    printf 'Терминальное меню: %sregalia%s\n' "$bold" "$reset"
    printf 'MacqueenDE обнаружит компонент при следующем открытии раздела VPN.\n'
    printf 'Журнал: %s\n' "$log_file"
    line
}

main "$@"
