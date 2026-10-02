# lp10

> One command, one screen — a terminal player and equalizer for the **Arylic LP10**
> network audio streamer, driven over the device's own `:2018` control channel. No ssh, no login.

[![CI](https://github.com/lucasdaddiego/lp10/actions/workflows/ci.yml/badge.svg)](https://github.com/lucasdaddiego/lp10/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/lucasdaddiego/lp10)](https://goreportcard.com/report/github.com/lucasdaddiego/lp10)
[![Go Reference](https://pkg.go.dev/badge/github.com/lucasdaddiego/lp10.svg)](https://pkg.go.dev/github.com/lucasdaddiego/lp10)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
![go](https://img.shields.io/badge/go-1.27%2B-00ADD8)
![platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey)

```
$ lp10
┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃  ♪ LP10 · Living  ● 21:10                         Network  1 player  2 equalizer  3 diagnostics  ┃
┃  connected · firmware AR241CP_8747.29.2 · MCU 29 · Spotify eSDK 3.216.31                         ┃
┃                                                                                                  ┃
┃  nothing playing                                                                                 ┃
┃  start something on Spotify · AirPlay · Bluetooth                                                ┃
┃                                                                                                  ┃
┃  ⏸                                                                                               ┃
┃                                                                                                  ┃
┃   ◀◀   play   ▶▶                                                     vol ━━━━━━━●── 83%   mute   ┃
┃                                                                                                  ┃
┃                                                                                                  ┃
┃                   space play/pause · ↑↓ volume · m mute · s sleep · 1-3 views · ? help · q quit  ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
```

`lp10` turns the Arylic LP10 (a LibreWireless / LUCI network streamer) into a
live terminal dashboard — a player, an equalizer and a diagnostics read-out,
one view at a time — from a single Go executable. No companion app, no
browser, no background daemon, no password: run `lp10`, get one screen.

Since firmware `AR241CP_8747` (the vendor's 2026-09-30 OTA) the box has no
ssh, telnet or adb at all, so lp10 speaks only what the box answers on the
LAN: one plain TCP connection to its control tunnel (`:2018`, the MCU's Arylic
UART API) for the player and the equalizer, plus the device's own LSSDP and
Spotify ZeroConf answers for its identity.

## Features

- **Live now-playing** — the device pushes the title, artist and album over
  the tunnel each time the track changes, and the play state and the service
  when playback starts or stops. A run that starts mid-track says "playing on
  Spotify · the title shows when the track changes" until the next track.
  There is no cover art, seek bar, position or format line: the tunnel
  carries none of them, so the art panel is a procedural plasma motif (moving
  while playing, frozen when paused). The title and artist are clickable
  (OSC 8) and open a Spotify search.
- **Three views, one screen** — `1` player · `2` equalizer · `3` diagnostics,
  named in the header strip (full names, then short names, then bare numerals
  as the width shrinks); `tab` cycles them, `esc` returns to the player, `?` is
  a help page with every key. Playback keys work from every view, so a track
  can be paused from the diagnostics. The player's footer shows a second page
  of the rarer keys for four seconds in every sixteen.
- **A notice line** under the header, in every view — a volume step names the
  level, mute says so, the sleep timer reports its state, and a lost
  connection warns. On connect it greets with what the box says about itself:
  `connected · firmware AR241CP_8747.29.2 · MCU 29 · Spotify eSDK 3.216.31`
  (the LSSDP answer, the tunnel's `VER`, the Spotify engine's ZeroConf). Each
  notice fades after a couple of seconds; the row is always there, so nothing
  shifts.
- **An idle clock** — connected with nothing playing, the full player shows
  the time in large block digits and how to wake the box ("start something on
  Spotify · AirPlay · Bluetooth" — the three services the box always runs; the
  tunnel cannot say which others are switched on), so the terminal reads from
  across the room.
- **A real mute** — `m` sends the MCU's own mute: the level stays where it is,
  and unmute brings the sound back at that level.
- **The volume bridge** — on firmware 8747 the Spotify app's volume slider
  stopped changing what the room hears: the level reaches the device's
  register and the app's slider, but not the audible stage (TEARDOWN §8.1).
  The knob, the remote and a tunnel `VOL` still work, because they go through
  the MCU. So lp10 re-sends as a tunnel `VOL` the first volume reading of each
  connection and every later level the device reports that lp10 did not set
  itself: the room follows the Spotify slider again, about 0.1–0.2 s behind
  it. Only while lp10 runs; the real fix is the vendor's.
- **Equalizer** (`2` or `e`) — the EQ switch and its preset, treble / mid / bass
  tone, the deep-bass switch and level, balance, and the output cap (Max
  volume) as wide slider rows, each with a note on what it does on this box.
  Driven over the device's own control channel. Paints instantly from a cached
  snapshot on launch.
- **Diagnostics** (`3` or `i`) — a one-line **status band** — a color-coded
  health verdict (`healthy` / `warn` / `fault`) with its reasons, and the
  clock — over ruled sections, two columns on a wide terminal (a stacked
  read-out when narrow): the **audio** the tunnel reports (source, play state
  and title, volume and mute, Max volume, EQ), lp10's own **connection** to the
  box (the tunnel and the age of its last frame, the host, the LSSDP answer,
  the Spotify engine's ZeroConf answer — the last two readable even while the
  tunnel is down), the **device** (firmware from LSSDP, the MCU build from
  `VER`, the eSDK from ZeroConf, what moved since the last `lp10 sweep`, and
  the vendor's update verdict once `u` has asked), and a **hardware** reference
  (SoC, the DAC situation, the line-out / optical outputs).
- **Finds the device itself** — mDNS auto-discovery at startup locates the LP10 on
  the LAN by its `am=LP10` advertisement, so a changed DHCP lease never needs a
  config edit; when mDNS is quiet it falls back to the device's own **LSSDP**
  responder (an SSDP M-SEARCH on UDP 1800, answered by the LibreWireless stack
  itself), then to the configured host. Pure UDP, no dependency, no bound port.
  The same LSSDP probe runs while lp10 can't reach the tunnel, so the
  "connecting…" screen says whether the device is **up on the LAN but not
  answering on `:2018`** or not answering at all.
- **Spotify ZeroConf** — the running Spotify engine advertises
  `_spotify-connect._tcp` and answers an unauthenticated `getInfo` on the
  advertised port (the port is per engine — 9095 for the Pro one, 9096 for the
  legacy HiFi one — so it is taken from the SRV record every time, never
  remembered). It says whether the engine is *actually up*, on which eSDK
  build, and — when the engine reports it — who is signed in; "not advertised"
  means no engine is running.
- **Sleep timer** — `s` arms a "pause in N minutes" countdown (15 → 30 → 45 →
  60 → 90 min, one step per press; `S` cancels), shown beside the clock. It lives
  entirely in lp10: at the deadline it sends the device's play/pause toggle —
  only while the device says it is playing, so a timer can never resume the
  room. It ends with the process: quitting lp10 cancels it. A deadline that
  passes while the link is down fires when the link comes back and the device
  says again whether it plays — unless that is more than ten minutes late (an
  outage through the night), when lp10 cancels the timer with a notice.
- **`lp10 sweep`** — the "did it update?" command, with no login: one
  read-only pass over the box and the vendor. On the LAN: a TCP connect scan of
  every port (ssh 22, telnet 23 and adb 5037 / 5555 called out if they ever
  answer again), the tunnel's read-only getters on one connection (the MCU
  build, the preset list and the inputs are compared; the settings are
  printed; never a set or an action), the DLNA renderer's UPnP description,
  LSSDP and the engine's ZeroConf. From the vendor: the app index the box's
  loader fetches (`rakoit_app` version and md5), the manifest's verdict for the
  running build, and the newest bundle it serves — size, date, etag — or that
  it offers none, even to an old build. It prints a report and diffs it
  against the previous sweep, kept as a baseline in `~/.local/state/lp10/`;
  `--json` prints the baseline's shape, `--no-save` leaves the old one in place
  — as does an interrupted run. The baseline is merged fact by fact: what a
  sweep cannot read (a silent tunnel, a probe that timed out) keeps its last
  known value and the date it was read, so a hollow report never becomes the
  thing the next sweep compares with; the report names those older facts
  (`mcu, eq presets as of Sep 22 10:00`). The diff skips listeners on the Linux
  ephemeral ports (32768–60999), where the vendor app's second port moves on
  every restart, but keeps dmr's 49494. It exits non-zero when the port scan
  or the tunnel fails. The syslog's reconnect counts and the box's own
  firmware verdict needed ssh and are gone; the web UI's log download is the
  manual substitute ([TEARDOWN §15](docs/TEARDOWN.md) — the download embeds
  the box's secrets). This is the one lp10 command that asks the vendor on its
  own — by design, since that is the question it answers.
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
- **Light on both ends** — one TCP connection and a status query every two
  seconds on the box's side, a single executable on yours (see
  [How it works](#how-it-works)).

## Install

Requires **macOS or Linux** and a recent **Go** toolchain (1.27+). Nothing
else at runtime — no ssh client, no secret store, no password to set up.

```sh
# Build a stripped release binary into ~/.bin (make sure it's on your PATH).
make install

# Run — no arguments, just one screen. (`lp10 --version` prints the build.)
lp10
```

## Keys

The screen shows one **view** at a time — the **player**, the **equalizer**,
or the **diagnostics** — named in the header's view strip with the one on show
lit. `1`–`3` jump straight to a view, `tab` cycles them, `esc` returns to the
player, and `?` opens a help page listing everything below.

| Key | Action |
|-----|--------|
| `1` · `2` · `3` | player · equalizer · diagnostics |
| `tab` / `shift-tab` | next view |
| `esc` | back to the player |
| `?` | help page (a second `?` closes it) |
| `e` · `i` | also open the equalizer · diagnostics, and close them again |
| `q` / `Q` | quit — from a view, first back to the player |

**Player**

| Key | Action |
|-----|--------|
| `space` | play / pause |
| `n` / `p` | next / previous track |
| `↑` / `↓` · `+` / `-` | volume ± step (`=` / `_` also work); like `m`, waits — with a notice — until the device has reported its volume this run, so a step is never taken from the last run's cached level |
| `←` / `→` · `enter` | move the transport focus · press the focused button |
| `m` | mute / unmute in the device (the level stays where it is) |
| `s` / `S` | sleep timer: arm / step the countdown (15 · 30 · 45 · 60 · 90 min, then off) / cancel |

**Equalizer** — `↑` / `↓` select a control, `←` / `→` adjust it, `enter` flips
a switch or steps the preset. **Diagnostics** — `↑` / `↓` scroll and `←` / `→`
page when the read-out is taller than the terminal (the footer says how much is
off-screen; the help page scrolls the same way); `u` asks the vendor's manifest
whether the firmware is current — the one request that leaves the LAN, only on
that key; a verdict answers a repeat `u` for half an hour. When a `lp10 sweep`
baseline exists, the device section also says what moved since it — firmware,
MCU or Spotify eSDK — or that nothing did.

The playback keys (`space`, `n`, `p`, `m`, volume, the timer) work from every
view that does not use the key itself — in the equalizer the arrows and
`enter` are its own.

> On Spotify, the device's "previous" first restarts the current track (seen on
> its LUCI transport through firmware 8530); press `p` twice to skip back.

On macOS the keyboard's **media transport keys** (play/pause, next, previous —
the F7–F9 glyphs or their touch-bar equivalents) also work, from any app, while
lp10 is connected — see the media-keys bullet under [Features](#features) for
the Accessibility grant this needs.

The player adapts to the terminal size: the full **dashboard** (the framed art
motif beside the now-playing column and a vertical volume rail, or the idle
clock) at ≥ 25 rows / 70 cols, a **compact** frame (no art, inline volume, the
source in the header) below that, and a one-line **mini** view below 9 rows /
58 cols. The header's view strip shows the view names when they fit and bare
numerals when they do not.

There's no mouse support — lp10 is keyboard-only, so the terminal's native
text selection and scrolling stay untouched. There's also no seek/scrub and no
position — the device exposes no seek command, and the tunnel reports no
position or duration.

## Equalizer

The equalizer view (`2` or `e`) drives the device's tone and output as a stack
of wide rows — the **EQ** switch and the **Preset** it applies (Flat ·
Classical · Pop · Jazz · Rock · Vocal, named by the device), the **Treble / Mid
/ Bass** tone, the deep-bass **Sub bass** switch and its **Sub level**,
**Balance**, and **Max volume**, the output cap, kept last as it's rarely
touched. `↑` / `↓` select a row; `←` / `→` adjust it; `enter` flips a switch or
steps to the next preset. Under the rows, a short note explains the selected
control. On a short terminal the rows scroll, so the selected one is always on
screen.

> **How the two EQ rows relate:** the **tone sliders are always live**, EQ on or
> off. **EQ** only decides whether the selected **Preset** curve is applied on
> top — so "EQ on" with flat sliders still colours the sound (that's the preset),
> and "EQ off" with Treble +8 still adds treble (that's the tone stage). Both
> stages run inside the LP10's MCU, which is also its DAC.

These ride the same plain-text control connection as the player, TCP
**2018** (the channel the vendor app uses too). While it is down the
equalizer is read-only (`←` / `→` / `enter` are refused with a notice), and
the last-known values are restored instantly from cache on launch.

> **Heads-up:** a low **Max Volume** is what makes the Bluetooth remote and
> Spotify seem unable to turn the volume up (they hit the cap). Set it to 100
> for the full range.

## Diagnostics

Press `3` or `i` for a read-out of the device, the connection, and what the
box says about itself without a login. A one-line **status band** answers "is
the LP10 OK?" in a glance — a health verdict and the clock — over ruled
sections. The sections run **alphabetically**, flowing down the left column and
continuing down the right, with the split picked to balance the two heights (it
collapses to a single stacked column when narrow):

```
┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃  ♪ LP10 · Living  ● 21:12                                                      1 player  2 equalizer  3 diagnostics  ┃
┃                                                                                                                      ┃
┃  diagnostics   ● healthy                                                                                    ● 21:12  ┃
┃  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  ┃
┃  ─ audio ───────────────────────────────────────────────    ─ device ──────────────────────────────────────────────  ┃
┃    source    Network                                          firmware  AR241CP_8747.29.2                            ┃
┃    state     idle                                             mcu       29-1d316f0c-10                               ┃
┃    volume    83%                                              eSDK      3.216.31-g317ae1c7                           ┃
┃    max vol   100%                                             sweep     unchanged since Oct 1 21:05                  ┃
┃    eq        off                                              vendor    up to date · checked 6.2s ago                ┃
┃                                                                                                                      ┃
┃  ─ connection ──────────────────────────────────────────    ─ hardware ────────────────────────────────────────────  ┃
┃    tunnel    live · :2018 · last frame 0.6s ago               dac       MVSilicon BP10xx MCU · I2S in · tone/EQ/ba…  ┃
┃    host      192.168.1.13                                     line in   3.5 mm aux · ADC unidentified (WM8904 decl…  ┃
┃    lssdp     answered 12s ago · S · eth0                      line out  3.5 mm · 1 Vrms (no power amp)               ┃
┃    spotify   answered 11s ago · :9095                         optical   S/PDIF TOSLINK ≤ 24-bit/192 kHz              ┃
┃                                                               radio     dual-band 802.11ac · BT 5.0                  ┃
┃                                                               soc       Amlogic A113L · 2× Cortex-A35                ┃
┃                                                                                                                      ┃
┃  live · u asks the vendor about updates · esc player · ? help                             ● good   ● warn   ● fault  ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
```

The **health verdict** (`healthy` / `warn` / `fault`) is the worst of the live
signals — the age of the tunnel's last frame (a live box answers the status
query every two seconds; after six seconds of silence lp10 drops the link and
reconnects), the LSSDP answer and the Spotify engine's answer — color-coded
and word-paired so it still reads on a no-color terminal, and **naming its
reasons** (`● warn · tunnel quiet`) so an amber verdict never has to be hunted
down. Disconnected, the band says so instead.

Four sections, each answering one question, in the alphabetical order they
render: the **audio** the tunnel reports (source, play state and title, volume
and mute, the Max volume cap, the EQ and its preset), lp10's own **connection**
to the box (the tunnel, the target host, the LSSDP and ZeroConf answers —
readable even while the tunnel is down, which is exactly when you need them),
the **device** identity (the firmware as LSSDP names it, the MCU build from the
tunnel's `VER`, the Spotify eSDK from ZeroConf, what moved since the last
`lp10 sweep`, and the vendor's verdict once `u` has asked), and a **hardware**
reference (SoC, the DAC situation, the line-out / optical outputs — encoded
from a full teardown of the unit (`docs/TEARDOWN.md`), corrected by live
probes: the DAC is the front-panel MCU itself, an MVSilicon BP10xx fed over
I2S, which also runs every tone / preset / balance stage; the WM8904 the
firmware declares isn't on the bus). A section with nothing to report is
skipped. Volume and EQ settings live on the player and in the equalizer view;
the audio section only reports them.

The LSSDP and ZeroConf probes run once at startup (the connect greeting and the
update check need the firmware build), then only while this view is open —
every 30 s — and every few seconds while the tunnel is down. What the box
sends off the LAN on its own — the Spotify session, a 4-hourly OTA check
carrying its MAC and serial, a vendor log uploader that had never fired by the
2026-09-13 audit, and on 8747 a metrics uploader with no URL set — is audited
in [docs/TEARDOWN.md §10.4](docs/TEARDOWN.md). `esc`, `q` or `i` return to the
player; the playback keys work from here too.

## How it works

One plain TCP connection to the device's control tunnel (`:2018`) is the whole
transport — no ssh, no login, no helper process on the box. The tunnel relays
the MCU's Arylic UART API to the LAN as bare `CODE:VALUE;` frames, with no
framing and no auth (TEARDOWN §6.3):

- **Seed, poll, listen** — on connect lp10 asks for the status, every EQ
  control, the preset names and the MCU build, 150 ms apart; then it asks
  `STA;` every two seconds (source, mute, volume and play state in one frame)
  and applies what the device pushes on its own: the title, artist and album
  on a track change (plain UTF-8), the play state and the service, the
  Spotify app's volume changes. Nothing is pushed while a track plays, which
  is why there is no position.
- **A silent link is a dead link** — a connection that delivers no frame for
  six seconds is closed and redialled with backoff (250 ms, doubling to 3 s).
  That also covers a quirk seen live: the box sometimes accepts a connection
  and never serves it, while the next one answers at once. lp10 holds exactly
  one connection — the tunnel daemon serves its clients one at a time, and a
  burst of quick connects once left the next one hanging.
- **One allowlist** — everything lp10 sends passes `tunnel.Wire`: play/pause,
  next and previous (bare actions, sent only on a keypress — never as a
  query), volume and mute, the EQ controls, and read-only getters. Any other
  code is refused before it reaches the socket; the device also clamps every
  value and echoes what it applied. A key pressed while the link is down is
  delivered when it comes back if it is under four seconds old, and dropped
  visibly otherwise; a held volume key sends the newest level at most every
  150 ms; quitting still writes what was already queued.
- **The volume bridge** — the first volume reading of each connection, and
  any later level the device reports that lp10 did not set, goes back out as
  `VOL:n;` (see [Features](#features)). For a knob or remote change, already
  applied, the re-send changes nothing.
- **Probes that need no tunnel** — the LSSDP responder and the Spotify
  engine's ZeroConf endpoint, as above. The vendor's manifest is asked only on
  `u` (and by `lp10 sweep`).
- **Light on the laptop too** — the renderer is capped at 15 frames a second:
  bubbletea re-parses the whole frame on every flush, changed or not, and on a
  200×50 terminal the default 60 cost about 7 % of a core for a static view
  and about 17 % with the motif animating; 15 is a quarter of that, and more
  than the 10 Hz logic tick and the motif need.
- **Typed state boundary** — every frame is parsed once (`tunnel.ParseFrames`):
  unknown codes are dropped, numbers must parse, and text (a title, the
  source, the version, a preset name) is reduced to printable runes and
  clipped before it reaches the screen. The worker runtime owns the
  connection, shutdown coordination and snapshot persistence; the shared
  protocol state holds only the lock-protected player, EQ and liveness model
  the UI reads.
- **Firmware now** — `AR241CP_8747.29.2` / MCU v29 (the vendor's OTA of
  2026-09-30, a production build; TEARDOWN §14.5), re-swept 2026-10-01. It
  deleted ssh, telnet and adb, so lp10 became tunnel-only. The MCU's command
  table and preset list are unchanged from v23; the tunnel pushes the track,
  the play state and the Spotify app's volume; the Spotify app's own volume no
  longer reaches the audible stage (hence the bridge); the Pro engine runs
  eSDK 3.216.31.
- **Verified firmware** — `AR241CE_8530.23.2` / MCU v23 (the August 2026 OTA)
  and `AR241CE_9243.16.2` / MCU v16 before it, both with the ssh-era lp10
  (TEARDOWN §14.1–§14.4).

### Security & threat model

> **lp10 is built for a trusted home LAN, and only that.**

- **No login, no stored secret.** The box's control tunnel has no auth, so lp10
  holds no password, key or token, and nothing it reads needs one. The config
  file holds only a host, a label, a volume step, the discovery switch and a
  theme.
- **The tunnel is open to the whole LAN.** Anyone on the network can send the
  box what lp10 sends — and more: the same API has codes that start Wi-Fi
  setup or reboot the box. lp10 limits itself to its allowlist (transport,
  volume, mute, EQ and read-only getters) and never sends anything else.
- **The device is untrusted input.** Every device and LAN string — tunnel
  frames, mDNS names, LSSDP and ZeroConf answers, the vendor's replies — is
  control-stripped and bounded before it reaches the terminal, so a hostile
  answer cannot inject an escape sequence or widen the frame.
- **What leaves the LAN.** Only `u` in the diagnostics (one manifest request)
  and `lp10 sweep` (the manifest, the vendor's app index and one `HEAD` of the
  newest bundle). Nothing else lp10 does reaches past the LAN.

Do not expose the LP10 to the public internet, and don't run lp10 across an
untrusted network. There is no transport hardening to add: the tunnel is plain
TCP with no auth, by the vendor's design.

## Configuration (optional)

`~/.config/lp10/config.toml` (or `$XDG_CONFIG_HOME/lp10/config.toml`) — defaults shown:

```toml
host     = "lp10.local"   # fallback IP / mDNS name when discovery is off or finds nothing
name     = "LP10"         # UI label; discovery refines it to "LP10 · <device name>" (also the disambiguation hint)
vol_step = 2              # volume change per keypress (1–100)
discover = true           # find the LP10 on the LAN via mDNS (LSSDP as the fallback) at startup
theme    = "auto"         # auto | light | dark  (auto follows the terminal's background)
```

`user`, `ping_host`, `art` and `art_mode` are retired — they configured the
ssh login, the device's ping target and the album art, which went with
firmware 8747. A config that still sets one starts with a notice that names
it, and the value is ignored. Unknown keys and values of the wrong type are
reported the same way, so a typo never silently keeps the default.

`theme` picks the palette: `auto` (the default) asks the terminal for its
background colour once at startup and uses the light palette on a light
terminal, `light` and `dark` decide outright. `NO_COLOR` in the environment
turns every style off, as it does for any Charm program.

### Discovery

With `discover = true` (the default), lp10 sends a multicast-DNS query at startup
and connects to whichever LP10 answers — so a changed DHCP lease never needs a
config edit. It identifies the device by the `am=LP10` fingerprint the AirPlay
daemon advertises (`_raop._tcp`), reads its current IP, and uses it; the UI is
then labelled with the device's own advertised name (`LP10 · Living`), so nothing
is hardcoded. The query goes out **every** active interface, so a multi-homed Mac
(docked Ethernet, a VPN, or a Wi-Fi you just switched to) still finds a device on
a non-default interface. When mDNS is quiet, an LSSDP M-SEARCH gets one more
window. With more than one LP10, set `name` to the target's advertised name to
pick it (e.g. `name = "Living"`); otherwise the sole/first one is used. It is
pure UDP — no bound port, no dependency, ~tens of milliseconds when the device
is present, and it falls back to `host` if nothing answers, so startup never
blocks on a missing device. Set `discover = false` to pin `host` (an IP, or a
`.local` name your OS resolves).

`LP10_HOST` overrides `host` for a single run and skips discovery. Persistent
state (the volume and EQ snapshot used for instant first paint, and the `lp10
sweep` baseline `sweep-<host>.json`, whose `carried` map dates each fact kept
from an earlier sweep) lives under `~/.local/state/lp10/`, in files keyed on
the configured `host` (so a new DHCP lease found by discovery keeps them).

### Environment overrides

Beyond `LP10_HOST`, everything else is a test / development hook. Set-but-empty
switches off the probe it names for `LP10_LSSDP_HOST`, `LP10_ZC_ADDR` and
`LP10_OTA_URL`; for the others an empty value is the same as unset
(`LP10_TUNNEL_ADDR` empty still means the configured host's `:2018`):

| Variable | Effect |
|----------|--------|
| `LP10_STATE_DIR` | state directory instead of `~/.local/state/lp10/` |
| `LP10_TUNNEL_ADDR` | the `:2018` tunnel's `host:port` — the whole connection to the box (the suite points it at an in-process fake) |
| `LP10_LSSDP_HOST` | the UDP:1800 liveness probe's target (`host` or `host:port`) |
| `LP10_ZC_ADDR` | a fixed Spotify ZeroConf `host:port`, skipping mDNS |
| `LP10_OTA_URL` | the vendor's firmware manifest URL — set it empty to switch the on-demand check off (`u` then says the check is off) |
| `LP10_COVERDIR` · `LP10_DUMP_DIR` | `make cover` instrumentation · dump every layout the invariants test renders |

`LC_ALL` / `LC_CTYPE` / `LANG` pick the ASCII glyph set under a CJK locale.

## Development

```sh
make test     # go vet + the full suite, fully off-device
make ci       # exactly what CI runs (gofmt, vet, go fix -diff, staticcheck, govulncheck, -race), under go.mod's toolchain
make cover    # merged unit + integration coverage of the shipped packages -> coverage.out
make build    # ./lp10
make run      # launch the live TUI
make install  # a stripped release binary into ~/.bin
```

The suite never touches a real device: the tests point `LP10_TUNNEL_ADDR` at
an in-process fake of the `:2018` tunnel (`internal/testutil`), switch the
LSSDP, ZeroConf and manifest probes off (set-but-empty), and keep state and
config in temp dirs. The end-to-end tests run the real binary in a pty
against that fake — keys reach it as tunnel frames, a pushed track shows on
screen, the volume bridge re-sends the device's level, and quitting, Ctrl-C
and SIGTERM restore the terminal. CI runs the same checks on Linux and macOS
(the media-key tap is compiled, and so analysed, only on macOS).

## Project layout

```
main.go                 entry: config, discovery, `lp10 sweep`, TUI launch
internal/config/        config file (retired-key warnings), paths, typed snapshot persistence
internal/protocol/      the shared domain State, the typed Track, sanitising of every device string
internal/tunnel/        the :2018 protocol: player and EQ codes, the one allowlist (Wire), frame parsing
internal/discovery/     mDNS discovery, the LSSDP (UDP:1800) probe and fallback, Spotify ZeroConf
internal/workers/       the tunnel worker (seed, poll, commands, volume bridge), the LSSDP / ZeroConf / OTA probes, persistence
internal/mediakey/      macOS media-key event tap (play/next/prev system-wide)
internal/atomicfile/    temp-sibling + fsync + rename writes for the persisted state
internal/tui/           Bubble Tea model, rendering, input dispatch, helpers
internal/sweep/         `lp10 sweep` — the read-only inventory, its baseline and diff
internal/testutil/      test helpers (env isolation, the binary builder, a fake :2018 tunnel)
internal/e2e/           end-to-end tests (argv contract, pty sessions against the fake tunnel)
docs/TEARDOWN.md        device teardown & technical reference (hardware, audio path, env store, LUCI/MsgBox, the :2018 tunnel, protocols, OTA, firmware history)
```

## Dependencies

- [`bubbletea/v2`](https://github.com/charmbracelet/bubbletea) / [`lipgloss/v2`](https://github.com/charmbracelet/lipgloss) / [`x/ansi`](https://github.com/charmbracelet/x) — terminal UI (x/ansi: style-preserving clipping)
- [`BurntSushi/toml`](https://github.com/BurntSushi/toml) — config
- [`golang.org/x/text`](https://pkg.go.dev/golang.org/x/text) — NFC normalisation of device strings (display width is `x/ansi`)
- [`creack/pty`](https://github.com/creack/pty) — pty end-to-end tests only

## License

MIT — see [LICENSE](LICENSE).
