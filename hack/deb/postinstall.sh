#!/bin/bash
set -e

systemctl daemon-reload
systemctl enable proxmox-scheduler

if ! systemctl restart proxmox-scheduler; then
  echo "Warning: proxmox-scheduler failed to start. Check 'journalctl -u proxmox-scheduler' for details." >&2
fi

echo "Proxmox Scheduler installed successfully."
