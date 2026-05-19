# Security Policy

## Supported Versions

This project is provided as-is for hobby/private use.

- **Latest `main` branch**: supported
- **Older commits / forks**: not guaranteed

If you rely on this for daily use, pin a known-good release/commit and keep a local backup of your `profiles.json`.

## Reporting a Vulnerability

If you discover a security issue, please **do not open a public GitHub issue**.

Instead, report it privately:

- Email: **kidpoleon@proton.me**

Include:

- A clear description of the issue
- Steps to reproduce
- Impact (what an attacker can do)
- Affected version/commit
- Any proof-of-concept details you can share safely

## Security Notes / Best Practices

### 1) Treat your profiles file as sensitive

Your profiles file may contain private portal credentials.

- Do not commit it to Git
- Keep it on trusted storage
- Use proper file permissions when possible

Environment variable:

- `STALKERHEK_PROFILES_FILE=/path/to/profiles.json`

### 2) Do not expose directly to the public internet

This app is intended for **private networks (LAN)**.

If you want remote access:

- Use a VPN (recommended)
- Or put it behind a reverse proxy with authentication

### 3) Reverse proxies and forwarded headers

If running behind Nginx/Cloudflare/etc:

- Ensure your proxy sets `X-Forwarded-Host` and `X-Forwarded-Proto`
- Only trust forwarded headers from your own proxy

### 4) Keep dependencies up to date

- Update Go periodically
- Run `go get -u ./...` and `go mod tidy` when upgrading

## Disclosure Policy

- We will acknowledge receipt of vulnerability reports when possible.
- Fixes will be made available in the repository.
