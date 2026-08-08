#!/usr/bin/env bash
#
# install.sh — Reticora CMDB installer entry point.
#
# Asks which component should be installed and delegates to the matching
# interactive installer:
#   1) Central cloud          → install-cloud.sh
#   2) VM in customer network → install-vm.sh
#
# Both installers are "foolproof": they prompt for all important information,
# validate the input and can be re-run safely (idempotent).
#
# Usage:
#   ./install.sh                asks which component to install
#   ./install.sh --cloud        central cloud (add --non-interactive for CI)
#   ./install.sh --vm           collector VM   (add --docker for Docker mode)
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

print_usage() {
    cat <<'EOF'
install.sh — Reticora CMDB installer entry point.

Asks which component should be installed and delegates to the matching
interactive installer:
  1) Central cloud          → install-cloud.sh
  2) VM in customer network → install-vm.sh

Both installers are "foolproof": they prompt for all important information,
validate the input and can be re-run safely (idempotent).

Usage:
  ./install.sh                asks which component to install
  ./install.sh --cloud        central cloud (add --non-interactive for CI)
  ./install.sh --vm           collector VM   (add --docker for Docker mode)
EOF
}

MODE=""
declare -a PASSTHROUGH=()
for arg in "$@"; do
    case "$arg" in
        --cloud) MODE="cloud" ;;
        --vm|--collector) MODE="vm" ;;
        -h|--help)
            print_usage
            exit 0
            ;;
        *) PASSTHROUGH+=("$arg") ;;
    esac
done

if [ -z "$MODE" ]; then
    if [ -t 0 ]; then
        echo "Reticora CMDB — installation"
        echo
        echo "  1) Central cloud (server, frontend, database, identity)"
        echo "  2) VM in the customer network (discovery collector)"
        echo
        choice=""
        while true; do
            read -r -p "Which component should be installed? [1/2]: " choice || true
            case "$choice" in
                1) MODE="cloud"; break ;;
                2) MODE="vm"; break ;;
                *) echo "Please enter 1 or 2." ;;
            esac
        done
    else
        echo "Non-interactive invocation requires --cloud or --vm." >&2
        exit 2
    fi
fi

case "$MODE" in
    cloud) exec "$SCRIPT_DIR/install-cloud.sh" ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"} ;;
    vm)    exec "$SCRIPT_DIR/install-vm.sh" ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"} ;;
esac
