# VM setup and control

The VM commands attach to existing hypervisors by default. Lifecycle and
snapshot operations are explicit and use fixed APIs; freeform QEMU monitor or
VirtualBox command strings are not accepted.

## QEMU

`internal/vm/qemu` attaches to a QMP Unix socket and supports status,
pause/resume, reset, powerdown, quit, keyboard/pointer input, and screenshots.
QMP snapshot save/load/delete are exposed as background jobs when QEMU 6.0 or
newer supports them. QMP does not provide a portable snapshot-list command in
this adapter, so `snapshot list` reports an explicit unsupported-capability
error rather than using HMP text parsing.

The snapshot job argument shapes follow the [QEMU QMP reference
manual](https://www.qemu.org/docs/master/interop/qemu-qmp-ref.html).

The managed launcher accepts a JSON `LaunchConfig` with an existing absolute
disk, optional existing firmware, new QMP socket and PID paths, machine and
architecture selection, bounded memory/CPU resources, an allowlisted
accelerator, NAT networking, and a bounded display choice. It emits explicit
argv for QEMU, never deletes the disk, and removes only control files owned by
the launched process. Launching is opt-in and does not start a VM during tests.

Example configuration:

```json
{
  "disk_path": "/srv/vms/dev.qcow2",
  "disk_format": "qcow2",
  "socket_path": "/run/user/1000/bifrost/dev-qmp.sock",
  "pid_path": "/run/user/1000/bifrost/dev-qemu.pid",
  "machine": "q35",
  "architecture": "x86_64",
  "accelerator": "kvm",
  "memory_mb": 4096,
  "cpus": 4,
  "network_nat": true,
  "display": "none"
}
```

The CLI launch form is `vm qemu launch CONFIG.json`. Existing VM lifecycle
commands use `vm qemu pause|resume|reset|powerdown|quit SOCKET`. Snapshot save
and restore require a QMP tag, VM-state block node, and optional device nodes.

## VirtualBox

The installed `VBoxManage 7.2` help was used to verify `startvm --type
headless`, `controlvm pause`, `resume`, `reset`, `acpipowerbutton`, and
`poweroff`, plus `snapshot list`, `take`, `restore`, and `delete`. The manager
also retains screenshot, keyboard text, and scan-code operations. Pointer input
is intentionally unsupported because the installed `controlvm` API has no
pointer command; a guest desktop worker or SDK-backed adapter is required.

CLI examples:

```text
vm virtualbox start VM (the CLI always selects headless)
vm virtualbox pause VM
vm virtualbox snapshot take VM NAME [DESCRIPTION]
vm virtualbox snapshot list VM
vm virtualbox snapshot restore VM NAME
vm virtualbox snapshot delete VM NAME
```

Tests use fake QMP sockets and a fixture `VBoxManage` executable. They do not
start, pause, reset, power off, or mutate a user VM. A disposable diskless
QEMU integration remains opt-in via `BIFROST_QEMU_INTEGRATION=1`.
