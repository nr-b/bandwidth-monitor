#!/bin/sh
set -e

# Create working directory for GeoIP databases
mkdir -p /var/lib/bandwidth-monitor
chmod 0755 /var/lib/bandwidth-monitor

# Reload systemd and handle service state
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload

    # On upgrade: re-enable (in case the old prerm/postrm disabled it) and restart
    # On fresh install: just print instructions
    if systemctl is-enabled bandwidth-monitor >/dev/null 2>&1; then
        systemctl restart bandwidth-monitor
    elif [ "$1" = "configure" ] && [ -n "$2" ]; then
        # deb upgrade: $1=configure, $2=old-version
        systemctl enable --now bandwidth-monitor
    elif [ "$1" = "1" ] || [ "$1" = "2" ]; then
        # rpm: $1=1 on install, $1=2 on upgrade
        if [ "$1" = "2" ]; then
            systemctl enable --now bandwidth-monitor
        fi
    fi
fi

echo ""
echo "bandwidth-monitor installed."
echo "  1. Edit /etc/bandwidth-monitor/env with your settings"
echo "  2. (Optional) Place GeoLite2-*.mmdb in /var/lib/bandwidth-monitor/"
echo "  3. systemctl enable --now bandwidth-monitor"
echo ""
