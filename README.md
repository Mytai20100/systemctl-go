# systemctl-go

![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![Build](https://img.shields.io/badge/build-passing-brightgreen)
![Platform](https://img.shields.io/badge/platform-linux-lightgrey)
![Architecture](https://img.shields.io/badge/arch-amd64%20%7C%20arm64%20%7C%20arm-blue)

A Go port of [gdraheim/docker-systemctl-replacement](https://github.com/gdraheim/docker-systemctl-replacement). Emulates `systemctl` and `journalctl` for containers and proot environments that lack a real init system.

## Install

```bash
bash build.sh
```

Cross-compile for ARM:

```bash
bash build-arm64.sh
```
or 

For Arm32:

```bash
bash build-arm32.sh
```
## Usage

```bash
systemctl start nginx.service
systemctl stop nginx.service
systemctl restart nginx.service
systemctl reload nginx.service
systemctl status nginx.service
systemctl enable nginx.service
systemctl disable nginx.service
systemctl is-active nginx.service
systemctl is-enabled nginx.service
systemctl list-units
systemctl list-unit-files
systemctl daemon-reload

# Isolated root (no impact on host)
systemctl --root /path/to/root start myapp.service
```

## Features

- **Service control** — start, stop, reload, restart, try-restart, reload-or-restart
- **Service types** — simple, forking, oneshot, notify, exec, idle, dbus
- **Unit management** — enable, disable, preset, preset-all, mask, unmask
- **Status & query** — status, is-active, is-failed, is-enabled, show, cat
- **Kill** — configurable KillSignal and KillMode, SIGTERM → SIGKILL fallback
- **Socket activation** — ListenStream, ListenDatagram, ListenSequentialPacket (Unix & TCP/UDP)
- **Notify socket** — `NOTIFY_SOCKET` / `sd_notify` protocol (`Type=notify`)
- **Journal** — per-unit log files, `journalctl -u`, follow mode (`-f`)
- **PID file** — read/write/wait for `PIDFile=` (`Type=forking`)
- **Environment** — `Environment=`, `EnvironmentFile=`, `ExecStart` prefix flags (`-`, `+`, `@`, `!`, `:`)
- **Service directories** — RuntimeDirectory, StateDirectory, CacheDirectory, LogsDirectory
- **Exec hooks** — ExecStartPre, ExecStartPost, ExecStop, ExecStopPost, ExecReload
- **Credentials** — `User=` / `Group=` via `SysProcAttr.Credential`
- **Drop-in files** — `.conf` overrides in `<unit>.d/` directories
- **Targets** — start/stop target units, active target tracking
- **Init mode** — `--init` flag runs as PID 1 replacement with socket-activation loop
- **Isolated root** — `--root` flag for container or proot usage
- **File locking** — `fcntl` waitlock prevents parallel operations on the same unit