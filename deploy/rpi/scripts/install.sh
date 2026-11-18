#!/usr/bin/env bash
set -euo pipefail
root="${BUILD_ROOT:?BUILD_ROOT is required}"
target_dir=$(realpath -e "${INSTALL_DIR:?INSTALL_DIR is required}")
service="${SERVICE:?SERVICE is required}"
health_url="${HEALTH_URL:?HEALTH_URL is required}"
target="$target_dir/prusa-connect"
candidate="$root/bin/prusa-connect-pcma"
[[ -f "$target" && ! -L "$target" && -x "$candidate" ]]
[[ "$(systemctl show "$service" -p WorkingDirectory --value)" == "$target_dir" ]] \
    || { echo 'Service working directory differs from INSTALL_DIR.' >&2; exit 1; }
systemctl show "$service" -p ExecStart --value | grep -F "path=$target ;" >/dev/null \
    || { echo 'Service executable differs from the installation target.' >&2; exit 1; }
if [[ "$EUID" -ne 0 && ! -O "$target" ]]; then
    echo 'Run as the owner of the installed binary or as root.' >&2
    exit 1
fi
service_action() {
    if [[ "$EUID" -eq 0 ]]; then systemctl "$@" "$service"; else sudo -n systemctl "$@" "$service"; fi
}
exec 9> "$target_dir/.pcma-install.lock"
flock -n 9 || { echo 'Another installation is already running.' >&2; exit 1; }
(cd "$root/bin" && sha256sum --check SHA256SUMS)
expected_version=$(cat "$root/bin/VERSION")
expected_hash=$(sha256sum "$candidate" | cut -d ' ' -f 1)
backup_dir="$target_dir/backups"
mkdir -p "$backup_dir"
chmod 0750 "$backup_dir"
backup="$backup_dir/prusa-connect.$(date -u +%Y%m%dT%H%M%SZ).$$"
cp -p -- "$target" "$backup"
was_active=0
if systemctl is-active --quiet "$service"; then was_active=1; fi
replaced=0
staged="$target_dir/.prusa-connect.new.$$"

restore_on_error() {
    local status=$1
    trap - ERR INT TERM
    if [[ "$replaced" -eq 1 ]]; then
        echo "Installation failed; restoring $backup" >&2
        service_action stop || true
        ln -- "$backup" "$staged"
        mv -f -- "$staged" "$target"
        if [[ "$was_active" -eq 1 ]]; then service_action start || true; fi
    elif [[ -f "$staged" ]]; then
        rm -f -- "$staged"
    fi
    exit "$status"
}
trap 'restore_on_error $?' ERR
trap 'restore_on_error 130' INT
trap 'restore_on_error 143' TERM
install -m 0755 -- "$candidate" "$staged"
if [[ "$EUID" -eq 0 ]]; then chown --reference="$target" "$staged"; fi
mv -f -- "$staged" "$target"
replaced=1
service_action restart
python3 "$root/scripts/health.py" "$health_url" "$expected_version"
pid=$(systemctl show "$service" -p MainPID --value)
running_hash=$(sha256sum "/proc/$pid/exe" | cut -d ' ' -f 1)
[[ "$running_hash" == "$expected_hash" ]]
trap - ERR INT TERM
printf 'Installed %s\nService: %s\nBackup: %s\nSHA-256: %s\n' "$expected_version" "$service" "$backup" "$running_hash"
