# stalkerhek

[![Docker Pulls](https://img.shields.io/docker/pulls/kidpoleon/stalkerhek)](https://hub.docker.com/r/kidpoleon/stalkerhek)
[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![GitHub Stars](https://img.shields.io/github/stars/kidpoleon/stalkerhek?style=social)](https://github.com/kidpoleon/stalkerhek)

---

## Tutorial Video

<p align="center">
  <a href="https://www.youtube.com/watch?v=7AvkvlGfv64">
    <img src="https://i.ibb.co/b53gtx6G/STALKERHEK-BANNER-3840x2160.png" alt="Stalkerhek Tutorial" style="max-width: 100%; height: auto;">
  </a>
  <br>
  <em>Click the image above to watch the tutorial video and get started with stalkerhek</em>
</p>

---

## Screenshots

<div align="center">

| **Create Profile** | **Filter Management** |
| :---: | :---: |
| <img src="https://i.ibb.co/67YdPgTz/create-page.png" width="400"> | <img src="https://i.ibb.co/zTcQLk91/filter-page.png" width="400"> |
| **Profile Management** | **Tuning Settings** |
| <img src="https://i.ibb.co/zVNTtg2W/manage-page.png" width="400"> | <img src="https://i.ibb.co/8DVWCFJg/tuning-page.png" width="400"> |

</div>

---

Turn Stalker IPTV portal accounts into local streaming endpoints.

You get:
- Multiple profiles
- Web UI for management
- HLS playlist endpoint (works great in VLC / IPTV players)
- Stalker-style proxy endpoint (for clients that expect STB-ish behavior)
- Per-profile filtering (Categories -> Genres -> Channels)

## Table of contents
- [What this is](#what-this-is)
- [Quick start](#quick-start)
- [Docker](#docker)
- [Ports](#ports)
- [Web UI usage](#web-ui-usage)
- [Authentication](#authentication)
- [Optional portal parameters](#optional-portal-parameters)
- [Filters (per-profile)](#filters-per-profile)
- [Advanced settings (stability)](#advanced-settings-stability)
- [Persistence (where data is stored)](#persistence-where-data-is-stored)
- [Environment variables](#environment-variables)
- [Troubleshooting](#troubleshooting)
- [Security notes](#security-notes)
- [Changelog](#changelog)
- [Credits and license](#credits-and-license)

## What this is
`stalkerhek` is a single-binary Go application.

It authenticates to a Stalker portal (typically `portal.php` or `load.php`) using your profile credentials (MAC address, and internally MAG-style device identifiers), fetches your channel list, then exposes:
- An **HLS endpoint** your players can read
- A **Proxy endpoint** that mimics STB interactions for clients that need it

It also includes a Filters UI to safely enable/disable content per profile.

## Quick start

### 1) Run from source (Go 1.21+)
```bash
git clone https://github.com/kidpoleon/stalkerhek
cd stalkerhek
go run cmd/stalkerhek/main.go
```

Open:
- Web UI: `http://localhost:4400/dashboard`

### 2) Create a profile
In the Web UI:
- Click **Add Profile**
- Fill in:
  - **Portal URL**
  - **MAC address**
  - **HLS port** and **Proxy port** (must be unique per profile)
- Click **Save Profile**

The service will:
- Validate the portal URL + MAC format
- Authenticate
- Fetch channels
- Start HLS + Proxy for that profile

## Docker

### Docker run (host networking recommended)
```bash
mkdir -p ~/stalkerhek/data

docker run -d \
  --name stalkerhek \
  --network host \
  -v ~/stalkerhek/data:/data \
  -e STALKERHEK_PROFILES_FILE=/data/profiles.json \
  kidpoleon/stalkerhek:main
```

### Docker compose
Example `docker-compose.yml`:
```yaml
version: '3.8'
services:
  stalkerhek:
    image: kidpoleon/stalkerhek:main
    container_name: stalkerhek
    network_mode: host
    restart: unless-stopped
    environment:
      - STALKERHEK_PROFILES_FILE=/data/profiles.json
      - STALKERHEK_AUTH_FILE=/data/auth.json
      - STALKERHEK_FILTERS_FILE=/data/filters.json
    volumes:
      - ./data:/data
```

Start:
```bash
docker-compose up -d
```

Update:
```bash
docker-compose pull
docker-compose up -d
```

## Ports

- **Web UI**: `4400`
- **HLS**: per profile (example `4600`, `4601`, ...)
- **Proxy**: per profile (example `4800`, `4801`, ...)

If something does not start:
- You likely have a **port conflict**
- Or your firewall blocks the port

## Web UI usage

### Dashboard
`/dashboard` is the main page.

You can:
- Create/edit/delete profiles
- Start/Stop profiles
- Copy HLS/Proxy URLs
- Open logs (`/logs`) for troubleshooting

### Logs
Open `/logs` to see live logs.

If a profile fails to start, logs usually show:
- wrong portal URL
- wrong MAC address
- portal handshake/auth issues

## Authentication

Stalkerhek includes a complete authentication system to protect your WebUI, especially important for Docker deployments.

### First-Time Setup

When you first access Stalkerhek:
1. You'll be redirected to `/register` to create an admin account
2. Set a username and password (minimum 4 characters)
3. Optionally set a security question for password recovery
4. After registration, you'll be automatically logged in

### Authentication Features

- **Session-based login** with secure cookies
- **bcrypt password hashing** for security
- **7-day session persistence**
- **Password reset** via security questions
- **Local network bypass** for trusted subnets
- **Multi-user support** (when enabled)

### Account Management

Access `/account` to:
- **Password Tab**: Change your current password
- **Security Tab**: 
  - Toggle "Local Network Bypass" (skip auth on LAN)
  - View trusted network status
  - Check security question status
- **Users Tab** (when enabled): Add new user accounts

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `STALKERHEK_DISABLE_AUTH` | Set to `1` to disable authentication entirely | (unset) |
| `STALKERHEK_ALLOW_REGISTER` | Set to `1` to allow new registrations after first user | (unset) |
| `STALKERHEK_AUTH_FILE` | Path to store user data | `auth.json` (or adjacent to profiles) |
| `STALKERHEK_TRUSTED_SUBNETS` | Comma-separated list of trusted CIDR ranges | `127.0.0.0/8,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16` |

### Local Network Bypass

By default, Stalkerhek trusts these private networks:
- `127.0.0.0/8` (localhost)
- `10.0.0.0/8` (private Class A)
- `172.16.0.0/12` (private Class B)
- `192.168.0.0/16` (private Class C)

Devices on these networks can access the WebUI without logging in. You can:
- Toggle this in `/account` → Security
- Customize trusted subnets via `STALKERHEK_TRUSTED_SUBNETS`

### Password Reset

If you forget your password:
1. Go to `/forgot-password`
2. Enter your username
3. Answer your security question (if set during registration)
4. Set a new password

**Note:** Password reset only works if you set a security question during registration.

### Disabling Authentication

For completely private/trusted environments:
```bash
STALKERHEK_DISABLE_AUTH=1 ./stalkerhek
```

**Warning:** Only disable auth if Stalkerhek is behind another authentication layer (VPN, reverse proxy, etc.)

## Optional portal parameters

When creating a profile, you only need to provide:
- **Portal URL** (supports both `/portal.php` and `/load.php` endpoints)
- **MAC address** (format: `00:1A:79:AA:BB:CC`)
- **HLS port** and **Proxy port**

All other fields are **optional** and use safe defaults automatically:

| Field | Default Value | When to Override |
|-------|---------------|------------------|
| Username | (empty) | Only if provider requires login/password auth |
| Password | (empty) | Only if provider requires login/password auth |
| STB Model | `MAG254` | If provider expects a different model |
| Serial Number | `0000000000000` | If provider requires specific SN |
| Device ID | 64 `f`s | If provider requires specific device ID |
| Device ID 2 | 64 `f`s | If provider requires specific device ID 2 |
| Signature | 64 `f`s | If provider requires specific signature |
| Time Zone | `UTC` | Change if provider expects different timezone |
| Watchdog Interval | `5` minutes | Adjust based on provider requirements |

**Recommendation:** Leave all advanced fields empty unless your provider specifically requires custom values. The system will use proven defaults that work for most providers.

## Filters (per-profile)

Filters are designed for speed and safety.

Open from Dashboard:
- Click **Filters** on a profile

Flow:
1. **Categories** (derived grouping from portal genre names)
2. **Genres** (within a category)
3. **Channels** (within a genre)

You can:
- Bulk select and enable/disable
- Fine-tune individual channels

Keyboard shortcuts in Channels:
- Up/Down: move active row
- Enter: open details
- Space: toggle selection
- Esc: clear selection

Note: Filters UI is intentionally **desktop-focused**.

## Advanced settings (stability)

Defaults are applied on startup (no save required):

| Setting | Default | Purpose |
|---------|---------|---------|
| Playlist delay | 5 segments | Buffers live HLS slightly to reduce rebuffering |
| Upstream header timeout | 35s | Waits longer for slow IPTV origins |
| Max idle conns/host | 128 | Keeps warm connections for concurrent viewers |
| HLS link reuse | 180s | Avoids re-auth hiccups during playback |

In the Dashboard **Advanced Settings** you can override these process-wide values.

If you experience buffering:
- Increase Playlist delay first (try 8–12)
- Then increase Upstream header timeout (45–60)

The process also recycles every **24 hours** (Docker `restart: unless-stopped`); `profiles.json`, auth, and filters on disk are unchanged.

## Persistence (where data is stored)

Profiles and filters are persisted to JSON.

- `STALKERHEK_PROFILES_FILE` controls where `profiles.json` is stored.
- `filters.json` defaults to the **same directory** as `profiles.json` (e.g. `/data/filters.json` if profiles are `/data/profiles.json`).

For Docker:
- Mount a `/data` volume and set `STALKERHEK_PROFILES_FILE=/data/profiles.json`.

## Troubleshooting

### "Profile won't start"
Check these first:
- Portal URL must look like one of these:
  - `http(s)://HOST/portal.php`
  - `http(s)://HOST/load.php` (some providers use this instead)
  - `http(s)://HOST/stalker_portal/server/portal.php`
  - `http(s)://HOST/stalker_portal/server/load.php`
- MAC address must look like:
  - `00:1A:79:AA:BB:CC`
- Ports must be free

**If you see "invalid character '<' looking for beginning of value":**
This means the portal returned an HTML error page instead of JSON. Try:
1. If your URL ends with `/portal.php`, try changing it to `/load.php`
2. If your URL ends with `/load.php`, try changing it to `/portal.php`
3. Check with your provider which endpoint is correct

Open `/logs` and look for the first error.

### Web UI not reachable
- Confirm the process is running
- Confirm port `4400` is open
- Try `http://localhost:4400/dashboard`

### Playlist works but channels buffer
- Try increasing Playlist delay (segments)
- Increase Upstream header timeout
- If you have many devices, increase Max idle conns/host

## Security notes

- Do not expose this directly to the public internet.
- Use a private LAN, VPN, or reverse proxy with authentication.
- Profiles contain sensitive information (portal URL + MAC).

See `SECURITY.md` for details.

## Changelog

See [CHANGELOG.md](CHANGELOG.md) for a detailed history of changes and improvements.

## Credits and license

Origins/inspiration:
- https://github.com/erkexzcx/stalkerhek
- https://github.com/CrazeeGhost/stalkerhek
- https://github.com/rabilrbl/stalkerhek

Author:
- https://github.com/kidpoleon

License: MIT
