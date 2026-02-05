#!/bin/bash
set -e

echo "Cleaning up proxmox-scheduler..."
systemctl daemon-reload || true

echo "Proxmox Scheduler removed successfully."
