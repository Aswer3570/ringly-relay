# Ringly Relay

Volunteer relay for the **Ringly** messenger. Run it on any Linux server and help people in
countries that block messengers reach Ringly — for messages and, optionally, calls.

[Releases](https://github.com/ringlymessenger/ringly-relay/releases) · [Ringly for iOS](https://github.com/ringlymessenger/ringly-ios) · [ringlyapp.net](https://ringlyapp.net)

## What it does

| Module | What it does | Costs you | Default |
|---|---|---|---|
| **Message bridge** | a disguised entry point (VLESS + Reality on port 443) that the app tunnels its requests to the Ringly server through | almost nothing | on |
| **Call relay (TURN)** | relays voice and video when a direct connection is blocked | bandwidth | off |
| **Publishing** | `auto` — the Ringly server hands your relay out to users; `private` — you share a bridge link yourself | — | your choice |

## What a relay can and cannot see

- **Messages and calls are end-to-end encrypted on the devices.** The relay only ever forwards
  ciphertext and has no keys to read it.
- Your server sees the **IP addresses** of people connecting to it — like any server. The daemon does
  not write them to its log and does not send them anywhere.
- In `auto` mode the daemon reports to the Ringly server **only numbers**: that it is alive, how many
  bytes went through it and how many clients and calls are connected right now.
- The bridge only forwards traffic to Ringly servers (`*.ringlyapp.net`); any other destination is
  rejected, so your server can't be used as a general-purpose proxy.
- From the outside the bridge looks like an ordinary website with a real certificate of the cover
  domain you choose.

## Requirements

- A Linux VPS (x86-64 or ARM64). 1 vCPU and 512 MB RAM are plenty for the message bridge.
- Open ports:
  - **443/tcp** — message bridge;
  - for the call relay: **3478/udp + 3478/tcp**, **5349/tcp** (TURN over TLS) and the UDP range the
    system uses for outgoing ports (Linux default: 32768–60999).

If your provider has its own firewall or "security group", open the ports there too.

## Quick start (Docker)

You need `git` and Docker with the Compose plugin. sing-box is already inside the image.

```bash
git clone https://github.com/ringlymessenger/ringly-relay
cd ringly-relay/deploy/docker
docker compose run --rm relay setup             # answer the questions (Enter = default)
docker compose up -d --build                    # start
docker compose exec relay ringly-relay doctor   # check: everything should be ✓
```

The image is always **built locally from this repository** (`pull_policy: build`) — no prebuilt image
from a registry, so what runs is exactly the code you see.

**Private mode:** the bridge link is printed in the log on every start (`docker compose logs relay`)
or by `docker compose exec relay ringly-relay link`. Paste it in Ringly → Profile → Censorship bypass,
or share it with people you trust.

**Updating:**

```bash
cd ringly-relay && git pull && cd deploy/docker && docker compose up -d --build
```

Your configuration and the relay's identity (`deploy/docker/config/`) are not touched by updates.

## Without Docker (binary + systemd)

Download `ringly-relay-linux-amd64` or `-arm64` and `SHA256SUMS` from
[Releases](https://github.com/ringlymessenger/ringly-relay/releases) (or build them yourself, see
below), check the sum and install. You also need [sing-box](https://sing-box.sagernet.org) 1.12+ in
`PATH`; the systemd unit gives it the right to listen on port 443.

```bash
shasum -a 256 -c SHA256SUMS --ignore-missing
sudo install -m 755 ringly-relay-linux-amd64 /usr/local/bin/ringly-relay
sudo useradd -r -s /sbin/nologin ringly-relay
sudo ringly-relay setup                         # → /etc/ringly-relay/config.toml
sudo cp deploy/systemd/ringly-relay.service /etc/systemd/system/
sudo systemctl enable --now ringly-relay
sudo ringly-relay doctor
```

## Commands

```
ringly-relay setup     # interactive setup; run again to change settings
ringly-relay run       # run with the current configuration
ringly-relay doctor    # step-by-step check with hints
ringly-relay status    # what is configured
ringly-relay link      # bridge link for private mode (--host if the address wasn't detected)
ringly-relay --version
```

`-c/--config` sets the configuration path (default `/etc/ringly-relay/config.toml`).

## Troubleshooting

Start with `ringly-relay doctor` — it checks everything in order and says what to do.

| Symptom | What to do |
|---|---|
| `sing-box not found` | without Docker: install sing-box 1.12+ (the Docker image always has it) |
| `bind: permission denied` on 443 | Docker: rebuild (`docker compose up -d --build`); without Docker: use the systemd unit from `deploy/systemd` |
| `port 443 is already in use` | a web server is running; free the port or choose another one in `setup` |
| `cover domain: … is unreachable from this server` | choose another domain in `setup` — it must open from your server over TLS 1.3 |
| `test connection through the bridge` fails | the cover domain doesn't work with Reality (www.microsoft.com is known not to) — choose another one in `setup`, e.g. `dl.google.com` |
| `the Ringly server is unreachable from this VPS` | the server can't reach api.ringlyapp.net — check DNS and outgoing traffic |
| all ✓, but clients can't connect | the port is closed by the provider's firewall; `doctor` can't check that from the inside |

In `auto` mode the relay registers once and keeps its token in `state.json` next to the configuration
(under systemd — in `/var/lib/ringly-relay`). Don't delete it: the relay would register again as a new one.

## How it works

- **Message bridge** — VLESS + Reality via the reference [sing-box](https://sing-box.sagernet.org)
  binary, supervised by the daemon (`internal/singbox`, `internal/bridge`). Reality is not
  re-implemented here. Routing rules allow only Ringly hosts as destinations.
- **Call relay** — TURN on [pion/turn](https://github.com/pion/turn) (`internal/calls`): UDP, TCP and
  TURN over TLS. Short-lived credentials are issued by the Ringly server (coturn-compatible REST
  scheme); every relay has its own secret.
- **Publishing** (`internal/publisher`) — registration in the relay pool and a heartbeat once a
  minute. The shared registration secret is in the code on purpose: it filters accidental spam and is
  not a security boundary.
- **Accounting** (`internal/stats`) — byte counters and connection counts only; bridge traffic comes
  from sing-box's built-in statistics API, which listens on `127.0.0.1` with a random secret.

## Building from source

Go 1.27+:

```bash
make build    # bin/ringly-relay
make test
make vet
make dist     # dist/ringly-relay-linux-{amd64,arm64} + SHA256SUMS
```

`make dist` builds with `-trimpath`, CGO off and fixed flags, so the same tag built with the same Go
version gives the same binaries — compare your sums with the ones in a release
(`make dist VERSION=v0.1.0`; the Go version is noted in the release).

## Security

Found a vulnerability? Please report it privately — see [SECURITY.md](SECURITY.md).

## License

MIT — see [LICENSE](LICENSE). The relay does not use libsignal (it forwards ciphertext without
decrypting it), so the AGPL of the Ringly app does not apply here.

