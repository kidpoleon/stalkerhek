# Changelog

All notable changes to the Stalkerhek project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [2026-05-19] — EPG, VOD & Xtream for IPTV apps

### Added

- **Xtream Codes API** (TiviMate, IPTV Smarters, etc.)
  - `GET /player_api.php` and `GET /panel_api.php` on port **4400** (profile resolved by **MAC** username)
  - Standard playback paths: `/movie/`, `/series/`, `/live/` → portal `create_link` / `play/movie.php`
  - Password: `stalkerhek` (documented in README)
  - Live, VOD categories, movies, series, seasons/episodes, and short EPG actions

- **EPG improvements**
  - Programme times use profile **timezone** (`stalker/timezone.go`)
  - Auto-detect embedded XMLTV URL from portal `get_profile` (`stalker/portal_meta.go`)
  - XMLTV includes programmes by default (`?programs=1&limit=500`); cached ~10 minutes
  - HLS playlists point at full programme guide via `url-tvg`

- **VOD improvements** (aligned with [IPTV-MAC-STALKER-PLAYER-BY-MY-1](https://github.com/Cyogenus/IPTV-MAC-STALKER-PLAYER-BY-MY-1))
  - **0-based** Stalker pagination (`p=0` first page) and multi-page fetch per category
  - Separate **Movies** vs **Series** categories (`type=vod` / `type=series`)
  - Seasons and episodes via `get_ordered_list` (`movie_id`, `season_id`)
  - Seekable playback URLs with `play_token` and `type=movie|series`
  - Full VOD M3U uses Xtream-style `/movie/` and `/series/` links

- **WebUI**
  - `webui/http_request.go` — shared `requestSchemeHost`, `writeJSON`, proxy-aware URLs
  - `webui/xtream.go`, `webui/portal_meta_store.go`
  - Public routes for EPG, VOD, Xtream, and playback (no WebUI login required for IPTV clients)

### Changed

- Removed **EPG** and **VOD** copy-link buttons from Manage (live uses HLS; VOD/EPG use Xtream on port 4400)
- README — Xtream setup: server `http://<host>:4400`, MAC as username
- VOD API list endpoint uses **0-based** `page` query parameter

### Fixed

- VOD lists were empty when the first Stalker page was skipped (`p=1` instead of `p=0`)
- IPTV apps ignoring `direct_source`; playback now uses standard Xtream URL paths
- Build error: undefined `requestSchemeHost` after EPG/VOD refactor
- Safer nil/empty checks on VOD playlist generation and Xtream play redirects
- Stream base URL derivation for portals under `/c/` paths (e.g. `line.smootvone.vip/c/portal.php`)

### Security

- `.gitignore` / `.dockerignore` — reinforced exclusions for credentials, local JSON, and binaries

---

## [2025-05-19]

### Added

- **M3U8 / playlist quality** (inspired by community fixes from [zedstate](https://github.com/zedstate/stalkerhek))
  - `stalker/normalize.go` — superscript stripping, genre/title cleanup for external players (VLC, Dispatcharr, Plex, etc.)
  - `RawGenre()` on portal channels for faithful `group-title` output (no `strings.Title` mangling)
  - Blank `tvg-id` and `tvg-logo` in playlists (Stalker CMD and relative logos break importers)
  - Duplicate channel names deduplicated at playlist generation (first sorted entry wins)
  - Playlist sorted by `group-title` then natural channel name (`CH 2` before `CH 10`)
  - Shared `stalker.DeriveCategory()` for filters UI and playlist consistency

- **Stability defaults applied on startup** (no manual save required)
  - Playlist delay: **5** segments
  - Upstream header timeout: **35s**
  - Max idle connections per host: **128**
  - HLS upstream link reuse: **180s** (reduces mid-playback re-auth hiccups)
  - Media link reuse: **45s**

- **Operations**
  - Scheduled **24-hour service recycle** (`maintenance/`) — process exits cleanly; Docker `restart: unless-stopped` brings it back; `/data` config unchanged
  - Instance logs retained up to **~2 days** / 8000 lines
  - Tagged logging: `[SETTINGS]`, `[HLS]`, `[MAINTENANCE]`, `[PROFILE …]`
  - `/health` and `/healthz` endpoints registered for Docker health checks
  - Local dashboard banner at `/assets/banner.png` (`graphic/banner.png`)

- **Docker**
  - Image includes **ffmpeg** and **curl** (diagnostics / healthcheck)
  - `graphic/` copied into image; `STALKERHEK_ROOT=/app`
  - `docker-compose.yml` documents `STALKERHEK_RESTART_HOURS` and optional GPU (`/dev/dri`)

### Changed

- Filter API and channel cache use **natural sort**; categories sort with **Other** last
- `docker-compose.yml` — `STALKERHEK_AUTH_FILE`, `STALKERHEK_FILTERS_FILE`, restart env documented
- README — stability defaults table and 24h recycle note
- `.gitignore` / `.dockerignore` — exclude credentials, local data, and build artifacts (`.github/` workflows are **not** ignored)

### Fixed

- **HLS mutex handling** — `/iptv/` streams no longer unlock the channel mutex twice (could cause panics or flaky playback on segment requests)
- **HLS probe errors** — unlock channel mutex when upstream probe fails
- **Root vs `/iptv/` errors** — consistent `503 channel unavailable` instead of generic 500
- **M3U8 attribute escaping** — quotes/newlines in channel names no longer break `#EXTINF` lines
- **Health check** — fresh installs with no profiles return HTTP 200 (`no_profiles`) so Docker healthchecks pass
- **Runtime tuning** — settings now apply via `init()` on boot (previously only after saving in the UI)

### Security

- Reinforced git/docker ignore rules for `profiles.json`, `auth.json`, `filters.json`, `.env`, and `data/`

### Thanks

Special thanks to **[zedstate](https://github.com/zedstate)** for their pull request and M3U8 normalization work. Their changes were reviewed and integrated into this release (playlist metadata, genre handling, superscript cleanup, and Dispatcharr-friendly output) even though the PR was not merged as-is.

---

## [2025-05-20] — EPG & VOD

### Added

- **EPG (Electronic Program Guide)**
  - Portal EPG via Stalker `get_short_epg` / `get_epg_info` (`stalker/epg.go`)
  - XMLTV export: `http://<host>:4400/epg/<profileId>/xmltv.xml`
  - Optional **custom EPG URL** per profile (`.xml` or `.xml.gz`) in Quick Edit Advanced Settings — proxied and cached for IPTV players
  - Live HLS playlists include `url-tvg` / `x-tvg-url` and numeric `tvg-id` when the portal provides channel IDs (matches XMLTV)
  - API: `GET /api/epg/{id}/channel?title=...` for program listings

- **VOD (Video on Demand)**
  - Portal VOD categories and paginated lists (`stalker/vod.go`)
  - API: `GET /api/vod/{id}/categories`, `GET /api/vod/{id}/list?category=&page=`, `GET /api/vod/{id}/link?cmd=`
  - Sample VOD M3U: `http://<host>:4400/vod/<profileId>/playlist.m3u`
  - Play redirect: `/vod/<profileId>/play?cmd=...`

- **Reference implementation** — patterns from [Cyogenus/IPTV-MAC-STALKER-PLAYER-BY-MY-1](https://github.com/Cyogenus/IPTV-MAC-STALKER-PLAYER-BY-MY-1) and [zedstate/stalkerhek](https://github.com/zedstate/stalkerhek) EPG work

### Changed

- License updated to **GNU GPL v3**
- Contact: **kidpoleon@proton.me**
- Dashboard profile row: **EPG** and **VOD** copy-link buttons *(removed in 2026-05-19; use Xtream instead)*

### Fixed

- HLS channel mutex lifecycle (prevents rare deadlocks on failed upstream probe)
- M3U `tvg-id` uses portal channel id when available (improves EPG matching in TiviMate, Plex, Dispatcharr)
- Custom EPG fetch (`.xml.gz`) no longer double-decompresses when the server already expands gzip bodies

### Notes

- EPG program data depends on your provider; use a custom XMLTV URL if the portal guide is empty or slow.
- For full VOD in IPTV apps, use **Xtream** on port 4400 (see README and [2026-05-19] entry).

---

## [Unreleased] - 2026-02-21

### Added
- **WebUI Authentication System** - Complete login/register/logout functionality
  - Session-based authentication with secure cookies
  - bcrypt password hashing for security
  - 7-day session persistence
  - Optional authentication via `STALKERHEK_DISABLE_AUTH=1`
  
- **Security Questions & Password Reset**
  - 5 preset security questions for account recovery
  - Password reset flow via `/forgot-password` and `/reset-password`
  - Case-insensitive answer matching
  
- **Local Network Bypass**
  - Automatic authentication bypass for LAN connections
  - Trusted subnets: 127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16
  - Toggle control in `/account` Security tab
  - Configurable via `STALKERHEK_TRUSTED_SUBNETS` environment variable
  
- **Account Management Page** (`/account`)
  - Tabbed interface: Password, Security, Users
  - Change password functionality
  - Add new users (when registration enabled)
  - Local network bypass toggle
  - Security status display
  
- **Responsive Design**
  - Mobile-optimized auth pages
  - Flexible viewport scaling with `clamp()` CSS
  - Touch-friendly button sizes (min 44px)
  - Collapsible navigation on small screens
  
- **User Registration Flow**
  - Initial admin creation on first run
  - Optional user registration when `STALKERHEK_ALLOW_REGISTER=1`
  - Security question setup during registration
  - Password confirmation validation

### Changed
- **Color Scheme Harmonization**
  - Auth pages now match main WebUI dark green theme
  - Primary accent changed from blue (#5b8def) to green (#2d7a4e)
  - Consistent CSS variables across all pages
  - Dark gradient backgrounds (#0a0f0a to #0d1410)
  
- **Password Requirements**
  - Reduced minimum length from 8 to 4 characters
  - Better suited for home/Docker environments
  - Still uses bcrypt hashing for security
  
- **URL Handling**
  - Improved portal URL normalization
  - Preserves user-specified endpoints (/portal.php vs /load.php)
  - Better error messages for URL validation

### Fixed
- **Redirect Loop Issue**
  - Fixed infinite redirect when no users exist
  - Proper initial setup flow to `/register`
  - Graceful handling of first-time access
  
- **Authentication Flow**
  - Correct session validation
  - Proper cookie handling with HttpOnly and SameSite
  - Secure token generation

### Security
- bcrypt password hashing with default cost
- Secure session cookies (HttpOnly, SameSite=Strict)
- Automatic session expiration after 7 days
- Trusted subnet verification for LAN bypass
- Security question-based password recovery

### Technical
- Added `golang.org/x/crypto` dependency for bcrypt
- Persistent user storage in `auth.json`
- Thread-safe user and session management
- Environment-based configuration

---

## [Previous Versions]

### Early Development
- Initial Stalkerhek middleware implementation
- HLS and Proxy streaming support
- Basic WebUI with profile management
- Portal authentication with MAC-based device ID
- Optional portal parameters (Model, Serial, DeviceID, etc.)
- Channel and genre filtering
- Runtime tuning settings

---

## Planned Features

- [ ] Email-based password reset
- [ ] Two-factor authentication (TOTP)
- [ ] User roles and permissions
- [ ] Audit logging for account actions
- [ ] Session management (view/kill active sessions)
- [ ] Account lockout after failed attempts
- [ ] Password strength indicator
- [ ] Automatic backup of auth data

---

## Contributing

When adding changes to this changelog:
1. Add a dated section or entries under `[Unreleased]`
2. Categorize as Added, Changed, Deprecated, Removed, Fixed, or Security
3. Keep entries concise but descriptive
4. Reference issue numbers when applicable

---

**Note:** This changelog tracks significant user-facing changes. For detailed code-level changes, refer to the git commit history.
