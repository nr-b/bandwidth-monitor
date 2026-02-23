#!/bin/sh
set -e

if command -v systemctl >/dev/null 2>&1; then
    systemctl stop bandwidth-monitor 2>/dev/null || true
    # Only disable on removal, not on upgrade.
    # deb passes "upgrade" on upgrade; rpm passes "1" on upgrade, "0" on removal.
    if [ "$1" != "upgrade" ] && [ "$1" != "1" ]; then
        systemctl disable bandwidth-monitor 2>/dev/null || true
    fi
    systemctl daemon-reload
fi
