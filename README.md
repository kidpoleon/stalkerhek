# stalkerhek

[![Go Version](https://img.shields.io/badge/Go-1.21-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![Repo](https://img.shields.io/badge/GitHub-kidpoleon%2Fstalkerhek-0f1410?logo=github)](https://github.com/kidpoleon/stalkerhek)
[![Go Reference](https://pkg.go.dev/badge/github.com/kidpoleon/stalkerhek.svg)](https://pkg.go.dev/github.com/kidpoleon/stalkerhek)
[![Go Report Card](https://goreportcard.com/badge/github.com/kidpoleon/stalkerhek)](https://goreportcard.com/report/github.com/kidpoleon/stalkerhek)

## Upstream / Origins

This project is based on and inspired by these original repositories:

- **erkexzcx/stalkerhek**
  - https://github.com/erkexzcx/stalkerhek/
- **CrazeeGhost/stalkerhek**
  - https://github.com/CrazeeGhost/stalkerhek/
- **rabilrbl/stalkerhek**
  - https://github.com/rabilrbl/stalkerhek

## General Description

`stalkerhek` is a lightweight Go application that turns one or more **Stalker IPTV portal** accounts (profiles) into local streaming endpoints on your home network.

You add your portal URL + MAC + ports in the WebUI, and the program:

- authenticates to the Stalker middleware
- pulls the channel list
- starts a per-profile **HLS playlist server** (easy to open in players like VLC)
- starts a per-profile **STB-style proxy** for portal-compatible clients

Everything is designed to be:

- **hands-off** (save profile -> it starts)
- **multi-profile** (profiles run in parallel)
- **LAN-friendly** (run it on one machine, then stream from other devices)

`stalkerhek` runs **multiple Stalker IPTV portal profiles in parallel** and exposes each profile as:

- an **HLS playlist server** (for IPTV players like VLC)
- an **STB/portal-compatible proxy** (for STB-style apps)

It includes a modern **dark green WebUI dashboard** for adding profiles and monitoring per-profile status.

> Intended for private home networks.

---

## Table of Contents

- [What you get](#what-you-get)
- [Quickstart (fast)](#quickstart-fast)
- [Step-by-step guide (from zero)](#step-by-step-guide-from-zero)
  - [1) Install Go](#1-install-go)
  - [2) Download this repo](#2-download-this-repo)
  - [3) Build](#3-build)
  - [4) Run](#4-run)
  - [5) Open the WebUI](#5-open-the-webui)
  - [6) Add a profile](#6-add-a-profile)
  - [7) Use the playlist / proxy](#7-use-the-playlist--proxy)
- [WebUI features](#webui-features)
- [Health / Metrics / Info endpoints](#health--metrics--info-endpoints)
- [Ports and networking](#ports-and-networking)
- [Graceful shutdown](#graceful-shutdown)
- [Troubleshooting](#troubleshooting)
- [Project structure](#project-structure)

---

## What you get

- **Multi-profile concurrency**
  - Every saved profile starts in parallel.
  - Saving a profile in the WebUI starts it immediately.

- **Per-profile service ports**
  - Each profile has its own:
    - HLS port
    - Proxy port

- **WebUI dashboard**
  - Add / verify / stop / delete profiles
  - Shows HLS and Proxy links
  - Dark-only theme with readable contrast and tooltips

---

## Quickstart (fast)

```bash
# 1) clone
git clone https://github.com/kidpoleon/stalkerhek
cd stalkerhek

# 2) run
go run cmd/stalkerhek/main.go
```

Open the dashboard:

- `http://localhost:4400/dashboard`

Add a profile, pick ports (example: `6600` / `6800`), save.

---

## Step-by-step guide (from zero)

### 1) Install Go

You need **Go 1.21+**.

Check your version:

```bash
go version
```

If you don’t have it, install from:

- https://go.dev/dl/

### 2) Download this repo

If you have Git:

```bash
git clone https://github.com/kidpoleon/stalkerhek
cd stalkerhek
```

If you don’t have Git:

- Download ZIP from GitHub
- Extract it
- Open a terminal in the extracted folder

### 3) Build

```bash
go build ./...
```

This compiles everything and confirms your environment is working.

### 4) Run

```bash
go run cmd/stalkerhek/main.go
```

You should see logs like:

- `Starting WebUI on :4400 ...`

### 5) Open the WebUI

On the same machine:

- `http://localhost:4400/dashboard`

From another device on your LAN (phone/TV box/another PC):

- `http://<YOUR_PC_LAN_IP>:4400/dashboard`

> Tip: Your LAN IP often looks like `192.168.1.xxx` or `10.0.0.xxx`.

### 6) Add a profile

In **Add a Profile**:

- **Portal URL**
  - Must point to `portal.php`
  - Example:
    - `http://example.com/portal.php`

- **MAC**
  - Example:
    - `00:1A:79:12:34:56`

- **HLS Port / Proxy Port**
  - Choose ports that are free and unique per profile
  - Example:
    - HLS `6600`
    - Proxy `6800`

Click **Save Profile**.

What happens next:

- The profile starts immediately:
  - authenticates to the portal
  - retrieves channel list
  - launches HLS + Proxy servers on your chosen ports

### 7) Use the playlist / proxy

#### A) HLS playlist (VLC / IPTV players)

Open the HLS URL shown in the dashboard, for example:

- `http://<YOUR_PC_LAN_IP>:6600/`

This should return an `.m3u` playlist.

In VLC:

- **Media** → **Open Network Stream**
- Paste the URL above

#### B) Proxy (STB-style apps)

Use the Proxy URL shown in the dashboard, for example:

- `http://<YOUR_PC_LAN_IP>:6800/`

The proxy is designed to satisfy STB portal-style clients and rewrite stream links appropriately.

---

## WebUI features

Dashboard:

- **Add profile**
  - Normalizes portal URLs
  - Validates MAC format
  - Starts services immediately on save

Per-profile actions:

- **Verify**
  - Confirms credentials and counts channels
- **Stop**
  - Stops the profile’s running servers
- **Delete**
  - Removes profile from `profiles.json`

---

## Health / Metrics / Info endpoints

These are built-in and lightweight.

- **Health (JSON)**
  - `http://<HOST>:4400/health`

- **Metrics (JSON)**
  - `http://<HOST>:4400/metrics`

- **Info (HTML, themed)**
  - `http://<HOST>:4400/info`

What you’ll see:

- Uptime
- Profile totals / running / error counts
- Runtime stats (goroutines, memory, GC)

---

## Ports and networking

- WebUI:
  - **`:4400`**

- Per-profile:
  - **HLS port**: you choose (example `6600`)
  - **Proxy port**: you choose (example `6800`)

Important rules:

- Do **not** reuse the same HLS/Proxy ports across profiles.
- If a port is already used by another app, the server can’t bind.
- If connecting from another device:
  - make sure firewall allows inbound connections on:
    - `4400`
    - your chosen HLS ports
    - your chosen Proxy ports

---

## Docker (Container) Guide

This repo includes a `Dockerfile`, `.dockerignore`, and `docker-compose.yml`.

Important concept:

- `stalkerhek` starts **dynamic per-profile ports** (whatever you set in the WebUI).
- In containers, you must ensure those ports are reachable.

### Option A (Recommended on Linux): Docker Compose with `network_mode: host`

On Linux, **host networking** is the cleanest way because you do **not** have to pre-map every HLS/Proxy port.

1) Install Docker + Compose

- Docker Engine: https://docs.docker.com/engine/install/
- Compose plugin is included in most modern Docker installs.

2) Create/initialize `profiles.json`

The container persists profiles via a bind mount to `./profiles.json`.

Create an empty file first (so the bind mount works):

```bash
echo '[]' > profiles.json
```

3) Build + run

```bash
docker compose up -d --build
```

4) Open the WebUI

- `http://localhost:4400/dashboard`

From other devices on your LAN:

- `http://<YOUR_PC_LAN_IP>:4400/dashboard`

5) Add profiles

Pick any free ports (example `6600` / `6800`). With host networking, those ports are directly opened on your host.

6) Stop / update

```bash
docker compose down
docker compose up -d --build
```

### Option B (Docker Desktop / non-Linux): Bridge networking with explicit port publishing

On Docker Desktop (Windows/macOS), `network_mode: host` is not supported in the same way.

You must:

- publish `4400` for the WebUI
- publish every HLS/Proxy port you plan to use

Example (edit `docker-compose.yml` using the commented template at the bottom):

```yaml
ports:
  - "4400:4400"  # WebUI
  - "6600:6600"  # HLS (example)
  - "6800:6800"  # Proxy (example)
```

Then run:

```bash
docker compose up -d --build
```

### Logs

```bash
docker compose logs -f
```

---

## Graceful shutdown

On Ctrl+C / SIGTERM:

- WebUI, HLS, and Proxy servers:
  - drain requests for a short period
  - then shut down with timeouts

This reduces the chance of corrupt or abruptly closed responses.

---

## Troubleshooting

### “Dashboard opens, but my HLS/Proxy port doesn’t respond”

Check:

- Did you choose a free port?
- Are you using the correct host?
  - From another device, use `http://<LAN_IP>:<PORT>/` not `localhost`.
- Firewall:
  - allow inbound TCP on chosen ports.

### “No profiles saved yet”

Profiles are stored in:

- `profiles.json`

If it is `[]`, you have no profiles yet.

### “Portal URL not working”

- Portal URL must end in `portal.php`.
- If you paste a different PHP endpoint, the UI tries to normalize it.

### “Stop doesn’t stop the profile”

- Confirm you pressed **Stop** for the correct profile.
- Check console logs for shutdown messages.

---

## Project structure

- `cmd/stalkerhek/main.go`
  - App entrypoint
  - Starts WebUI
  - Starts all saved profiles in parallel

- `webui/`
  - Dashboard + profile CRUD
  - `/health`, `/metrics`, `/info`

- `stalker/`
  - Portal authentication, channel discovery

- `hls/`
  - HLS playlist + streaming server

- `proxy/`
  - STB-style proxy + link rewrite

---

## Notes

- This project targets private/home usage.
- If you want external access, put it behind your own reverse proxy/VPN.
