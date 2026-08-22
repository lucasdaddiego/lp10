# lp10

> One command, one screen — a terminal player and equalizer for the **Arylic LP10**
> network audio streamer, driven over a single SSH connection.

[![CI](https://github.com/lucasdaddiego/lp10/actions/workflows/ci.yml/badge.svg)](https://github.com/lucasdaddiego/lp10/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/lucasdaddiego/lp10)](https://goreportcard.com/report/github.com/lucasdaddiego/lp10)
[![Go Reference](https://pkg.go.dev/badge/github.com/lucasdaddiego/lp10.svg)](https://pkg.go.dev/github.com/lucasdaddiego/lp10)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
![go](https://img.shields.io/badge/go-1.26%2B-00ADD8)
![platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey)

```
$ lp10
┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃  ♪ LP10 · Living  ● 15:42           Spotify · audio/ogg · 44.1 kHz    Vol    ┃
┃                                                                              ┃
┃                                                                              ┃
┃                                                                              ┃
┃                                                                              ┃
┃                                                                              ┃
┃  ╭──────────────────────────────╮                                      ▓     ┃
┃  │██████████████████████████████│                                      ▓     ┃
┃  │██████████████████████████████│                                      ▓     ┃
┃  │██████████████████████████████│                                      ▓     ┃
┃  │██████████████████████████████│  De Música Ligera                    ▓     ┃
┃  │██████████████████████████████│  Soda Stereo                         ▓     ┃
┃  │██████████████████████████████│  Canción Animal                      ▓     ┃
┃  │██████████████████████████████│                                      ▓     ┃
┃  │██████████████████████████████│  ● Spotify · audio/ogg · 44.1 k…     ▓     ┃
┃  │██████████████████████████████│                                      ▓     ┃
┃  │██████████████████████████████│  ▶ Playing 00:31 ●─────── -02:59     █     ┃
┃  │██████████████████████████████│                                      █     ┃
┃  │██████████████████████████████│     ◀◀        pause       ▶▶         █     ┃
┃  │██████████████████████████████│                                      █     ┃
┃  │██████████████████████████████│                                      █     ┃
┃  │██████████████████████████████│                                      █     ┃
┃  │██████████████████████████████│                                      █     ┃
┃  ╰──────────────────────────────╯                                     44%    ┃
┃                                                                              ┃
┃                                                                              ┃
┃                                                                              ┃
┃                                                                              ┃
┃  ─────────────────────────────── equalizer ────────────────────────────────  ┃
┃  EQ      ○ off                                                               ┃
┃  Preset  Flat · Classical · Pop · Jazz · Rock · Vocal                        ┃
┃  Treble  ────────────────────────────────────────●─────────────────────  +3  ┃
┃  Mid     ───────────────────────────────●──────────────────────────────   0  ┃
┃  Bass    ────────────────────────────────────────●─────────────────────  +3  ┃
┃  Sub     ● on                                                                ┃
┃  Lvl     ─────────●────────────────────────────────────────────────────  15  ┃
┃  Balance ───────────────────────────────●──────────────────────────────   0  ┃
┃  Max Vol ─────────────────────────────────────────────────────────────● 100  ┃
┃   space play · ↑↓ vol · m mute · s sleep · d night · e EQ · ? diag · q quit  ┃
```

`lp10` turns the Arylic LP10 (a LibreWireless / LUCI network streamer) into a
live terminal dashboard — now-playing, transport, a graphic equalizer, and a
diagnostics overlay — from a single Go executable. No companion
app, no browser, no background daemon: run `lp10`, get one screen.

## Features

- **Live now-playing** — title, artist · album, source / quality, a seek bar,
  and segmented transport buttons. The art panel shows the **real album cover**
  — true pixels via the Kitty graphics protocol on Ghostty / kitty, a 24-bit
  half-block raster on any other truecolor terminal, falling back to an animated
  plasma motif for radio / idle / lesser terminals. The title and artist are
  clickable (OSC 8) and link to Spotify.
- **Graphic equalizer** — the EQ switch and treble / mid / bass tone, a deep-bass
  switch and level, and the output cap (Max Volume) — driven over the device's
  own control channel. Paints instantly from a cached snapshot on launch.
- **Diagnostics overlay** (`?`) — a one-line **status band** — a color-coded
  health verdict (`healthy` / `warn` / `fault`) and the clock, nothing else —
  over two ruled columns on a wide terminal (a stacked read-out when
  narrow): device & firmware identity (down to the serial, MCU version, and BT
  address); lp10's own **connection** to the box (ssh stream freshness and the
  `:2018` control-tunnel state, and the LSSDP liveness answer — readable even
  while the device is down); the
  active network link (Wi-Fi or ethernet, with live throughput, error/drop
  counters as session deltas, the multiroom group state, Wi-Fi **SNR**, and
  round-trip latency — average, jitter, and a spike-flagging peak — to your laptop,
  the gateway, and the internet);
  the **audio chain** (ALSA playback state, the **buffer fill**, the DAC's
  *actual* rate/format/channels vs the source — catching resampling — and the
  **output level**: the ALSA softvol the firmware holds one step under the volume
  it reports, flagged in amber when the two drift apart, i.e. the room is quieter
  or louder than every display claims; a volume nudge resyncs it); and resource
  gauges (cpu + clock · memory · storage · process contention · temp · uptime). It also lists the
  device's **streaming capabilities** (AirPlay 2 · Bluetooth · DLNA · Spotify on,
  with Cast / Qobuz / Tidal / USB shown off when env-gated — read live from the box)
  and a **hardware reference** (SoC, the DAC situation, the line-out / optical outputs).
  The live metrics are gathered **only while the overlay is open**; any metric the
  hardware can't provide degrades to "—".
- **Finds the device itself** — mDNS auto-discovery at startup locates the LP10 on
  the LAN by its `am=LP10` advertisement, so a changed DHCP lease never needs a
  config edit; when mDNS is quiet it falls back to the device's own **LSSDP**
  responder (an SSDP M-SEARCH on UDP 1800, answered by the LibreWireless stack
  itself), then to the configured host. Pure UDP, no dependency, no bound port.
  The same LSSDP probe runs while lp10 can't reach the box over ssh, so the
  "connecting…" screen says whether the device is **up on the LAN but refusing
  ssh** (its sshd rate-limits rapid reconnects) or not answering at all — and
  the diagnostics overlay's connection section shows the last answer.
- **Sleep timer** — `s` arms a "pause in N minutes" countdown (15 → 30 → 45 →
  60 → 90 min, one step per press; `S` cancels), shown beside the clock. It lives
  entirely in lp10 — at the deadline it sends the same pause the space bar does —
  so it needs nothing from the device (whose own sleep timer is hidden on this
  firmware) and can't leave anything behind. It ends with the process: quitting
  lp10 cancels it.
- **Night mode** — `d` switches on the device's own multi-band **dynamic range
  compressor** (the Amlogic AED block's DRC, with the firmware's stock 3-band
  table): peaks reined in, quiet passages lifted, for late-night listening at low
  volume. It's the one audio effect on the box a host can actually switch (the
  EQ/DRC coefficient tables are read-only from userspace, and the "WM8904"
  mixer controls drive a chip that isn't on the bus), driven over the same ssh
  stream as playback and read back from the device so the `◐ night` badge
  beside the clock shows device truth. Session-scoped: quitting lp10 puts the
  compressor back to the state it found. **`b` is bedtime**: the sleep timer and
  night mode in one press — compress now, pause in N minutes, and put the
  compressor back when the timer goes off.
- **Keyboard-only, on purpose** — the mouse is never captured, so the terminal
  keeps its native text selection and scrolling; every control is a keystroke
  away (see [Keys](#keys)).
- **macOS media keys** — the keyboard's play/pause / next / previous transport
  keys drive the device **system-wide**, even when the terminal isn't focused.
  While lp10 is connected they're consumed (so Music/Spotify on the laptop don't
  also react); disconnected, they pass through untouched. Needs **Accessibility**
  permission granted to the terminal app (System Settings → Privacy & Security);
  without it lp10 quietly retries and arms the tap the moment it's granted.
  No-op on Linux.
- **Adapts to the terminal** — the full dashboard, a compact frame, or a
  one-line mini view, by size.
- **Light on both ends** — one ssh connection, a single executable, and an
  on-device shell loop trimmed to the minimum of work (see [How it works](#how-it-works)).

## Install

Requires **macOS or Linux**, a recent **Go** toolchain (1.26+), and **OpenSSH**
(already on macOS; `openssh-client` on Linux). On Linux you also need
`secret-tool` (`libsecret-tools`) plus a running keyring — see step 1. Nothing
else at runtime.

```sh
# 1. Store the device's root password in the OS secret store (once). Both forms
#    prompt interactively, so the password never lands in shell history or `ps`.

# macOS — the login Keychain (built in):
security add-generic-password -U -a root -s lp10 -w

# Linux — the Secret Service via libsecret (needs libsecret-tools + a running
# keyring daemon, e.g. GNOME Keyring / KWallet, in a desktop / D-Bus session):
secret-tool store --label=lp10 service lp10 account root

# 2. Build a stripped release binary into ~/.bin (make sure it's on your PATH).
make install

# 3. Run — no arguments, just one screen.
lp10
```

## Keys

The screen is a two-pane dashboard — the **player** (now-playing + transport)
and the **equalizer**. `tab` moves focus between them; the focused pane drives
the arrow keys.

| Key | Action |
|-----|--------|
| `space` | play / pause |
| `n` / `p` | next / previous track |
| `+` / `-` | volume ± step (`=` / `_` also work) |
| `↑` / `↓` | player: volume ± step · equalizer: pick a band |
| `←` / `→` | player: move button focus · equalizer: adjust the focused band |
| `enter` | player: press the focused button · equalizer: toggle an on/off band |
| `tab` / `shift-tab` | switch pane (player ↔ equalizer) |
| `esc` | equalizer pane: step focus back to the player |
| `e` | jump focus to the equalizer |
| `m` | mute (volume 0 ↔ restored level, persisted) |
| `t` | right-hand time: remaining ↔ total |
| `s` / `S` | sleep timer: arm / step the countdown (15 · 30 · 45 · 60 · 90 min, then off) / cancel |
| `d` | night mode: toggle the device's multi-band DRC (restored on quit) |
| `b` | bedtime: `s` and `d` in one — arm / step the sleep timer with night mode on; night mode is put back when the timer fires or is cancelled |
| `?` | diagnostics overlay (see below) |
| `q` | quit |

> On Spotify, `p` (previous) first restarts the current track — that's the
> device's own MID-40 `PREV` behaviour, not lp10's; press it twice to actually
> skip back.

On macOS the keyboard's **media transport keys** (play/pause, next, previous —
the F7–F9 glyphs or their touch-bar equivalents) also work, from any app, while
lp10 is connected — see the media-keys bullet under [Features](#features) for
the Accessibility grant this needs.

The view adapts to the terminal size: the full **dashboard** (now-playing with
album-motif art, a vertical volume slider, and the graphic equalizer) at ≥ 25
rows / 70 cols, a **compact** frame (no art, inline volume, one-line EQ summary)
below that, and a one-line **mini** view below 9 rows / 58 cols.

There's no mouse support — lp10 is keyboard-only, so the terminal's native
text selection and scrolling stay untouched. There's also no seek/scrub — the
device exposes no seek command.

## Equalizer

The equalizer pane (focus it with `e` or `tab`) drives the device's tone and
output as a stack of rows — the **EQ** switch and the **Preset** it applies
(Flat · Classical · Pop · Jazz · Rock · Vocal, named by the device), the
**Treble / Mid / Bass** tone, the deep-bass **Sub** switch and its **Lvl**,
**Balance**, and **Max Vol**, the output cap, kept last as it's rarely touched.
`↑` / `↓` pick a row; `←` / `→` adjust it; `enter` flips an on/off row or steps
to the next preset.

> **How the two EQ rows relate:** the **tone sliders are always live**, EQ on or
> off. **EQ** only decides whether the selected **Preset** curve is applied on
> top — so "EQ on" with flat sliders still colours the sound (that's the preset),
> and "EQ off" with Treble +8 still adds treble (that's the tone stage). Both
> stages run inside the LP10's MCU, which is also its DAC.

These ride a separate plain-text control connection to the device on TCP
**2018** (the same channel the vendor app uses), independent of the SSH player
stream — so a dead tunnel only greys out the equalizer, it never disturbs
playback, and the last-known values are restored instantly from cache on launch.

> **Heads-up:** a low **Max Volume** is what makes the Bluetooth remote and
> Spotify seem unable to turn the volume up (they hit the cap). Set it to 100
> for the full range.

## Diagnostics

Press `?` for a full read-out of the device, connection, and link health. A one-line
**status band** answers "is the LP10 OK?" in a glance — a health verdict beside the
title and the key live vitals, color-coded — over two boxless, ruled columns. The
sections run **alphabetically**, flowing down the left column and continuing down the
right, with the split picked to balance the two heights (it collapses to a single
stacked column when narrow):

```
┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃  diagnostics   ● healthy                                                                                    ● 16:40  ┃
┃  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  ┃
┃  ─ audio ───────────────────────────────────────────────    ─ latency ─────────────────────────────────────────────  ┃
┃    buffer    ━━━━━━━━━───  78% full                           gw        14 ms ±1.4  max 14                           ┃
┃    dac       44.1 kHz · S16_LE · 2ch ● live                   net       30 ms ±2.0  max 31                           ┃
┃    stream    audio/ogg · 44.1 kHz                             you      2.2 ms ±0.2  max 2.3                          ┃
┃                                                                                                                      ┃
┃  ─ connection ──────────────────────────────────────────    ─ network ─────────────────────────────────────────────  ┃
┃    host      root@192.168.1.13                                address   192.168.1.13 · gw 192.168.1.1                ┃
┃    ssh       rx 0.9s ago · 1 attempt                          dns       192.168.1.1                                  ┃
┃    tunnel    :2018 · live                                     errors    rx 0 · tx 0 · drop 0 · session               ┃
┃                                                               link      ethernet · 100 Mbit/s · full duplex          ┃
┃  ─ device ──────────────────────────────────────────────      mac       aa:bb:cc:dd:ee:ff                            ┃
┃    bt        aa:bb:cc:dd:ee:fe                                multiroom solo                                         ┃
┃    build     2025-12-24 · app 312                             traffic   rx 58 KB/s · tx 2 KB/s                       ┃
┃    firmware  AR241CE_9243.16.2                                                                                       ┃
┃    mcu       v16                                            ─ resources ───────────────────────────────────────────  ┃
┃    model     Arylic AR241CE · LS8                             cpu       ━━━─────────  22% 1m 0.44 · 1200 MHz         ┃
┃    name      Living                                           memory    ━━━━────────  37% 135/215 MB free            ┃
┃    os        Linux 5.15.137 · 2 cores                         storage   ━━──────────  17% 1228/7168 MB /lsync        ┃
┃    serial    RKARYLLP100000000000                             tasks     2 running · 237 total                        ┃
┃                                                               temp      ━━━━━━━─────  52 °C SoC                      ┃
┃  ─ hardware ────────────────────────────────────────────      uptime    3h 25m                                       ┃
┃    dac       MVSilicon BP10xx (the MCU) · I2S in · tone/EQ/balance on-chip                                           ┃
┃    line in   3.5 mm aux · ADC unidentified (WM8904 declare… ─ services ────────────────────────────────────────────  ┃
┃    line out  3.5 mm · 1 Vrms (no power amp)                   on  ● AirPlay 2 ● Bluetooth ● DLNA / UPnP ● Spotify    ┃
┃    optical   S/PDIF TOSLINK ≤ 24-bit/192 kHz                  off ○ Google Cast ○ Qobuz ○ Tidal ○ USB playback       ┃
┃    radio     dual-band 802.11ac · BT 5.0                      open ● telnet :23 ● adb :5555 ● web :80 ● control :2018┃
┃    soc       Amlogic A113L · 2× Cortex-A35                    env-gated · toggle in the Arylic app                   ┃
┃                                                                                                                      ┃
┃  live · any key returns to the dashboard                                                  ● good   ● warn   ● fault  ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
```

A single **status line** carries a one-glance **health verdict** (`healthy` / `warn` /
`fault`) — the worst of the live signals (cpu · memory · temp · `/lsync` · buffer · link
freshness), color-coded and word-paired so it still reads on a no-color terminal —
with the connection light + clock on the right. Nothing else rides up top: every
live number lives in its section below. (The audio buffer reads `idle` when
nothing's playing, and volume/EQ don't appear in the overlay at all — they're
settings, not diagnostics, and live on the dashboard and the equalizer pane.)

Eight sections, each answering one question, in the alphabetical order they render:
the **audio** chain (source stream in, DAC out, the ring buffer between), lp10's own
**connection** to the box (the ssh stream the records ride, the `:2018` control
tunnel, the target host — readable even while the device is down, which is exactly
when you need them), **device** identity (model, firmware, build — plus the name,
serial, Bluetooth MAC, and MCU version read from the device's own registers), a
**hardware** reference (SoC, the DAC situation, the line-out / optical outputs — encoded
from a full teardown of the unit, corrected by live probes: the DAC is the front-panel
MCU itself, an MVSilicon BP10xx fed over I2S, which also runs every tone / preset /
balance stage; the WM8904 the firmware declares isn't on the bus), **latency**, the **network** the box itself is on
(address, DNS, link, MAC, interface **error/drop counters** shown as session deltas —
so a degrading powerline link turns amber without boot-lifetime noise false-alarming —
and the **multiroom** group state), **resources** (cpu · memory · storage · tasks ·
temp · uptime), and the **services** it offers. The rows inside each section are
alphabetical by label too, so any reading is a lookup, never a hunt. A section with
nothing to report is skipped, and the column split re-balances around what's left.

The **services** matrix is read live from the device (a one-shot read at connect): a
`pidof` for the running daemons (Spotify / AirPlay / DLNA / Bluetooth), a `getenv`
for the marketed-but-disabled features (Cast / Tidal / Qobuz / USB), and a scan of
`/proc/net/tcp` for the **open** group — the unauthenticated listeners anyone on the
LAN can reach: **telnet :23** and **adb :5555** (a root shell, shown in the warn
colour) and the vendor's own web page :80 and control tunnel :2018 (by design, dim).
lp10 only reports them; closing telnet/adb is a device-side change. Capabilities the
LP10 doesn't actually offer — Roon / Alexa / Matter, LibreWireless firmware baggage
that's never on the spec sheet — are not shown; toggle the rest in the device's own
setup (the Arylic / 4STREAM app), not here.

The resource gauges and the network stats (throughput, Wi-Fi signal, and the three
ping round-trips) are collected on the device **only while this overlay is open** —
close it and the on-device loop drops back to the bare minimum. Each latency row
holds its **peak** over a rolling ~30s window, flagged amber once a genuine spike
lands, so an intermittent glitch (a powerline link dropping out, say) is visible
after the fact. The internet-ping target is the
`ping_host` config key (default `spotify.com`); after the first successful ping the
loop pins the name to its resolved IP, so a dying DNS resolver can't stall the
on-device loop mid-session. Any key returns to the dashboard.

## How it works

One direct `ssh` child is the whole transport — no ControlMaster, no expect. A
BusyBox-ash loop on the device streams framed snapshots:

- **Adaptive cadence** — cheap reads roughly once a second while playing,
  stretching to ~3 s when idle. The now-playing JSON is shipped only when it
  changes; the play position is resynced periodically while the UI extrapolates
  it locally between reads; the resource stats run **only while the diagnostics
  overlay is open**. The per-tick work is kept to the minimum of device-API
  reads — every other stat comes from `/proc` and `/sys` via shell builtins.
- **Whitelisted commands** — input to the device is a whitelist of
  `<mid> <data>` lines (transport, volume, and a stats-on/off toggle), never
  `eval`. Failed sends are held and delivered in order on reconnect; stale ones
  are dropped visibly.
- **Secret-store auth** — password-only via `SSH_ASKPASS`: the binary re-execs
  itself and answers ssh's prompt from the OS secret store (the macOS login
  Keychain, or the Secret Service via `secret-tool` on Linux).
- **Self-reaping** — the loop detects a dead session by read-timing and exits,
  so both ends are reaped no matter how the TUI died; the client reconnects with
  backoff.
- **Typed state boundary** — device JSON is coerced once into a whitelisted
  `Track` schema. The worker runtime owns child handles, shutdown coordination,
  and snapshot persistence; the shared protocol state contains only the
  lock-protected player, device, and liveness model consumed by the UI.

### Security & threat model

> **lp10 is built for a trusted home LAN, and only that.**

- **Host keys are deliberately not verified.** The LP10 regenerates its SSH host
  key on every boot from a ramfs, so pinning is pointless: lp10 runs ssh with
  `StrictHostKeyChecking=no` and `UserKnownHostsFile=/dev/null` (see
  `transport.SSHArgv`). This is the **one intentional security tradeoff** — a
  static analyzer (gosec / CodeQL) will flag it, by design — and it means lp10
  offers **no protection against a man-in-the-middle** on the path to the device.
  Only run it on a network you control.
- **The password never touches the repo.** It lives solely in the OS secret store
  (the macOS login Keychain or the Linux Secret Service) and is delivered to ssh
  through `SSH_ASKPASS`; it is not in the source, git history, config files, shell
  history, or `ps` output.
- **The device is trusted as root.** Commands are a fixed whitelist, never
  `eval`, but lp10 logs in as `root@LP10` — treat the device as you would any
  appliance you have root on.

Do not expose the LP10 to the public internet, and don't run lp10 across an
untrusted network. There is intentionally no transport hardening beyond SSH's
password auth.

## Configuration (optional)

`~/.config/lp10/config.toml` (or `$XDG_CONFIG_HOME/lp10/config.toml`) — defaults shown:

```toml
host      = "lp10.local"    # fallback IP / mDNS name when discovery is off or finds nothing
user      = "root"
name      = "LP10"          # UI label; discovery refines it to "LP10 · <device name>" (also the disambiguation hint)
vol_step  = 2               # volume change per keypress (1–100)
ping_host = "spotify.com"   # diagnostics: the device's internet-latency target
discover  = true            # find the LP10 on the LAN via mDNS at startup
art       = true            # show the real album cover (off => the plasma motif)
art_mode  = "auto"          # auto | kitty | halfblock | off  (see below)
```

### Album art

The art panel renders the track's `CoverArtUrl`, fetched once and cached under
`~/.local/state/lp10/art/` (so a re-seen cover needs no network and the last
cover paints instantly on the next launch). `art_mode` picks the renderer:

- `auto` *(default)* — **Kitty** true-pixel graphics on a terminal that
  advertises support (Ghostty, kitty), a **half-block** raster on any other
  truecolor terminal, the **plasma motif** otherwise.
- `kitty` — force the Kitty path even when it isn't auto-detected (e.g. WezTerm /
  Konsole, or kitty/Ghostty inside tmux where detection backs off). It only falls
  back if the image can't be encoded; on a terminal that genuinely can't
  composite, use `halfblock`.
- `halfblock` — always the 24-bit half-block raster (no graphics protocol).
- `off` — never fetch, cache, or draw art; keep the plasma motif.

> The Kitty path uses Unicode-placeholder graphics so it composes with the
> diff renderer. If your terminal claims Kitty support but the cover renders
> wrong, set `art_mode = "halfblock"`.

### Discovery

With `discover = true` (the default), lp10 sends a multicast-DNS query at startup
and connects to whichever LP10 answers — so a changed DHCP lease never needs a
config edit. It identifies the device by the `am=LP10` fingerprint the AirPlay
daemon advertises (`_raop._tcp`), reads its current IP, and uses it; the UI is
then labelled with the device's own advertised name (`LP10 · Living`), so nothing
is hardcoded. The query goes out **every** active interface, so a multi-homed Mac
(docked Ethernet, a VPN, or a Wi-Fi you just switched to) still finds a device on
a non-default interface. With more than one LP10, set `name` to the target's
advertised name to pick it (e.g. `name = "Living"`); otherwise the sole/first one
is used. It is pure mDNS — no bound port, no dependency, ~tens of milliseconds
when the device is present, and it falls back to `host` if nothing answers, so
startup never blocks on a missing device. Set `discover = false` to pin `host`
(an IP, or a `.local` name your OS resolves).

`LP10_HOST` overrides `host` for a single run and skips discovery. Persistent state (the pre-mute
level and the now-playing/EQ snapshot used for instant first paint) lives under
`~/.local/state/lp10/`.

## Development

```sh
make test     # go vet + the full suite, fully off-device
make run      # launch the live TUI
make generate # regenerate the embedded device loop after editing remote_loop.src.sh
```

The suite never touches a real device: `LP10_SSH` swaps in a fake ssh transport
(`cmd/fakessh`) selected by `LP10_FAKE_SCENARIO` (`normal`, `silent`, `dataless`,
`eof`, `garbage`, `authfail`, `keychain-locked`, `heal`), and `LP10_STATE_DIR`
isolates persistent state. The on-device shell loop is checked for validity
(`sh -n`) and its parsers are exercised against captured device output, so edits
to it fail in CI rather than silently on the device. The loop is authored as
readable shell in `internal/transport/remote_loop.src.sh` and minified into the
embedded `remote_loop.sh` by `go generate` (`make generate`); a stale embed fails
the suite.

## Project layout

```
main.go                 entry: askpass hot path, config/discovery, TUI launch
internal/config/        config file, paths, typed premute/snapshot persistence
internal/protocol/      LUCI framing, typed Track parsing, commands, domain State
internal/transport/     secret-store/askpass auth, ssh argv, the on-device loop
internal/transport/loopgen/  minifies remote_loop.src.sh into the embedded remote_loop.sh
internal/discovery/     one-shot mDNS query to find the LP10 on the LAN
internal/workers/       owned processes, persistence, stream / command / watchdog / EQ / art runtime
internal/tunnel/        the :2018 plain-text EQ/control protocol
internal/artwork/       album-cover fetch/cache + half-block & Kitty rasterizers
internal/mediakey/      macOS media-key event tap (play/next/prev system-wide)
internal/atomicfile/    temp-sibling + fsync + rename writes for the persisted state
internal/tui/           Bubble Tea model, rendering, input dispatch, helpers
internal/fixtures/      embedded wire-record fixtures (shared by tests + fake)
cmd/fakessh/            fake ssh transport for tests (substituted via LP10_SSH)
internal/testutil/      test helpers (env isolation, fake/binary builders)
internal/e2e/           end-to-end tests (argv contract, pty smoke)
```

## Dependencies

- [`bubbletea/v2`](https://github.com/charmbracelet/bubbletea) / [`lipgloss/v2`](https://github.com/charmbracelet/lipgloss) / [`x/ansi`](https://github.com/charmbracelet/x) / [`colorprofile`](https://github.com/charmbracelet/colorprofile) — terminal UI (x/ansi: style-preserving clipping; colorprofile: truecolor detection for the album-art gate)
- [`BurntSushi/toml`](https://github.com/BurntSushi/toml) — config
- [`golang.org/x/text`](https://pkg.go.dev/golang.org/x/text) — East-Asian display width
- [`creack/pty`](https://github.com/creack/pty) — pty smoke test only

## License

MIT — see [LICENSE](LICENSE).
