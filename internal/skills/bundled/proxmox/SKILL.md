---
name: proxmox
description: "Manage Proxmox VE nodes, VMs/containers, backups, and networking via the Proxmox API"
always: false
tags: [infrastructure, virtualization, server-management]
requirements: [bin:pvesm, bin:qm, bin:pct, bin:pvesh]
---

# Proxmox VE Management

This skill covers administering Proxmox VE hosts through joshbot's
`shell` tool. All commands run via the Proxmox API through `pvesh`,
or the native CLIs (`qm` for VMs, `pct` for containers, `pvesm` for storage).

The Proxmox API user `joshbot-api@pve` is already configured with a token
(`joshbot-api@pve!tokens` / `joshbot-api`). Commands below use this user
implicitly when running locally on the node; for remote API access pass
`--host <node-ip>` and `--user joshbot-api@pve`.

## Node Status

Check cluster health and resource usage:

```bash
# Cluster summary
pvesh get /cluster/status

# Node list and resource usage
pvesh get /nodes

# Specific node status (replace <node> with actual hostname)
pvesh get /nodes/<node>/status

# Storage pool status
pvesh get /storage

# Summary view (quick glance)
pvesh get /nodes/<node>/summary
```

## Troubleshooting

```bash
# Check service status
pvesh get /nodes/<node>/services

# Restart a service (e.g., pvedaemon)
pvesh post /nodes/<node>/services/pvedaemon --action=restart

# Check task log for errors
pvesh get /nodes/<node>/tasks --errors 1

# System journal for a specific service
pvesh get /nodes/<node>/tasks --typefilter service
```

## Creating Resources (VMs and Containers)

### LXC Containers

```bash
# Create an LXC container
pct create <vmid> <ostemplate> \
  --hostname <name> \
  --password <password> \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp \
  --rootfs local-lvm:8G

# Start/stop a container
pct start <vmid>
pct stop <vmid>

# List all containers
pct list
```

### QEMU Virtual Machines

```bash
# Create a VM from a template
qm create <vmid> \
  --name <name> \
  --memory 2048 \
  --net0 virtio,bridge=vmbr0 \
  --scsihw virtio-scsi-pci \
  --serial0 socket

# Clone from a template (fast clone)
qm clone <template-vmid> <new-vmid> \
  --name <new-name> \
  --full 0

# Start/stop a VM
qm start <vmid>
qm stop <vmid>
```

### Template Creation

```bash
# Convert a VM to a template
qm template <vmid>

# List all templates
qm list | grep "template"
```

## Deleting Resources

```bash
# Stop and destroy a container
pct stop <vmid>
pct destroy <vmid>

# Stop and destroy a VM (removes all disks)
qm stop <vmid>
qm destroy <vmid> --purge

# Remove a template
qm destroy <template-vmid>
```

## Backups

### Creating Backups

```bash
# Create a backup of a VM/container
vzdump <vmid> \
  --compress gzip \
  --storage <storage> \
  --mode snapshot

# Scheduled backup via API (recurring job on the cluster)
pvesh create /cluster/backup \
  --schedule "0 2 * * *" \
  --mailto admin@example.com \
  --vmid <vmid> \
  --storage <storage> \
  --mode snapshot
```

### Listing and Restoring

```bash
# List all backups
pvesh get /nodes/<node>/storage/<storage>/content --content backup

# Restore a VM backup
qmrestore <storage>:backup/vzdump-qemu-<vmid>.tar.gz <vmid>

# Restore a container backup
pct restore <storage>:backup/vzdump-ct-<vmid>.tar.gz <vmid>
```

### Backup Management

```bash
# Remove old backups
pvesh get /nodes/<node>/storage/<storage>/content --content backup
pvesh delete /nodes/<node>/storage/<storage>/content/<volume>

# Check backup job status
pvesh get /cluster/backup
```

## Network Configuration

### Linux Bridge Setup

```bash
# Create a Linux bridge
pvesh create /nodes/<node>/network --iface vmbr0 \
  --type bridge \
  --address 192.168.1.1/24 \
  --autostart=true

# Add a VLAN-aware bridge
pvesh create /nodes/<node>/network --iface vmbr0 \
  --type bridge \
  --bridge_vlan_aware yes

# Apply network changes (PUT reloads the config)
pvesh put /nodes/<node>/network
```

### Firewall

```bash
# Enable firewall at node level
pvesh put /nodes/<node>/firewall/options --enable 1

# Create a firewall rule
pvesh create /nodes/<node>/firewall/rules \
  --type in \
  --action ACCEPT \
  --dport 22 \
  --proto tcp \
  --comment "SSH access"
```

### DNS and Hosts

```bash
# Set DNS for a node
pvesh put /nodes/<node>/dns \
  --dns1 8.8.8.8 \
  --dns2 1.1.1.1 \
  --search example.com

# Add a hosts entry (data is the full /etc/hosts content)
pvesh get /nodes/<node>/hosts
pvesh post /nodes/<node>/hosts \
  --data "192.168.1.10 host1.example.com host1"
```

## Best Practices

1. Always check node status before making changes
2. Use `pvesh get` first to inspect current state, then `pvesh create`/`put`/`delete` to modify
3. Use snapshot backups (`--mode snapshot`) for running VMs/containers
4. Test firewall rules on staging first — a wrong rule can lock you out
5. Keep at least one accessible console (VM ID 100 or container ID 100) for recovery
