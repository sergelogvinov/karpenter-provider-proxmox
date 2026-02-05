#!/bin/bash
set -e

echo "Stopping proxmox-scheduler service..."

if systemctl is-active --quiet proxmox-scheduler; then
    systemctl stop proxmox-scheduler || true
    echo "Proxmox Scheduler service stopped."
fi

if systemctl is-enabled --quiet proxmox-scheduler; then
    systemctl disable proxmox-scheduler || true
    echo "Proxmox Scheduler service disabled."
fi
