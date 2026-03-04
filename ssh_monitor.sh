#!/bin/bash
# SSH Traffic Monitor Script
# Monitors SSH traffic on port 22 using tcpdump
# Requires root/sudo privileges for packet capture

set -euo pipefail

INTERFACE="${INTERFACE:-any}"
PORT="${PORT:-22}"
DURATION="${DURATION:-0}"  # 0 means run indefinitely until Ctrl+C

echo "=== SSH Traffic Monitor ==="
echo "Interface: $INTERFACE"
echo "Port: $PORT"
echo "Duration: $(if [ "$DURATION" -eq 0 ]; then echo 'indefinite (Ctrl+C to stop)'; else echo "${DURATION}s"; fi)"
echo ""

# Check if running with sufficient privileges
if [ "$EUID" -ne 0 ] && ! sudo -n true 2>/dev/null; then
    echo "WARNING: This script requires root/sudo privileges for packet capture."
    echo "Please run with: sudo $0"
    exit 1
fi

# Build tcpdump command
TCPDUMP_CMD="tcpdump -i $INTERFACE -n -l port $PORT"

echo "Starting SSH traffic capture..."
echo "Press Ctrl+C to stop monitoring"
echo ""

if [ "$DURATION" -gt 0 ]; then
    timeout "$DURATION" $TCPDUMP_CMD 2>/dev/null || true
else
    $TCPDUMP_CMD
fi

echo ""
echo "SSH monitoring stopped."