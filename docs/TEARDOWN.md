# Arylic LP10 — Device Teardown & Technical Reference

> **What it is:** Arylic **LP10** — officially an *"AirPlay 2 & Google Cast Music Streamer"*:
> a LibreWireless-based **network audio streamer / line-level streaming source** that adds
> Wi-Fi/streaming to existing audio gear. Ethernet / Wi-Fi / USB / Bluetooth in → **3.5 mm
> line-out + optical (TOSLINK) out**, plus a **3.5 mm line-in**. **No power amp** (no speaker
> terminals) and **no phono stage** (line-level aux input, not RIAA).
>
> **This unit, as of 2026-09-12:** SoC Amlogic **A113L "A1"** (`a1-a113l-ad403-spk`) · platform **LibreWireless LS8** ·
> serial `RKARYLLP10<redacted>` · firmware **`AR241CE_8530.23.2`** / MCU **v23** (the August-2026 OTA — the vendor has
> nothing newer) · vendor app `rakoit_app` **v32** · Spotify on the **Pro engine** (eSDK 3.211.130, ZeroConf `:9095`) ·
> wired `eth0` at **`<device-ip>`** / `<device>.local` (over a TL-WPA4220 powerline link).
>
> **How to read it.** Chapters 0–13 describe the device *as it is now*; where a fact changed with firmware, the old value
> follows in parentheses with its date. §14 is the dated history (June baseline → August OTA → September re-sweep), §15
> is how to re-verify any of it read-only, and the inspection log at the end records every pass and what it touched.
> Everything was verified live on this unit (process table, `/proc`, `/sys`, ALSA, `iw`, binary `strings`, mDNS, the LUCI
> bus, the `:2018` tunnel, on-device certs, the sqlite env store) and cross-checked against Arylic's published specs (§12)
> and the `lp10` controller source (Appendix B). **Nothing was written to the device** on the read-only passes; the one
> pass that wrote (2026-06-30, benign and reverted) is described in the log. Secrets (`SP_BLOB`, PSKs, passwords) were
> never read — key names only.
>
> **Revision log** — 2026-06-27 Spotify chapter · 06-28→29 full teardown (4 passes, `AR241CE_9243.16.2` / MCU 16) ·
> 06-30 control-plane writes (OLED via `-remote 42`) · 07-01 OTA endpoint probed, `mcu.bin` pulled and reversed, MsgBox
> table reversed · 08-22 `:2018` identified as the Arylic UART API (`EQS`/`EQE` corrected) · 09-02 the 8530 OTA
> re-analysed (bundle diff, MCU v23, `rakoit_app` v32) · **09-12 re-sweep: no newer OTA; env store decoded; Pro engine
> since 09-04.**

---

## 0. TL;DR

A dual-core Amlogic A1 (32-bit ARMv7, Buildroot/Linux 5.15, Android-derived IPC) running the
**LibreWireless LS8** stack. It speaks a huge range of audio protocols natively — **Spotify
Connect, AirPlay 2, Google Cast, Roon (RAAT), Tidal Connect, Qobuz Connect, Bluetooth 5.0
A2DP (sink + source), DLNA/UPnP, Airable/TuneIn radio, QPlay, Alexa/AVS, Matter** — each its
own daemon, gated by a persistent env flag. **Enabled on this unit: Spotify, AirPlay 2, DLNA,
Bluetooth** — the rest are installed but currently off (§8). Audio routes
through the Amlogic **"AUGE"** complex: a **WM8904 codec** (DAC → 3.5 mm line-out; its ADC
digitizes the line-in), an **S/PDIF** path to the optical out (up to **24-bit/192 kHz**),
hardware **EQ/DRC (AED)**, and **two HiFi4 DSP cores**. There is **no power amp** — the loaded
`tas5707` driver is unused generic-image baggage (confirmed by the I2C scan and the line-only
back panel). Control is the LibreWireless **LUCI/MsgBox** bus (what `~/.bin/lp10` drives) plus
a **GoAhead web server**; the front **0.91″ OLED** + **4 touch buttons** are run by an on-board
**MCU** (MVSilicon **BP10xx**, C-SKY core) over serial (the `LP10` text itself is silkscreen). The
host can drive **only the now-playing line** of the OLED — push MsgBox 42 (`RemoteUI`) via
`LUCI_local -remote 42`; a **full-screen custom message is not host-reachable**, confirmed by
reversing the MCU firmware (§6.4, §10.3). Three baked-in identities: a
LibreWireless device cert (LWT Root CA), a Google Cast cert (RAKOIT), and a Spotify OEM identity.

> **State** below = verified on **this unit** (env flag **and** running daemon); first read 2026-06-28/29, last re-read **2026-09-12**.
> Everything is installed in firmware; most services are env-gated and togglable via the app/web.

| Capability | Daemon / mechanism | Port(s) | State (this unit) |
|---|---|---|---|
| Spotify Connect | two engines, one runs: `spotifymusicpro` (Pro, eSDK 3.211.130) **now**; `newspotifyhifi` (HiFi, eSDK 3.203.239) until 2026-09-04 — §8.1 | per engine on 8530: 9096 (HiFi) · 9095 (Pro), 5353 | **ON** — **Pro engine** since 2026-09-04 (`SpotifyEnabled=0 / SpotifyProEnabled=1`, switched from `lp10`'s services pane, §14.3; HiFi ran 1/0 before) |
| AirPlay 2 | `airplaydemo` (Apple AccessorySDK, PTP) | 7000, 3721 (5000/WAC no longer bound since 8530) | **ON** (running) |
| DLNA / UPnP renderer | `dmr` | 49494, 1900 (SSDP) | **ON** (running) |
| Bluetooth 5.0 A2DP (sink + source) | `bluetoothd` + `bluealsa` (SBC/AAC) | — | **ON** (advertises as Loudspeaker) |
| Google Cast / Chromecast | `librecast` / `librecast_lite` | mDNS | **OFF** (`GoogleCast=false`, not running) |
| Roon (RAAT) | `libreraat` + `roon_monitor` | dynamic | **OFF** (`RoonEnable=0`; not marketed) |
| Tidal Connect | `tidalConnect` | dynamic | **OFF** (`TidalEnabled=0`, not running) |
| Qobuz Connect | `qobuzConnect` | 8000 | **OFF** (`QobuzConnectEnabled=0`) |
| Amazon Alexa (AVS) / Matter | env `AVSEnabled=0` / `MatterEnabled=0` | — | **OFF** |
| QPlay (QQ Music) | env `QPlay_*` | — | provisioned (off) |
| Airable / TuneIn radio + services | `UIframework` (`auth.airable.io`) | — | in-app browse (per-service login) |
| Line-in (3.5 mm) → stream/local | WM8904 ADC → `audionexus` | — | hardware-present |
| USB-A flash playback | `UIframework` + GStreamer + `automount` | — | **OFF** (`USBEnable=0`) |

---

## 1. Hardware

### Back-panel I/O (physical)

| Connector | Purpose |
|---|---|
| **RJ45 Ethernet** | Wired LAN (`eth0`), 10/100M — the primary network path here (powerline-fed) |
| **USB-A** | Host port — USB flash-drive playback (Arylic caps it at ~1000 songs / 16 GB; `USBEnable=0` at inspection) |
| **3.5 mm line-in** (TRS, 1 Vrms) | **Line-level aux** input (not phono) → WM8904 ADC |
| **3.5 mm line-out** (TRS, 1 Vrms) | Analog line output (WM8904 DAC) |
| **Optical out** (TOSLINK) | S/PDIF digital output, up to **24-bit/192 kHz** |
| **USB-C** | Power (5 V/2 A) + the RNDIS/ADB service gadget (§7); Arylic also lists it as **"PC audio"** (USB-audio function) |

No speaker terminals (no power amp) and no optical/coax *input* jack (the SoC supports
S/PDIF-in, but it isn't exposed). Ships with a 3.5 mm→2×RCA cable, so "RCA" in listings = via
that adapter.

### Internals

| Block | Detail |
|---|---|
| **SoC** | Amlogic **A113L "A1"** — DT compatible `a1-a113l-ad403-spk` |
| **CPU** | 2× **ARM Cortex-A35** (part `0xd04`, r0p2), **32-bit ARMv7** (`armv7l`, gnueabihf). DVFS 128 MHz–**1.2 GHz**, governor `schedutil`, ~768 MHz idle |
| **RAM** | **256 MB** DDR (~221 MB usable; rest reserved for DSP/CMA/secure) |
| **Flash** | **512 MB SPI-NAND** (`spinand`), MTD-partitioned (§3) |
| **Audio codec** | **Cirrus/Wolfson WM8904** (I2C `0x1a`, on TDM-B) — DAC → **3.5 mm line-out** (Line-Out + Headphone stages both on); its **ADC** (`IN2`, capture on) digitizes the **line-in**; on-chip EQ1-5 / DRC / HPF |
| **Spare ADC (unused)** | A **TI PCM1863** stereo ADC sits at I2C `0x4a` (`status=okay`), but its `pcm186x` driver is **unbound** and it's absent from the ASoC graph → **not used** (LS8 reference-design leftover) |
| **Power amp** | **None** — line-level device (`i2cdetect` finds no amp chip; `tas5707` module is unused baggage) |
| **DSP** | Amlogic **AED** HW block (5-band EQ, multi-band + full-band DRC, crossover, limiter, 2×2 mixer) **+ two Tensilica HiFi4 cores** (`/dev/hifi4dsp0/1`); the boot script loads `dspbootA.bin` onto core A and starts its RTOS (`dsp_util`) |
| **Digital out** | **S/PDIF** DAI → the optical/TOSLINK jack (≤24-bit/192 kHz); internal **loopback** capture (for multiroom/cast) |
| **Wi-Fi / BT** | **Dual-band 2.4 + 5 GHz 802.11ac/VHT** SDIO Wi-Fi (driver `aml_w1`/`amlogic_wireless`; `fwVersion.conf` tags `wifi_hw=NXP`; both bands live on phy0/phy1, ≈390 Mbps VHT) + UART **Bluetooth 5.0** (`hciattach … aml` on `ttyS1`, BlueZ) |
| **MCU** | Companion microcontroller on **`/dev/ttyS2` @ 115200** (`MCUVersion`=23; 16 until the August-2026 OTA), owned host-side by `luciserver`. Drives the **OLED + touch buttons + IR remote + standby**; its firmware updates over this UART via XMODEM (§6.1) |
| **Controls** | **4 touch buttons** (Mode / Vol− / Play-Pause / Vol+ → `gpio_keypad`/`adc_keypad` → MCU → MsgBox 64) + the bundled **IR remote** (MCU-decoded — there is **no** Linux IR/lirc subsystem). An `input_btrcu` BT-HID remote input (under `aml_bt`) also exists. The `rotary-encoder` DT node is **`disabled`** — no knob |
| **Front display** | **0.91″ 128×32 monochrome (blue) OLED**, **MCU-rendered** (§6.1). Not a Linux framebuffer, not `spi_led` |
| **USB** | USB2/USB3 PHYs; **USB-A** host (flash-drive playback; `USBEnable=0`); **USB-C** = power + a device-gadget (**RNDIS + ADB**, `usb0`=192.168.5.1, §7). Arylic also lists USB-C "PC audio" (a USB-audio function); `snd_usb_audio` isn't a loaded module here |
| **Security HW** | OP-TEE secure world (`optee`, `tee-supplicant`), `secmon`, **eFuse** key store + `unifykey`, HW crypto DMA + RNG |
| **Thermal** | single `soc_thermal` zone, ~**51–54 °C** |

> The kernel cmdline carries generic Amlogic **video** args (`vout=1080p60hz`, `hdmimode`,
> `logo=osd0`), but there is **no active framebuffer** (`/dev/fb*` absent) — the A1 drives no
> display from Linux. The only display is the MCU-driven OLED (§6.1).

---

## 2. The audio pipeline

A single ASoC card — **`AML-AUGESOUND`** (`auge_sound`) — exposes five DAIs:

| PCM | DAI | Codec | Dir | Role |
|---|---|---|---|---|
| 00-00 | **TDM-A** | dummy | play+cap | Unused on this product (modelled as a "dummy" codec; nothing wired here) |
| 00-01 | **TDM-B** | **wm8904** | play+cap | **WM8904** (0x1a): DAC → **3.5 mm line-out**; **ADC (`IN2`) = the line-in capture** |
| 00-02 | **PDM** | dummy | cap | Digital-mic port — supported, unused on the LP10 |
| 00-03 | **SPDIF** | dummy | play+cap | **S/PDIF** → optical out (≤24-bit/192 kHz; the input side is unexposed) |
| 00-04 | **LOOPBACK-A** | dummy | cap | Internal loopback (capture the mix → multiroom/cast) |

**Signal flow:**

```
 NETWORK / USB / BT sources ─► sw decode ─┐
                                          │  audionexus       ┌─► WM8904 DAC ─► 3.5mm LINE-OUT
 LINE-IN (3.5mm) ─► WM8904 ADC (IN2) ─────►│  (router +        │   (TDM-B, 0x1a)
                                          │   AED EQ/DRC ──────┤
                                          │   + HiFi4 DSP)    └─► S/PDIF ─► OPTICAL (TOSLINK) OUT
 [loopback tap] ◄──────────────────────────                       (+ loopback → multiroom)
        Volume = ALSA Master (WM8904)   ·   Source select = sourceswitchservice
```

- **`audionexus`** is the central router/mixer (exposes `AUX_START/STOP`, `SPDIF_START/STOP`);
  **`sourceswitchservice`** selects the active input. Source `4` = network/Spotify.
- The line-in can be played locally and/or **streamed** to other LibreWireless rooms via the
  loopback tap.
- **Sample rates:** optical out up to **24-bit/192 kHz** (Arylic spec); the analog line-out is
  line-level (1 Vrms) via the WM8904 (≤24-bit/96 kHz per its datasheet). Observed live:
  44.1 kHz/16-bit (Spotify's native Ogg/Vorbis).
- **94 ALSA mixer elements**: **WM8904** (`Master`, `Headphone`, `Line Output`, `Capture`,
  `EQ1-5`, `DRC`, `High Pass Filter`, `*Capture Mux/Mode`, `HPL/HPR/LINEL/LINER/DACL/DACR/
  AIFOUTL/R Mux`); **AED** HW DSP (`AED EQ/DC-cut/DRC/Noise-Detect/Crossover/Clip-THD/
  Mixer-Gain/master+L/R volume`); **SoC I/O** (`Audio In Source`, `Audio Out Sink`,
  `Audio spdif format/mute/in-source`, `Loopback datain source`, `PDM *`). SoC capture-mux
  options: `TDMIN_A/B/C, SPDIFIN, PDMIN, LOOPBACK_A/B, …`.
- Codec kernel modules: `amlogic_snd_codec_a1` (SoC internal: AED/SPDIF/PDM/loopback),
  `_tas5707` (loaded but **no chip**), `_tl1`, `_dummy`; WM8904 driver built-in. Local-file
  decode via FLAC / `faad` (AAC) / `mpg123` (MP3) / `sbc` (BT) + **GStreamer** + `aml_audio_player`.

> **Live proof (during Spotify playback, 2026-06-28):** `pcm1` (TDM-B → WM8904) opens at
> **44.1 kHz / `S16_LE` / stereo** while `pcm0p`/`pcm3p` stay `closed` — WM8904 is the active
> DAC. Source was Ogg/Vorbis (`Mime: Ogg`, `SampleRate: 44100`). Idle → all `hw_params`
> `closed`. dmesg also carries occasional benign `wm8904 … soc_component_read … -16` (EBUSY)
> register-read warnings — periodic, playback unaffected.

### 2.1 I2C bus map (verified)

Checked two ways — `sysfs` (zero bus traffic) and a safe **`i2cdetect -y -r 0`** (SMBus
*read-byte*; driver-owned chips show `UU` and are never probed). Single bus, `i2c-0` (Meson):

| Addr | Result | Chip |
|---|---|---|
| `0x1a` | `UU` (driver-owned) | **WM8904** codec — active (`status=okay`, driver bound) |
| `0x4a` | `status=okay` but driver **unbound** | **PCM1863** stereo ADC — populated but **unused** (not in the ASoC graph) |
| `0x10` | intermittent ACK, reads `0x00` | phantom (floating-bus read-byte artifact) |
| TAS5707 addrs | nothing | no power-amp chip on the bus |

The image bundles drivers for many board variants (`cs43130`, `pcm512x`, `pcm186x`, `tas5707`,
`wm8904`, `rtc-pcf8563`, `meson_pmic6b`, a touchscreen…); on this LP10 only the **WM8904** is
actually used — the PCM1863 is populated in DT but its driver never bound. The scan was
harmless (device stayed ~51 °C, all daemons up).

---

## 3. Boot, flash & firmware

**Boot chain:** ROM → `bootloader` (U-Boot, mtd0) → `tpl` → kernel in `boot` (mtd6) +
initramfs `/init` → pivot to the **squashfs root** (`root=/dev/mtdblock8 rootfstype=squashfs`).
`misc` (mtd2) = Android-style BCB for recovery signalling; there's a dedicated `recovery`
partition (mtd5). **No A/B scheme** (single system + recovery).

**MTD / SPI-NAND layout:**

| mtd | Size | Name | Use |
|---|---|---|---|
| 0 | 2 MB | bootloader | U-Boot |
| 1 | 8 MB | tpl | loader stage |
| 2 | 256 KB | misc | recovery BCB |
| 3 | 2 MB | logo | boot logo |
| 4 | 8 MB | factory | factory data (ubi0 → `/factory`) |
| 5 | 22 MB | recovery | recovery image |
| 6 | 20 MB | boot | kernel + initramfs |
| 7 | 10 MB | factory_customer | squashfs → `/factory/custom` (vendor web/keys) |
| 8 | **165 MB** | **system** | squashfs root `/` (ro) |
| 9 | **268 MB** | **lsync** | ubifs (ubi1 → `/lsync`, rw, persistent) |

**Filesystem persistence:** `/` = ro squashfs · `/tmp`,`/run`,`/dev/shm` = tmpfs (wiped on
reboot) · `/factory` = ubifs (rw, `assert=read-only`) · **`/lsync` = ubifs, the only durable
writable volume** (227 MB, ~5% used). `/data` → `/lsync/shared`. Notably `/lsync/rakoit_app`
(the main app) lives on the writable volume — updated by its own loader, independently of the rootfs (§10.2) — and so
does the settings store, `/data/libre/env/env.db` (§5).

**Versions** (`/etc/fwVersion.conf`): `build_number=AR241CE_8530`, `build_date=2026-01-12`, `app_svn_version=318`
(June 2026: `AR241CE_9243` / `2025-12-24` / `312` — §14.1), `kernel=5.15` (5.15.137), `defconfig=libre_ls8_24G_v1_c4a_debug_release`,
`platform=LS8`, `target=EVK`, `wifi_hw=NXP`, `ram=256MB`, `flash=512MB`. Userland =
**Buildroot 2020.02.1**. `reboot_mode=cold_boot` + empty `/sys/fs/pstore` = the last boot was
a power-on, not a software reboot (true at every pass; the 2026-09-04 one was a power loss, §14.3). `fw_printenv` reports a bad-CRC U-Boot env → the real cmdline is baked into
the boot image, not a live env. (The `24G` in the defconfig is a module tag — the radio is
dual-band, §1.)

---

## 4. OS & service catalog

Busybox `init` runs `/etc/init.d/S*` (~50 scripts). The stack is **Android-derived**: an
Android `servicemanager` + `/dev/binder`, `adbd`, a `jdwp-control` socket, `android::` symbols
across daemons. Live daemons and their roles:

| Process | Role |
|---|---|
| `rakoit_app` (`/lsync/rakoit_app`, Rust; **v32** since 2026-08-25) | **Main app** — play queue, now-playing, favorites/presets (the remote's FAV/NUM keys), the PlayView publisher, Qobuz/TuneIn/radio-browser client, a PlaylistServer on tcp 2345, and a loopback client on the :2018 tunnel (§10.2) |
| `luciserver` (+ `LUCI_local`) | **Control plane** — the MsgBox/LUCI bus (TCP 7777, UDP 1800); owns the MCU UART |
| `messageboxhandler` | The MsgBox bus backing the LUCI API |
| `env_service` | Persistent settings store — `getenv`/`setenv` → sqlite `/data/libre/env/env.db` (§5) |
| `sourceswitchservice` | Selects the active audio input |
| `audionexus` | Central audio router/mixer (sources → AED/DSP → outputs) |
| `spotifymusicpro` (or `newspotifyhifi`) | **Spotify Connect** (eSDK) — two mutually-exclusive engines; Pro runs here since 2026-09-04, §8.1 |
| `airplaydemo` | **AirPlay 2** receiver (Apple AccessorySDK, PTP) |
| `librecast`/`librecast_lite` | **Google Cast** (`chromecast::CastControl`) — *installed, off* |
| `libreraat` (+ `roon_monitor`) | **Roon** RAAT endpoint (Lua plugins) — *installed, off* |
| `tidalConnect` / `qobuzConnect` | Tidal / Qobuz Connect — *both installed, off* |
| `dmr` | **DLNA/UPnP** renderer (SSDP 1900, control 49494) |
| `UIframework` | Media browser / UI state machine — **Airable/TuneIn**, SD/USB browsing |
| `bluetoothd` + `bluealsa` | **Bluetooth 5.0** A2DP sink+source (SBC+AAC) + `gatt-server` (BLE setup) |
| `WACServer` | **Apple WAC** (iOS "set up speaker") — bound TCP 5000 on 9243; runs without a listener since 8530 |
| `mdnsd` | Apple mDNSResponder (Bonjour) |
| `librewebserver` | **Web UI + HTTP API** (GoAhead/Embedthis) — TCP 80 |
| `ota` | **OTA updater** (drives SWUpdate) — §10 |
| `tcptunnelling` | **Control tunnel + remote relay** — TCP 2018: a plain-text tone/EQ/max-vol control protocol on the LAN (what `lp10`'s equalizer drives — §6.3) **and** the cloud remote-relay path |
| `system_monitor` · `usb_monitor` + `automount` | health/watchdog · USB hotplug + mount |
| `dropbear` · `inetd`→`telnetd` · `adbd` | **SSH 22** · **telnet 23 (root)** · **ADB 5555/5037** |
| `dhcpcd` · `ntpd` · `wpa_supplicant` · `netmonitor` | DHCP · NTP · Wi-Fi · link-event monitor |
| `daemon` (`/factory/custom/csys/bin/daemon`, signed) | Arylic **app loader** ("rak-loader" since 8530): fetches/verifies `rakoit_app` from `cdn.rakoit-ota.com/download`, independent of the OTA (§10.2) |
| `dbus-daemon`(×2) · `rsyslogd` · `mdev` · `getty`(×2) | system plumbing |

Also present: **SDDP/SUDP** (Control4) discovery, cast discovery, and a legacy **LinkPlay-style
UDP** surface (port 3721).

> **Running vs installed (this unit):** of the streaming endpoints, only `spotifymusicpro` (the Spotify Pro
> engine — `newspotifyhifi` until 2026-09-04), `airplaydemo`, `dmr`, and `bluetoothd`/`bluealsa` are running. `librecast`, `libreraat`,
> `tidalConnect`, `qobuzConnect` are installed but their env flags are off, so their init
> scripts don't start them (see the §0 state table).

---

## 5. Persistent settings — the env store

Every feature flag, identity value and user setting on the box lives in one store, read and written through
`getenv` / `setenv` (binder clients of `env_service`). Knowing how it persists answers most "why did it revert" and
"why did it stick" questions — including the one the 8530 OTA raised (§8.1).

- **Live store:** `/data/libre/env/env.db` (`/data` → `/lsync/shared`, the persistent ubifs) — **sqlite**, one table
  `ENV_systemENV(key TEXT PRIMARY KEY, value TEXT, dbit TEXT)`, 264 rows on 8530. Key names in Appendix A.
- **Factory defaults:** `/factory/custom/factoryEnv.conf` — a libconfig list of `{ key = "…"; value = "…"; }`, shipped in
  the `factory_customer` squashfs and replaced by every OTA — with `/etc/custom/factoryEnv.conf` as the rootfs copy.
- **First-boot snapshots, not backups:** `/lsync/env/env_bk.db` (230 rows) and `/lsync/env/factoryEnv_bk.conf` are
  written once, on the very first boot (`env: First boot` / `createUserDBFromFactoryCfg`), while the clock is still at
  the epoch — hence their 1969 mtimes. They record the *original* state and are only read back if the live DB is
  missing or corrupt (`Failed to open %s, trying to open backup %s`).
- **`dbit` is a dirty bit.** A runtime `setenv` sets it to `1`. Exactly the 33 runtime-written keys carry it on this
  unit — `FriendlyName`, `ssid`, `TimeZone`, `current_volume`, `current_mute`, `SP_USERNAME`, `SP_BLOB`, `SpotifyEnabled`,
  `SpotifyProEnabled`, `TidalEnabled`, `QobuzConnectEnabled`, `RoonEnable`, `USBEnable`, `GoogleCast`, `DMRDisable`,
  `airplay`, `MCUVersion`, `IsFDR`, `boot_after_factory`, the static-IP / DNS set, … — and the first-boot snapshot has none.
- **The factory merge adds; it does not rebuild.** At boot `env_service` merges `factoryEnv.conf` into the live DB
  (`mergeDocument` / `objectsMerged`): keys the DB lacks are added, dirty rows are kept. The 34 keys the 8530 OTA
  introduced (`OtaUpdateSchedule`, `OtaSkipCount`, `OtaStandbyUpdate`, `custom_fwdownload_xml`, `TidalGain`, `USBFS`,
  `btmodes`, …) exist only in the live DB, and `FriendlyName=<device-name>`, `ssid`, `TimeZone` have outlived every
  boot since June. Whether a *clean* row is refreshed to a changed factory value is inferred, not traced — but it is
  the only reading that explains the 8530 flag flip: `SpotifyEnabled` was dirty and stayed `1`, `SpotifyProEnabled` was
  clean and took the new factory `1`, and neither engine started (§8.1).
- **Consequences.** A `setenv` from the app, the web UI or `lp10` is durable across reboots and — on the 8530
  precedent — across an OTA's factory flip; a flag you never touched follows the factory. `sqlite3` is on the box
  (`/usr/bin/sqlite3`), so `select key, value, dbit …` is a legitimate read — but the same table holds `SP_BLOB`, the
  Wi-Fi PSK and the web password: **select key names, never `*`.**

> The vendor app keeps its own state next door — `/lsync/source_service/{credentials.redb, secrets.json.enc,
> session_store}` and `/lsync/cache.redb` belong to `rakoit_app` (§10.2), not to the env store.

---

## 6. The LibreWireless control plane (LUCI / MsgBox)

Control is **not** LinkPlay (no `/httpapi.asp`, no port 8899). It's LibreWireless **LUCI**:
the `LUCI_local` CLI talks to `luciserver`/`messageboxhandler` over a numbered "MsgBox" bus.
`~/.bin/lp10` (Go TUI) drives exactly this. `LUCI_local -r <MID>` reads, `-a` dumps all.
**Two write verbs, and the difference is decisive (verified 2026-06-30):** plain
`LUCI_local <MID> <data>` (Usage-1, "old") is a **local** write (`writeLocalMessage`) that sets a
register the **MCU never reads**; **`LUCI_local -remote <MID> <data>`** (Usage-3) is
`writeRemoteMessage` and **pushes to the MCU** — the verb that drives **SoC-owned display data**
(e.g. the now-playing text, §6.4). It does **not** override **MCU-owned** state such as volume
(`-remote 64` is inert — §6/§6.4). `lp10` uses the plain/local form.

**MsgBox map** (verified; live values from inspection):

| MID | R/W | Meaning | Example |
|---|---|---|---|
| 5 / 6 | R | Firmware / MCU version | `AR241CE_8530` / `23` (June: `AR241CE_9243` / `16`) |
| 10 / 50 | R | Active source (`4` = network/Spotify) | `4` |
| 39 | R | Multiroom group (JSON) | `{"devices":[]}` |
| 40 | W | Transport: `PLAY PAUSE RESUME NEXT PREV` (Spotify PREV restarts the track first) | — |
| 42 | R | **Now-playing JSON** (Artist/Album/Track/CoverArtUrl/Mime/SampleRate/TotalTime/PlayState) | — |
| 49 | R | Position (ms) | — |
| 51 | R | Play state — `0`=playing, `2`=paused (inverted); the MID 42 `PlayState` is authoritative | — |
| 63 | R | Mute flag (`MUTE`/`UNMUTE`) — status only; writing `MUTE` does **not** cut audio | `UNMUTE` |
| 64 | RW | **Volume 0-100** (1:1 to ALSA Master) | `56` |
| 70 | R | Speaker active + source | `SPEAKER_ACTIVE,4` |
| 90 / 91 / 92 | R | FriendlyName / IP / device-info JSON (MACs, serial, fw, MCU) | `<device-name>` |
| 124 | R | Available-sources map (opaque encoding) | `2#1,0#2,1#3,0#4,0` |
| 208 | R | `NV Read` — a generic non-volatile read; has answered the OTA manifest URL (`https://lp10-ota.rakoit.com/v1`, June) and `HardwarePlatform:LS8-AC11DBT-GV` (September) | — |
| 770 | R | `mcu_info` (update type `xmodem`) | — |
| 791 | R | `alsaconfig` (samplerate/channels/format) | `44100`/stereo/`I2S` |

> **Mute is client-side (from `lp10`).** The controller never sends a device mute: it
> sets **MID 64 to 0** and remembers the pre-mute level (persisted under
> `~/.local/state/lp10`), restoring it on unmute. The device's own mute path restored the
> *wrong* level, and **MID 63 is status-only** (writing `MUTE` doesn't actually cut
> audio), so volume-to-zero + a client-side restore is the reliable mute. (Accordingly
> the controller's write whitelist is just MID 40 `PAUSE|RESUME|NEXT|PREV`, MID 64
> volume, and an app-only "90" stats toggle — §6.2 — never a mute MID.)

**Multi-controller bus (`remoteID`).** Every update is tagged with the **`remoteID`** of the
controller that made it (UART/MCU, network/app, local); luciserver routes it to the *other*
observers with **echo-suppression** (`Writing to UART because the remoteID is not equal to …`)
plus per-direction **disabled masks**. Practical effect: changing volume with the **physical
buttons/remote** pops the **on-screen volume bar** (the MCU draws it locally when it handles
the keypress, then reports MID 64 up "from UART side"), but changing volume via **`lp10`** (a
network-side MID 64 write) updates the level **silently** — the MCU renders no bar for a
host-pushed value. But the level does **not** fully propagate (refined 2026-06-30): a plain `lp10` MID-64 write moves
the **SoC/DSP — audible — volume** yet **not the MCU's own volume**, so the **remote resumes from
its own last value** and the panel shows a **stale** number. **`-remote 64` does NOT fix this** —
tested 2026-06-30 and **inert** (not audible, no panel bar): the MCU **owns** volume and ignores a
host push, so volume must stay a plain/local write. The stale panel / remote-desync is an
MCU-ownership limitation, not a write-verb bug (`-remote` drives SoC-owned *display* data like MID
42 — §6.4 — just not MCU-owned volume).

**Web API:** `librewebserver` (GoAhead/Embedthis Appweb) on :80 serves the setup/control UI,
`.asp` templates, and a `/action` **goform** API (e.g. `GoForm_SetTidalmode`), plus the
captive-portal setup + OTA pages and `logs → /tmp/libre/logdump/`. Routes/auth in
`/factory/custom/web/{route.txt,auth.txt}`; web creds from env (`Defaultweb*`/`Newweb*`).

**Env store:** the sqlite settings DB behind `getenv`/`setenv` — §5. Every feature is gated there (Appendix A).

### 6.1 Front OLED & the MCU (host UI)

The front panel is a **0.91″ 128×32 monochrome (blue) OLED**, driven entirely by the on-board
**MCU** — not by Linux. Behaviour (live-observed):

- **Idle on network:** a media glyph + the source category `NETWORK` + a fixed status glyph
  (a small columned-building icon with `3B`/`38`).
- **Playing:** line 1 = the active service (e.g. `Spotify`); line 2 = a scrolling carousel of
  the now-playing metadata — Artist · Album · Track (e.g. `The Police · Outlandos D'Amour ·
  So Lonely`).

The `LP10` text on the chassis is **silkscreen**, not on the panel. How it works:

- **No framebuffer** (`/dev/fb*` absent, no graphics/backlight class) and **not** the
  `spi_led`/`/dev/spidev1.0` path (that's an unused secondary LED line — `spi_led -c` is a
  confirmed no-op).
- **`luciserver` owns `/dev/ttyS2` @ 115200** (holds the fd; `stty` confirms 115200) via an
  `LUCIUARTController`, bridging the MsgBox bus ↔ the MCU.
- The host sends the MCU **semantic state, not pixels** — protocol strings include `Display
  request`/`Display response`, "UPDATE AUDIO PARAMS DeviceName/SR/Channel TO MCU", source,
  now-playing (MsgBox 42), volume, standby. The **MCU renders** fonts/icons itself — which is
  why on-screen text like `NETWORK` exists in **no** Linux binary, and why the idle `3B`/
  building glyph (absent from every MsgBox) is MCU-firmware-internal.
- The MCU is also the **input + power** controller: the **4 touch buttons** + IR remote return
  up the UART (volume → MsgBox 64), plus **standby** (`ACK_MCU_ENTERED/EXITED_STANDBY`). Its
  own firmware (v16 at the teardown, **v23** since the August-2026 OTA — §14.2) updates over this UART via **XMODEM** (MsgBox 770 `type:xmodem`).

> Unrelated: `wlcdmi` is a **Wayland + DRM / Widevine-CDM** GStreamer element for the **Cast**
> media path (`wl_display_*`, `/usr/lib/wlcdmidrm`, `/dev/secmem`), not the front panel.

### 6.2 The `lp10` streaming loop (the `@@`-record protocol)

`lp10` doesn't poll `LUCI_local` per frame. It runs **one** BusyBox-ash loop on the
device (over its single SSH connection) that streams **`@@`-framed records** on stdout
and takes whitelisted commands on stdin — so the laptop side just parses a stream and
the per-tick device work is a handful of builtins. The sections of a record:

| Tag | Cadence | Contents |
|---|---|---|
| `@@i` | once per connection | static device/network: `net`/`iface`/`ip`/`mac`/`gw`, link (`speed`/`duplex` or `ssid`/`freq`/`rate`), `build`/`app`/`platform`, `/lsync` usage, `dns` — parsed from `ip route` / `iw` / sysfs / `fwVersion.conf` by shell parameter-expansion (no `sed`/`grep`) |
| `@@c` | once per connection | **capability block** (feeds the diagnostics overlay's *services* read-out): each **marketed** streaming service as `on`/`off`, from `pidof` for the running daemons (`newspotifyhifi`/`airplaydemo`/`dmr`/`bluetoothd`) and `getenv` for the env-gated rest (`GoogleCast`/`TidalEnabled`/`QobuzConnectEnabled`/`USBEnable`). Non-marketed firmware baggage (Roon/Alexa/Matter) is **not** read. |
| `@@B` | on change | MID 42 now-playing JSON (only re-shipped when it differs) |
| `@@p` | every ~3rd tick | MID 49 position ms (the UI extrapolates between reads; a detected skip forces a re-read) |
| `@@t` | per tick | MID 51 play-state |
| `@@v` | per tick | MID 64 volume |
| `@@s` | per tick **only while the diagnostics overlay is open** | resource/link stats: uptime, loadavg, mem, SoC temp, iface byte counters, Wi-Fi signal/link/noise, three ICMP RTTs (laptop/gateway/internet), the ALSA DAC's *actual* rate/format/channels + buffer fill, CPU clock, run/total procs |
| `@@E` | — | end-of-record |

- **Adaptive cadence:** ~1 s while playing, stretching to ~3 s when idle; only MID 51 &
  64 are read every tick, MID 49 every 3rd, MID 42 only on change.
- **stdin = a command whitelist**, never `eval`: `40 PAUSE|RESUME|NEXT|PREV` (transport),
  `64 <0-100>` (volume) — these are forwarded to `LUCI_local` — plus an **app-only
  `90 1|0`** that toggles `@@s` emission (overlay open/closed). That "90" is **intercepted
  by the loop and never reaches `LUCI_local`**, so it does **not** touch the device's real
  **MID 90 (FriendlyName)** — it's a controller-side multiplexing of the number on the
  same stdin channel, not a MsgBox write.
- **Self-reaping:** the loop detects a dead SSH session by read-timing and exits, so both
  ends are reaped however the controller died; the client reconnects with backoff.

### 6.3 The :2018 control tunnel (tone / EQ / max-volume)

The `tcptunnelling` daemon's **TCP 2018** is not only the cloud remote-relay path (§10) —
on the LAN it speaks a **plain-text control protocol** for tone, EQ, deep-bass, and the
output cap, and it's the channel `lp10`'s equalizer drives **directly** (separate from
the SSH player stream, so a dead tunnel only greys out the EQ). Wire format: bare ASCII
**`CODE:VALUE;`** (semicolon-terminated, **no newline, no framing, no auth**); a bare
**`CODE;`** is a *query* — the device answers by **broadcasting `CODE:VALUE;` to every
connected client**. The device clamps authoritatively and echoes the applied value back.

| Code | Control | Range |
|---|---|---|
| `MXV` | **Max-volume cap** (output ceiling) | 30–100 (doc) |
| `EQE` | EQ **enable** — whether the selected preset is applied at all | 0/1 |
| `EQS` | EQ **preset index** into `PEQ` (`0@Flat,1@Classical,2@Pop,3@Jazz,4@Rock,5@Vocal`) — **not** an on/off switch | 0–5 |
| `BAS` / `MID` / `TRE` | **tone** (bass / mid / treble) — live regardless of `EQE` | −10…+10 dB |
| `VBS` | **deep/virtual-bass** switch | 0/1 |
| `VBI` | deep-bass **intensity** | 0–100 |
| `BAL` | **balance** (+ favours right) | −100…+100 |

> **Corrected 2026-08-22 (re-verified on MCU v23 on 2026-09-02 and 2026-09-12):** this socket is the full public
> **Arylic UART API** (developer.arylic.com/uartapi) tunnelled to the MCU — the same 102 + 21-code
> table in both `mcu.bin` images. `EQS` is the preset *index* (an earlier `lp10` toggled it 0/1 and
> was selecting *Classical* for "on"); `EQE` is the real enable. Other safe getters answered live:
> `VER:23-4ef47210-9` · `STA:NET,0,68,0,0,3,0,0,1,0` · `SRC:NET` · `LST:NET,BT,LINE-IN,USBPLAY` ·
> `VOF:1` `VST:3` `DLY:60` `CFE:0` `CFF:110` `LED:1` `BEP:1` `POM:NONE` `CHN:L` `MRM:N` · `NAM` hex-UTF-8.
> **Never send bare** `WRS;`, `SYS:*`, `DEF:SAV`, `POP/STP/NXT/PRE/PST`, `PMT:`/`COE:` (setup / reboot /
> transport). **Client gotcha:** `tcptunnelling` serialises clients — a burst of one-shot connections
> (26 in a row) left the next connect hanging; hold one connection and query sequentially.

> **Write-through (verified):** a `CODE:VALUE;` set from a **network client reaches the
> MCU** and takes effect; the *same* string injected locally via **`LUCI_local 112` does
> NOT** propagate — so this socket is the **only** way to drive these settings. (`112` is
> the MsgBox id the tunnel uses internally; poking it over the LUCI bus is inert.)

> **Gotcha:** a low **`MXV`** (Max-Volume cap) is what makes the **IR remote / Spotify /
> app volume feel stuck near the top** — they hit the cap, not a fault. Raise `MXV` to 100
> for the full range. (This, not the EQ, is the firmware-level cause of "volume won't go
> higher".)

### 6.4 Writing to the MCU — the `-remote` verb & drawing on the OLED (verified 2026-06-30)

`LUCI_local` has **two** write verbs and only one reaches the MCU (§6): plain `<MID> <data>` is a
**local** write the MCU ignores; **`-remote <MID> <data>`** (`writeRemoteMessage`) is pushed to
the MCU. This is *the* lever for the front panel — the OLED is a **fielded** display (the SoC
sends semantic state, the MCU renders it, §6.1), so you drive it by pushing the right MsgBox:

- **`LUCI_local -remote 42 '<now-playing JSON>'`** — while a track is playing (now-playing view
  active), replaces the panel's Album/Artist/Track lines with your text. **Proven live:** a custom
  MID-42 push made the OLED read `HELLO LUCAS / drawn by Claude`. It **self-heals** to the real
  track on the next source push, so re-push (~1 Hz) to hold it.
- MID-42 payload is literal JSON:
  `{"CMD ID":3,"Title":"PlayView","Window CONTENTS":{"Album":…,"Artist":…,"TrackName":…,`
  `"PlayUrl":"spotify:track:…","PlayState":0,"Current Source":4,"Mime":"Ogg","TotalTime":…}}`.
  A playing-looking `PlayUrl`/`PlayState` keeps it in the now-playing view; an **empty `PlayUrl`
  drops it to the idle NETWORK screen**.
- The **plain** verb does nothing here: a 200× flood of `LUCI_local 42 …` never touched the OLED
  (the source service's real-track pushes always won). But that same local write **is** seen by
  `lp10` (which reads MID 42 with `-r`), so `lp10`'s own screen shows the fake track while the OLED
  shows the real one — one messagebox, two readers, two truths.
- Transport confirmed in `libluci_helper.so` (Android binder): `writeRemoteMessage` /
  `SendMBResponse` / `WriteTunnelDataToUart` reach the MCU; `writeLocalMessage` does not.
- **`-remote` drives SoC-owned display data, not MCU-owned state:** `-remote 64` (volume) is
  **inert** (not audible, no panel bar — tested 2026-06-30) because the MCU owns volume; so the
  volume-on-panel / remote-sync goal is **not** reachable this way, and volume stays a plain local
  write.
- **Ceiling — the `716` "Display request" result (offline firmware RE, 2026-07-01).** Reversing
  `luciserver`'s `.data` **MsgBox descriptor table** (§6.5) settles what `716` is: a **real,
  registered** MCU messagebox — paired with `717 "Display response"`, in the same `flag=0x100`
  *live-control* class as volume, `LED Control` (207) and `Equalizer` (711/712). **But its payload
  format is defined in _none_ of the device's Linux binaries.** The only occurrences of `716`/`717`
  in the whole SoC image are the two 4-byte table cells; **no code** in `luciserver`, `UIframework`,
  `messageboxhandler`, `ota`, `audionexus`, `sourceswitchservice`, `yaniserver`, `tcptunnelling` or
  any `lib*.so` constructs, parses or references them. `luciserver` merely **registers** `716` and
  would relay it generically over the UART — so a blind `-remote 716` *reaches* the MCU but with a
  body the MCU can't parse (hence no render and no `717` reply, as tested). **Its shape lives off the
  SoC** — in the **vendor app** (which emits it over LUCI/TCP) or the **MCU firmware** (`mcu.bin`,
  not on the device; fetched from the OTA server). **Offline *SoC*-firmware RE is therefore
  exhausted for `716`;** the format lives in the MCU. **`mcu.bin` has since been pulled and verified
  (§10)** — it's an unencrypted MVSilicon image that *does* contain the LUCI handler + panel renderer
  (`disp_a200.c`/`disp_device.c`), so `716` is recoverable there (caveat: MVSilicon's proprietary
  ISA has no stock disassembler). The cheaper shot remains **capturing the vendor app's LUCI/TCP
  traffic** while it shows a prompt.
- **Net result for "draw on the OLED":** the **only** host-writable panel content is the `RemoteUI`
  (`MID 42`) now-playing record — line 1 = source label, and volume/icons are MCU-owned. There is
  **no** host-reachable full-screen message/prompt view — and **that is now confirmed at the MCU
  level itself** (§10.3, `mcu.bin` reverse): the MCU firmware's host-facing JSON parser recognises
  the single view `Title = "PlayView"` (keys `TrackName/Artist/Album/TotalTime/BitDepth/SampleRate/
  repeat/shuffle`) and **no** `Message`/`Text`/`Popup`/`Notification` view string exists anywhere in
  it; `716`/`717` appear as neither strings nor constants. So `MID 42` with alternate `Title`s is
  rejected because the MCU simply has no other host view, and the panel's other screens
  (source/volume/standby) are MCU-internal, driven by MCU state — not host text. **This closes the
  question across all three layers** (SoC UI, LUCI table, MCU firmware). *(Correction: the earlier
  "MID 78 = cover-art" guess was wrong — the descriptor table shows
  `MID 78 = SPOTIFY_EXTERNALMEDIAPLAYER`; artwork metadata is `MID 43 "ArtWork Metadata"`.)*

### 6.5 The MsgBox descriptor table (reversed from `luciserver`, 2026-07-01)

`luciserver` registers its **named** messageboxes from a static table in `.data`. It's a non-PIE
`ET_EXEC`, so the name pointers are inline/absolute (searchable). Each entry is **7 words / `0x1c`
bytes**:

```c
struct LUCIMessageBox {        // stride 0x1c
  char*    name;               // +0x00  -> .rodata string
  uint32_t mb_number;          // +0x04  e.g. 0x2cc = 716
  uint32_t _reserved0;         // +0x08  always 0
  uint32_t flags;              // +0x0c  0x100 or 0x00 (see below)
  uint32_t _reserved1;         // +0x10  always 0
  uint32_t remote_id;          // +0x14  init 0xffff (unset sentinel)
  uint32_t _reserved2;         // +0x18  always 0
};
```

**`flags`** splits the ~188 named boxes into two classes (membership verified; the *meaning* is
inferred): `0x100` (67 boxes) = **live control/status** channels the panel & remote reflect —
`RemoteUI` (42), `Current Source` (50), `Play Status` (51), `volume setting` (62), `CastMuteStatus`
(63), `LED Control` (207), `Equalizer request/response` (711/712), **`Display request/response`
(716/717)**, Gpio/Tunnel/BLE/standby; `0x00` (121 boxes) = **bulk data/content** blobs
(`ArtWork Metadata`, `PlayList Response`, `Network List`, `GetWifiScanResults`, `Log Dump`,
`Base`/`Treble Control`, …).

**Canonical names ≠ live semantics.** The table gives LinkPlay's *canonical* names; the MsgBox bus
is generic-by-integer, so a few live IDs differ from (or aren't in) the table:
- **`42 RemoteUI`** = the now-playing/`PlayView` JSON pushed to the panel (§6.4) — the panel-content channel.
- **`64`** (the live **volume** box, §6) has **no table entry at all**; the named `62 volume setting` is a different box.
- **`78 SPOTIFY_EXTERNALMEDIAPLAYER`** (not cover-art); **`43 ArtWork Metadata`** is the artwork channel.
- **`92 DEVICE_DETAILS`** = the device-info JSON; `90 Device Name`, `91 Device Mac ID`.

Full named map (`C` = `flags=0x100` control, `.` = content; read down the left column, then the right):

```
   6 . Host version info             141 . WPS Triggger
   7 C CastVersionInfo               142 C Wac start
   8 . Device Serial num             143 C Wac status
  10 C IsAllowedRequest              144 C Wac stop
  11 . IsAllowed                     149 . FACTORY_RESET_REQ
  12 . Airplay Control               150 . FACTORY_DEFAULT
  13 C Airplay Status                151 C RSSI
  14 . ACPShareCommand               161 C Getdevice list
  15 . ACPShareResponse              162 . Request Playstate from remotehost
  16 C GetLocalTime                  163 . Playstate repsonse from remotehost
  20 . DEEPSLEEP_START               164 C Sendto remotehost
  21 . DEEPSLEEP_END                 165 . Recievefrom Remotehost
  22 . NET_STANDBY_START             170 . CT start
  23 . NET_STANDBY_END               171 . CT stop
  24 . STANDBY_STATUS                205 . Avs Login stat
  31 . USB Control Request           207 C LED Control
  32 . USB Control Response          208 C NV Read
  33 . USB Device Info               210 . notify mcu of bt status
  35 . SDcardConnectedcnt            211 . FirmwareUpgradeStart
  36 . Device DisConnected           212 . SDDP Notify
  37 . Gracefull Shutdown            213 . UsernamePasswordNotifier
  38 . Device Connected              214 . ShareMode
  39 . Sound Device  Info            215 . PairMode
  42 C RemoteUI                      216 . ddmsSlaveInformation
  43 . ArtWork Metadata              219 C zone volume control
  44 C Airable TrackInformation      220 C client zone volume
  49 . Current Time                  221 . PairStatus
  50 C Current Source                222 . castotaupdate
  51 C Play Status                   223 . FirmUpg_reb_internet
  52 . PlayList control              224 . Cast_Is_Enabled
  53 . PlayList Response             226 . castupdateinfo
  54 . is play status value          227 . DMR Stop
  56 . Browse contorl                228 . DMR Start
  57 . DMP PlaylistHandler           229 C getNTPTime
  58 . MUltiple Device Response      230 . AUDIO_OUTPUT_FS
  62 C volume setting                230 . getcurrentWeather
  63 C CastMuteStatus                231 . CAST_SERIAL_NUM
  65 . FwUpgrade                     232 . BatteryPower
  66 . Firmeware_progress            233 . Mic Control
  68 . HostImage_Ready               234 . AVS appService
  69 . RequestForFirmwareUpgrade     235 . Localcheckupdate
  70 . host App control              236 C SPEAKER_FW_UPDATE
  71 . SD card status                238 . FORCED_UPDATE
  72 . TriggerWifiScan               239 . LowLatencyAudio
  73 . GetWifiScanResults            240 . AVS status
  75 C SPOTIFY_PRESET_ACTIONS        250 . Log Dump
  76 . SPOTIFY_DISCOVERY             297 . DSD_SOFTMUTE
  77 C Offline Downloads Status      298 . DSD_SOFTUNMUTE
  78 . SPOTIFY_EXTERNALMEDIAPLAYER   300 . ClientInformation
  79 . SPOTIFY_PLAYBACKSTATEREPORTER 302 . FACTORY_TESTS
  80 . Play AudioIndex               494 . CastSetupStartNotification
  81 . i2c_access                    495 . suspendresumecast
  82 . Play AudioIndex Response      496 C SETUP Stop
  83 . Stop AudioCue                 497 . Device Name local
  84 . CivetWeb Response             498 . TriggerSetup
  85 . CivetWeb Ack                  499 . DebugInternal
  90 . Device Name                   502 . Base Control
  91 . Device Mac ID                 503 . Treble Control
  92 C DEVICE_DETAILS                506 . Build info
  95 . in start                      555 . MicDump
  96 . in stop                       556 . AUDIO_OUTPUT_FS_ACK
  97 . external input start stop     561 . AppleHomeAppstatus
  98 . ddms internal                 562 . airplaypasswordrequest
  99 . ddms rate adapt               563 . airplaypasswordresponse
 100 . ddms                          571 C CASTOEMAPP_CONFIG_REQUEST
 101 . ddms ooh master               572 C CASTOEMAPP_CONFIG_RESPONSE
 102 . ddms ooh slave                573 C CASTOEMAPP_TIMEZONE_CONFIG
 103 . ddms status                   611 C STANDBY_MSG_FROM_MCU
 104 C ddms groupid                  612 C STANDBY_MSG_FROM_LS
 105 C ddms ssid                     625 C CUSTOM_ENVS_VALUES_REQUEST
 106 . speaker type                  626 C CUSTOM_ENVS_VALUES_RESPONSE
 107 . Scene Name                    651 C log report
 108 C StereoPair Mode               653 C MCU Log Data
 109 . Music Discovery               654 C MCU Log Response
 110 C Zone Info                     655 C MCU Log Request
 111 C Tunnel Start                  657 C log report Req
 112 C Tunnel Data                   658 C log report Resp
 113 . Miracast                      661 C BLE_SETUP_STATUS
 114 . RebootRequest                 662 C BLE_SETUP_EVENTS
 115 . OnReboot                      663 C BLE_REMOTE_EVENT_REQ
 116 . ON Reboot Response            664 C BLE_REMOTE_EVENT_RES
 117 C mTOs                          671 C Cloud Tunnelling
 118 C sTOm                          681 C Gpio request
 124 C netstatus                     682 C Gpio event
 125 . net conf                      683 C led request
 126 C ipodcontrol                   684 C led event
 127 C Forget network                690 C Home away
 128 . net conf static               691 C LTE Request
 129 . Network List                  692 C LTE Response
 130 . Network Options               693 C LTE Modem
 134 . network link up/down status   711 C Equalizer request
 135 . Network Monitor req           712 C Equalizer response
 136 . Network Monitor               716 C Display request
 140 . WPS_STATUS                    717 C Display response
```

*(`.` = `flags=0x00`, `C` = `flags=0x100`. `64` volume, `770` MCU-xmodem and `791` alsaconfig are
live-observed IDs with **no** named entry — the bus doesn't require one.)*

---

## 7. Networking

| Iface | Address | Notes |
|---|---|---|
| `eth0` | **<device-ip>/24** (+ IPv6 ULA + LL) | **Default route** (via <gateway>). MAC `<powerline-mac>` = the TL-WPA4220 powerline link |
| `wlan0` | link-local only — **DISCONNECTED** | Up but unassociated (device is wired). MAC `<wlan0-mac>`. Radio is dual-band (§1) |
| `p2p1` | — | Wi-Fi-Direct/P2P iface |
| `usb0` | **192.168.5.1/24** | USB-C device-gadget (below) |
| `lo`, `sit0` | 127.0.0.1 / — | loopback, inactive 6in4 |

DNS via DHCP (`fibertel.com.ar`; 1.1.1.1 / 8.8.8.8 + ISP). IPv6 active on the LAN.

**Listening ports** (2026-09-12; owners resolved through `/proc/net/*` socket inodes):

| Port | Owner | Service |
|---|---|---|
| tcp 22 / 23 | dropbear / inetd | **SSH** / **telnet (root)** |
| tcp 80 | librewebserver | Web UI / API (a v6 socket; reachable over v4) |
| tcp 2018 | tcptunnelling | **control tunnel + remote relay** (§6.3); one established loopback client — `rakoit_app` |
| tcp 2345 · 33719 | rakoit_app | PlaylistServer (`playlist_addr`, bound 0.0.0.0) · a second, dynamic-port listener (43761 the boot before; HTTP 404) |
| tcp 5037 (lo) / 5555 | adbd | ADB (local / **network**) |
| tcp 7000 | airplaydemo | AirPlay (RTSP/control) |
| tcp 7777 | luciserver | LUCI control |
| tcp 9095 **or** 9096 | spotifymusicpro **or** newspotifyhifi | Spotify ZeroConf — **per engine** on 8530: Pro `:9095`, HiFi `:9096` (HiFi sat on 9095 before 8530); §8.1 |
| tcp 49494 | dmr | DLNA control |
| udp 68 / 123 | dhcpcd / ntpd | DHCP / NTP |
| udp 1800 / 1900 | luciserver / dmr | **LSSDP** discovery (below) / SSDP |
| udp 3721 | airplaydemo | LinkPlay-style UDP discovery |
| udp 5353 | mdnsd **and** the Spotify engine | mDNS (the eSDK runs its own responder) |
| dynamic udp | mdnsd · airplaydemo · dmr | — |

`WACServer` (Apple WAC setup) runs but **no longer binds tcp 5000** since 8530 (it did on 9243).

**LSSDP** (udp 1800, answered by `luciserver`): an SSDP-style `M-SEARCH` to the box (or the multicast group) gets
`HTTP/1.1 200 OK` with `USN:<wlan MAC, no colons>` · `Version:LSSDP 1.0` · `FWVERSION:AR241CE_8530.23.2` · `CAST_MODEL:LP10` ·
`PORT:7777` · `TCPPORT:2020` · `DeviceName:<device-name>` · `State:S` · `NETMODE:ETH0` · `WIFIBAND:ETH` ·
`SPEAKERTYPE:Wireless Speaker` · `SOURCE_LIST:LS8::01000030` · `MRAMode:DDMS`. Pure UDP, no auth — the ssh-free liveness
and discovery probe `lp10` uses (Appendix B).

**USB-C device-gadget** (`S89usbgadget`): plugged into a host, the LP10 presents a composite
**RNDIS network (`usb0`=192.168.5.1) + ADB** gadget (VID `0x18D1` Google / PID `0x4e26`) — the
service/debug path. Arylic also lists USB-C as **"PC audio"** (a USB-audio gadget function);
`snd_usb_audio` wasn't a loaded module at inspection, so that UAC path wasn't separately confirmed.

**Wi-Fi firmware-crash bug:** the SDIO Wi-Fi (`aml_w1`) firmware can wedge RX-ok/TX-dead on a
link event; the in-driver recovery never advances, so the box keeps scanning but can't
re-associate until a power-cycle. **Wired eth0 (powerline) removes the trigger** — it can only
recur if it falls back to `wlan0`.

---

## 8. Streaming & control protocols

Arylic markets **Spotify, AirPlay 2, Google Cast, Tidal, Qobuz, and DLNA/UPnP**; the LS8
firmware also ships **Roon (RAAT), Alexa/AVS, Matter, and QPlay**. **Actually enabled on this
unit right now** (env flag *and* a running daemon): **Spotify Connect, AirPlay 2, DLNA/UPnP,
Bluetooth**. Installed but **off**: Google Cast (`GoogleCast=false`), Roon (`RoonEnable=0`),
Tidal (`TidalEnabled=0`), Qobuz (`QobuzConnectEnabled=0`), Alexa (`AVSEnabled=0`), Matter
(`MatterEnabled=0`), QPlay, and USB playback (`USBEnable=0`) — all togglable via the app/web.

### 8.1 Spotify Connect

A **certified Spotify Connect endpoint** on Spotify's embedded SDK (eSDK). The firmware ships **two engines**, each a
9,680-byte launcher plus a library, and exactly one may run:

| Engine | Daemon / lib | eSDK | ZeroConf on 8530 | Notes |
|---|---|---|---|---|
| **HiFi** ("legacy") | `newspotifyhifi` / `libspotifyhifi.so` | **3.203.239-g1d6bd565** (unchanged 9243 → 8530) | SRV **9096**, `version 2.9.0` (9095 before 8530) | Ogg/Vorbis + AAC; its softvol volume path always worked |
| **Pro** ("new") | `spotifymusicpro` / `libspotifypro.so` | **3.211.130-g110e3e03** (3.205.187 on 9243) | SRV **9095**, `version 2.10.0` | the only engine that negotiates **FLAC**; its volume bypassed the softvol right after 8530 — fixed by `rakoit_app` v32 (§10.2) |

**This unit:** HiFi from June to 2026-09-04; **Pro since 2026-09-04 15:06**, switched from `lp10`'s services pane (§14.3).
The phone is only a remote — the **speaker** authenticates and pulls audio itself (`Server: eSDK`; Ogg/Vorbis via
`decoder_vorbis.c`, FLAC on Pro when the account allows).

- **Env-gated and mutually exclusive.** `S99newspotifyhifi` starts HiFi only when `SpotifyEnabled=1 && SpotifyProEnabled=0`;
  `S99spotifymusicpro` starts Pro only when `SpotifyProEnabled=1 && SpotifyEnabled=0`. Each script is guarded on the *other*
  flag being clear, so **both set = neither engine starts** — exactly what the 8530 OTA produced (factory default flipped
  `1/0 → 0/1`, the user's dirty `SpotifyEnabled=1` survived the merge, §5). `lp10` therefore always writes the pair
  together (`1/0`, `0/1` or `0/0`), and either pin persists across reboots (§5). No CLI args — all config from env.
- **Network-event lifecycle, not boot.** `netmonitor` (a shell loop polling the default route every 3 s) calls every
  `S*` script that mentions `netready` with `netready` on the first address, `netchange` on an address / interface change,
  `netup` / `netdown` on link state. The engine scripts relaunch on `netup|netready|netchange` and **kill on `netdown`**;
  both skip in setup-AP mode (`/tmp/ap`). Every reboot, Wi-Fi wedge or IP change therefore rebuilds the whole session —
  why the Wi-Fi wedge killed Spotify, and why wired `eth0` fixes it.
- **ZeroConf handoff** (`dns-sd -Z _spotify-connect._tcp local`): `mdnsd` advertises the instance **`<device-name>`** with
  the engine's SRV port and TXT `CPath=/zc VERSION=1.0 Stack=SP` → `http://<ip>:<port>/zc`. An unauthenticated
  `GET /zc?action=getInfo` returns `status 101 OK`, `deviceType SPEAKER`, `brandDisplayName Arylic`, `modelDisplayName LP10`,
  `remoteName`, `libraryVersion` (the eSDK build), `version` (2.9.0 HiFi / 2.10.0 Pro), `tokenType accesstoken`,
  `scope streaming`, `supported_drm_media_formats` (Pro: two entries, `formats 65606`), plus the device's public key and
  `deviceID`; `addUser` then does the DH key exchange and the credential blob is decrypted on-device — **the password never
  reaches the speaker**. `lp10` polls `getInfo` for "is the engine up, on which build, signed in as whom" with no ssh in
  the loop (Appendix B).
- **Login persistence.** The engine's own string — `SPOTIFY: DIFF USER LOGGED IN STORE USERNAME %s AND THE BLOB %s IN
  ENV !!` — saves the username and a reusable blob to env (`SP_USERNAME` / `SP_BLOB`, both dirty rows), beside the OEM
  identity `SpotifyClientId` / `SpotifyProductID` / `SpotifySpeakerType=Speaker`. On start it reuses the blob (syslog:
  `SAME USERNAME IS THERE STORE THE BLOB …` — **that line prints the blob in clear**, §15); on failure it falls back to
  ZeroConf. `LOGGED_OUT → LOGGING_OUT → LOGGED_IN`, **single-user**. Runtime art / now-playing cache is `/lsync/cache.redb`
  (Rust `redb`, no plaintext credentials). Telemetry: `EsdkPlaybackStats` / `EsdkDownload` / `EsdkHttpErrors`. **Never
  dump the blob.**
- **Pro engine reconnects (observed 2026-09-12, not diagnosed).** In the 23 h of syslog retained, Pro logged 54 ×
  `The connection to Spotify has been lost` (`ConnectionNotify = 3`), each followed within seconds by a re-login with the
  stored credentials (`ConnectionNewCredentials`, 53 ×) — 1 min to 1.5 h apart — while DLNA subscription renewals and ssh
  sessions on the same link stayed up. No HiFi syslog survives to compare against.

**Proven socket map** (fd → `/proc/net/tcp`, HiFi engine, one capture):

| fd | Target | Meaning |
|---|---|---|
| 6 | LISTEN on the engine's ZeroConf port (table above) | ZeroConf HTTP endpoint (= advertised SRV port) |
| 8 | → Google Cloud `:4070` | **Spotify control / access-point** (AP ports 4070/443) |
| 11 | → Akamai `:443` | **audio CDN stream** (present only while playing) |
| 9 / 10 | `controlC0` / `pcmC0D1p` | ALSA mixer / PCM out |
| 4 / 5 | `/dev/binder` / `/dev/urandom` | Android IPC / TLS-DH entropy |

> Two **device-owned** connections, exactly as Spotify Connect prescribes. Idle: control to an
> AP `…:4070`, no audio socket. Playing (verified): control `…:4070` **+** audio CDN `…:443`
> (Akamai), now-playing `Mime: Ogg`, `SampleRate: 44100`. (AP/CDN IPs vary per session.)

### 8.2 AirPlay 2
`airplaydemo -n -d`, built from Apple's **AccessorySDK** with **PTP** timing (`libre_airplay_v2`).
Ports 7000 (RTSP/control) and udp 3721; the WAC setup port 5000 (`WACServer`) has not been bound since 8530. Apple Home / access control + AirPlay
password (env `AirPlayHomeAccessControlEnabled`, `AirPlayAppleHomePassword`, `AirPlayMetaData`).

### 8.3 Google Cast
`librecast`/`librecast_lite` (`chromecast::CastControl`). **Currently off** (`GoogleCast=false`,
`librecast` not running), but the per-device **Cast attestation cert** is present
(`/factory/client.crt`, issuer `RAKOIT … OU=Cast, CN=LP10`). Env `GoogleCast`, `CastSetup`,
`CastTOS`, `CastUsageReport=false`, `CastAlsaOutput*`, `GCASTVersion`.

### 8.4 Roon (RAAT)
`libreraat` + `S99roon_monitor` (RAAT, ALSA output plugin, Lua-scripted). **Currently off**
(`RoonEnable=0`, `libreraat` not running; not in Arylic's marketed list). Env `RoonEnable`,
`RoonOutputType=speakers`, `RoonMQACapabilities=none`, `RoonDOPEnable=0`.

### 8.5 Tidal / Qobuz Connect
`tidalConnect` (**off**, `TidalEnabled=0`, not running) and `qobuzConnect` (**off**,
`QobuzConnectEnabled=0`, port 8000, `QobuzConnect_AppId/AppSecret`). `rakoit_app` also speaks
the Qobuz REST API for in-app browse.

### 8.6 Bluetooth
**BlueZ 5.0** `bluetoothd` + **`bluealsa -p a2dp-source -p a2dp-sink -c SBC -c AAC`** → BT
**speaker (sink)** *and* **transmitter (source)**. Adapter `hci0` (UART), HCI/LMP **5.0**, BD
addr `<bt-mac>`, name `<device-name>` (default `BT_DeviceName=Arylic LP10`), Class
**`0x240414` = Audio/Video → Loudspeaker**. BLE `gatt-server` for setup. None paired at inspection.

### 8.7 DLNA / UPnP
`dmr` renderer — SSDP on 1900, control on 49494. `description*.xml` in the web dir. Env
`DMREnable`/`DMPEnable`.

### 8.8 Airable / TuneIn / aggregated services
`UIframework` embeds the **Airable** aggregator (`auth.airable.io`) + **TuneIn** (`opml.tunein.com`,
`api.radiotime.com`). Airable fronts services seen as reachable hosts — **Qobuz, Napster,
HighResAudio, IDAGIO, Sony Select hi-res**. Internet radio, podcasts, browse/login. Env `Airable*`,
`TuneIn*`.

### 8.9 Others (provisioned)
**QPlay** (QQ/Tencent — `QPlay_*`), **Alexa/AVS** (`Alexa*`, off), **Matter** (`MatterEnabled=0`),
**SDDP/Control4**, **USB/SD playback** (`USBEnable=0` — off; GStreamer + `automount`), **Dirac** hooks
(`DiracUSBVendor`), **external-DAC** support (`ExternalDAC`).

---

## 9. Identity, security & PKI

**Three baked-in identities** (what make it a *certified* multi-protocol device):

1. **LibreWireless device cert** — `/factory/libre/luci/deviceCert.pem`: subject
   `CN=LibreLS8ARYLLP10 RKARYLLP10<redacted> 2232`, issuer `O=Libre Wireless Technologies,
   CN=LWT Root CA` (Bengaluru, IN), valid 2025→2045. Per-device.
2. **Google Cast cert** — `/factory/client.crt`: issuer `RAKOIT TECHNOLOGY(SZ) … OU=Cast,
   CN=LP10` (Shenzhen). (`/factory/custom/ckeys/model.crt` is the un-provisioned template with
   literal `<UNIQUE HARDWARE ID>` placeholders.)
3. **Spotify OEM identity** — `SpotifyClientId`/`SpotifyProductID` in env.

**Secure silicon:** OP-TEE (`/dev/tee*`, `tee-supplicant`, `optee`), `secmon`, **eFuse** key
store + `unifykey`, HW crypto DMA + RNG. `S59provision_key_inject` injects keys at boot.

**Security posture (LAN management surface is wide):**
- **Telnet (root) :23**, **SSH (root) :22**, **network ADB :5555** — all reachable on the LAN;
  root login enabled (`/etc/passwd`: `root:…:/bin/sh`).
- Web UI creds in env (`Defaultwebuserpassword`, `Newweb*`).
- `androidboot.selinux=enforcing` is in the cmdline but **SELinux is not active** (no
  `selinuxfs`) — inherited Android boilerplate.
- Secret env values **never read** (key names only): `SP_BLOB`/`SP_USERNAME`,
  `AlexaRefreshToken`, `Airable{Auth,Secret}`, `*AppSecret`, `*Password`, Wi-Fi PSK.

> Fine as a trusted-LAN appliance; the open root telnet/ADB/SSH would be a real exposure if
> ever bridged or port-forwarded.

---

## 10. Cloud, OTA & telemetry

### 10.1 OTA — the daemon, the manifest API and the bundle

**Path:** `ota` daemon (libcurl) → **SWUpdate**; versions in `/etc/fwVersion.conf`; offline fallback via USB
(`/media/usb/software.swu`); the web ASP exposes `GetOTAFWVersion` / `GetOTAUpdate`. **Manifest URL** from env
`fwdownload_xml`: **`https://lp10.arylic.rakoit-ota.com/v1`** since 8530 (`https://lp10-ota.rakoit.com/v1` before — both
still answer identically).

**Cadence (syslog, 2026-09-12).** The box checks on its own **every 4 h**, on a timer started at boot
(`CheckInternetUpgradeOnTimeOut Periodic OTA trigger`; 02:56 / 06:56 / … local this boot), logging the playback state it
would have to interrupt (`Paused`). It posts
`{"device":{"brand":"Arylic","buildInfo":"AR241CE","castVersion":"0.0.0","custVersion":"2","deviceId":"<eth MAC>","fwVersion":"AR241CE_8530",…}}`
with "Mutual Authentication" enabled on its side, gets `No update available`, and the MCU then reports `NO_UPDATE` on
MsgBox 223 (98 such reports in `/lsync/app.log` between 2026-08-25 and 09-12). It also warns `GoogleCast is set to False
on a CAST product` on every check. Related env, all new in 8530: `OtaUpdateSchedule`, `OtaSkipCount`, `OtaSkipEnableMask`,
`OtaAckTimeout`, `OtaStandbyUpdate=0`.

**Manifest API (probed read-only 2026-07-01, re-probed 09-02 and 09-12).** `/v1` is a **Rust/axum + serde** JSON
endpoint — **`POST`** only (`GET` → 405), `Content-Type: application/json`, body
`{"device":{"brand","deviceId","fwVersion","model", …}}` (extra keys ignored; a synthetic `deviceId` is accepted — **no
device auth**; the client's mTLS is not required). Up to date → `{"errorCode":1001,"errorString":"No update available"}`;
an **older `fwVersion`** → `{"errorCode":1000,"errorString":"SUCCESS","url":"…","version":"AR241CE_8530","otapackage":"…",
"castVersion":"0.0.0","mcuOnlyUpdate":false}`. **As of 2026-09-12 the newest build the vendor has is 8530.**

**The bundle is public and unauthenticated** — `https://cdn.rakoit-ota.com/lp10/` (Cloudflare CDN, `accept-ranges: bytes`):

| Build | File | Size | Last-Modified | `mcu.bin` — outer-file offset · size · sha256 |
|---|---|---|---|---|
| `AR241CE_9243` / MCU 16 | `LP10_AR241CE_9243_16.swu` | 86,704,128 B | (still served) | 13,617,516 · 856,203 B · `04af7091…` |
| `AR241CE_8530` / MCU 23 | `lp10_AR241CE_8530_23_ca8e6abc.swu` | 89,751,552 B | 2026-08-20 07:55 GMT · etag `6a86b304-5598000` | 13,617,636 · 872,403 B · `d98284c4…` |

Naming moved to lower-case plus a content-hash suffix with 8530. Shape (both): a **double-wrapped cpio** — outer `070701`
cpio → single `software.swu` → inner `070702` (newc+CRC) cpio holding `sw-description` (+`.sig`) and the images.
`sw-description` (libconfig; 8530: `fwversion=AR241CE_8530`, `mcuversion=23`, `forceupdate=true`, `custversion=1`) lists
`rootfs.squashfs`→mtd8, `boot.img.encrypt`→mtd6, `dtb.img.encrypt`, `factory_customer.squashfs`→mtd7, **`mcu.bin`
(type=`mcu`)** and the script `update.sh` (byte-identical across builds; a generic rootfs swap). `mcu.bin` is
**unencrypted** and pullable with a single `Range` request (§10.3); the `type=mcu` handler streams it verbatim to the MCU
over the UART via XMODEM (§6.1).

### 10.2 The vendor app loader and `rakoit_app`

`/factory/custom/csys/bin/daemon` (signed, verified by `S99zcustomapp` against
`/etc/swupdate-public.pem`; rewritten by the 8530 OTA) is a curl-based **"rak-loader"**: reads an index from
`https://cdn.rakoit-ota.com/download` (fallback `ota.rakoit.com/download`), downloads to a
`mkstemp` file, **verifies the binary before install** ("binary verify failed"), kills and
restarts the app. `/lsync/daemon.ini` is empty (URL default). `/lsync/app-0.json` records
`rakoit_app` version **32**, md5 `9aa7f360…`, installed 2026-08-25 — i.e. the app updates on its
own schedule, independent of the OTA. As of 2026-09-12 nothing newer has been installed; the index URL answers
404 / 301 to a bare GET, so the exact index path is still unlocated.

`rakoit_app` (6,532,124 B, static ARM Rust, stripped; `rustc 1.94.0`, tokio 1.47, hyper 1.7,
reqwest 0.12 + rustls 0.23, redb 2.6.3, netlink/pnet, clap; built through `rsproxy.cn`) is the
**"source service"**:

- **Presets / favourites** — the new feature. The MCU forwards the remote's keys as `KEY:FAV`,
  `KEY:NUM%d`, `KEY:RESET` (replacing the old `FAV_SAVE:%d`/`FAV_DELETE:%d` strings); the app
  saves the current Spotify/Qobuz/radio item into one of 20 `GEN_FAV_n` slots (`[spotify-preset]
  FAV_SAVE/FAV_PLAY`, `[presets] Save slot=`), and publishes the OLED **PlayView** itself
  (`[preset-current] PlayView set_play_view`).
- **PlaylistServer on tcp 2345** (`playlist_addr = "127.0.0.1:2345"` in its config, but bound
  `0.0.0.0`) — a DIDL/UPnP-style playlist source (`urn:playlist:namespaces:metadata-1-0/`,
  `arylic:playListId`). Silent to a bare HTTP GET.
- Providers: TuneIn (`opml.tunein.com`, `api.radiotime.com`), radio-browser
  (`de1.api.radio-browser.info`), Qobuz `api.json/0.2`, `api.sound-machine.com`.
- Local clients: LUCI on `127.0.0.1:7777` **and the :2018 tunnel** (`127.0.0.1` ↔ `:2018` in
  `/proc/net/tcp`). It logs every `MB#112` frame and every MCU reply (`接收到TCP消息: BAS:0;`) to
  **`/lsync/app.log`** — the tunnel traffic is now observable from the SoC side.
- State: `/lsync/source_service/{credentials.redb, secrets.json.enc, session_store,
  playback_facts.sock}`; optional config `/lsync/source_service.toml` (absent → defaults);
  `/lsync/cache.redb` for art/now-playing. `credentials.redb` is `0600` — **never dump it**.

### 10.3 `mcu.bin` — the MCU firmware, reversed (v16 on 2026-07-01, v23 on 2026-09-02)

**The image.** An **MVSilicon "AP82"** BT-audio-SoC image (magic `MV\xb1X`, SDK banner `0.5.1 build @ Nov 9 2021`,
`BT_Audio_APP/bt_audio_app_src/…` tree), driving an **SSD1322** OLED controller via `display/disp_a200.c` +
`disp_device.c`, and it **speaks `LUCI`** (builds the MsgBox-770 `{"name":"mcu","version":…,"type":"xmodem"}` JSON).
Unencrypted (no `.encrypt` suffix, entropy 7.35 = code, plaintext strings). **The whole panel renderer and the host LUCI
parser live in this image.**

**CPU = C-SKY** (little-endian, 16-bit; MVSilicon **BP10xx**, per `…/rakoit/platform_bp10xx.c`).
Confirmed by disassembly: radare2's `mcore` backend yields coherent control flow at code regions
(`bsr`/`br`/`movi`/`jmpi`/`ld.h` with valid targets) where `sh`/`nds32` produce garbage; CPU-fault
strings `Privileged/Reserved instruction` corroborate. The image is a flat `MV\xb1X` blob (code
~`0x10000-0x3ffff` + others, rodata ~`0xc000-0x76000`, a symbol/reloc table at `0x80000`, an
opaque blob at `0xc0000`). No off-the-shelf disassembler is fully accurate for this C-SKY variant
(r2's M-CORE is only ~partial), so the finding below is **data-driven** (strings/constants), not
full instruction trace.

**Result — the panel's host interface is `PlayView`-only, MCU-side confirmed.** The MCU's own JSON
parser (key table clustered at the `PlayView` string) recognises exactly one host view —
`Title:"PlayView"` with `TrackName/Artist/Album/TotalTime/BitDepth/SampleRate/repeat/shuffle`. There
is **no** `Message`/`Text`/`Popup`/`Notification`/`Dialog` view string anywhere in the firmware, and
`716`/`717` occur as **neither strings nor 32-bit constants** (present ones: `42`,`207`,`770`, …).
So the MCU has no host-driven full-screen-text path; its non-now-playing screens
(source/volume/standby/mute) are rendered from **internal MCU state**, not from a host command. This
**confirms `MID 716` unlocks nothing on this device** and closes §6.4's ceiling at the hardware
layer. `disp_device.c` is a **generic multi-panel driver** (supports `GY12832`/`QG2832`/`ZJY223`/
`JHD19264`/`SSD1322` modules; LP10 ships a 128×32 OLED). *To actually add a new full-screen view you
would have to patch + re-sign `mcu.bin` and reflash the MCU — a firmware mod, not a host push.*

**What v23 added (string-level diff against v16, 2026-09-02).** Same MVSilicon SDK build banner (`0.5.1 build @ Nov 9 2021`), same `MV\xb1X` header, same
**102 + 21-entry command-code table** (byte-for-byte the §6.3 vocabulary), same **`PEQ`** list,
and **`PlayView` is still the only host view** — no `Message`/`Text`/`Popup` string appeared, so
§6.4's ceiling stands. What v23 adds (strings):

- Build id `4ef47210` (surfaces as `VER:23-4ef47210-9`).
- An **audio-visualisation module** (`audio_visualization*.c`: `VUMeter`, `Vumeter2`,
  `SpectrumMeter`, `Energy`, `Belt Energy`, `Belt Mix`, `ColorFlow`, `Custom`, "light data",
  `smooth:`) plus `rakoit/bwee.c` and `device/zb06.c` — an LED-strip / light-effect accessory
  driver. The LP10 has no such hardware; this is the shared Arylic MCU image growing for other
  products.
- Remote-key and status reports to the host: `KEY:FAV` / `KEY:NUM%d` / `KEY:RESET`,
  `GENERIC_FAV_DELETE:%d`, `VOLUMEMODE:FIXED`, `OUTPUT:%s` (the values `analog` / `digital` /
  `analog_digital` sit next to it — inferred, not traced), `AMPTYPE:%s` (with `integrated` / `speakers` / `header` appearing beside it — pairing inferred from adjacency, not traced), `WIRELESSOUTPUT:bttx`,
  `DSPEQ:%s`, `READY:%d`, `SAMPLERATE:%d`, `NETWORK`, `NO_UPDATE`, `BASS/MIDDLE/TREBLE/BALANCE:%d`,
  `set pregain: %d.%d dB`. The speaker-test sample moved to `/factory/custom/SpeakerTest.wav`
  (was `http://127.0.0.1/SpeakerTest.wav`). None of these are new *tunnel commands* — the code
  table did not change — they are MCU→host notifications.

### 10.4 Telemetry & remote relay

**Telemetry / phone-home (destinations only — no payloads read):**
- `logs.librewireless.com` — diagnostic log upload (`crash_uploader`/`stdlogctl`).
- `clients2.google.com` — crash dumps via Google **Breakpad** (`crash_uploader`).
- `ota.rakoit.com` / `lp10-ota.rakoit.com` / `lp10.arylic.rakoit-ota.com` (8530) — update checks; `cdn.rakoit-ota.com/download` — the app loader's index (8530).
- Per-service: Spotify eSDK stats, TuneIn `report.core-api`, Cast usage report (off).
- `www.digicert.com` (TLS OCSP/CRL) · `www.arylic.com` (vendor links).

**Remote relay:** `tcptunnelling` (local :2018, live loopback client) is the path that lets
the Arylic/4STREAM app reach the device from outside the LAN; no hardcoded relay host surfaced
in strings. The **same :2018**, addressed locally, is also the plain-text **tone/EQ/max-volume
control protocol** (§6.3) — one port, two roles. **Vendor daemon:** `/factory/custom/csys/bin/daemon` (signed) is the app loader of §10.2
(on 9243 it was a simpler glue that read a `daemon.ini` URL, "PROTOCOL v1.0", and reported fw/version).

---

## 11. Lifecycle gotchas

- **Spotify session is network-event-driven, not boot-driven** (§8.1) — any link blip rebuilds it.
- **Wi-Fi wedge** (§7) — root cause of past stream drops; mitigated by wired eth0.
- **Volume mute** restores the wrong level — `lp10` does mute = set 0 + restore (§6).
- **Panel *content*** (now-playing text, MID 42) can be pushed by the host with a **`-remote`**
  write (§6.4); a plain `lp10`/app MID write is local-only and never reaches the MCU. **Volume is
  different** — the MCU *owns* it, so a host `-remote 64` is **inert**: the on-screen **volume bar
  only pops for the physical buttons/remote**, and lp10's plain volume write is audible (SoC/DSP)
  but leaves the remote/panel on a separate, stale value (§6).
- **eth0 lease is dynamic** — discover via mDNS (`<device>.local`, or the `_spotify-connect._tcp` instance named after the
  FriendlyName) / LSSDP / the powerline MAC; don't hardcode.
- **Qobuz Connect intentionally OFF** — an OTA could flip `QobuzConnectEnabled`; re-check after
  firmware updates.
- **Both Spotify flags set = no Spotify at all** (§8.1) — the state an OTA's factory flip can leave behind; always write
  the pair.
- **A `setenv` sticks** (a dirty row, §5) — through reboots and, on the 8530 precedent, through the next OTA's factory
  flip; a flag you never touched follows the factory.
- **Power loss leaves almost no trace** — `reboot_mode=cold_boot`, everything under `/tmp` re-stamped, syslog gone
  (`/tmp/syslog` is tmpfs; ~23 h retained). `/lsync/app.log` is the one log that survives a boot.
- **Dropbear stalls the ~5th ssh connection made within a few minutes** — batch reads into one session; `lp10`'s record
  loop holds a single connection for exactly this reason.
- **One ZeroConf port per engine** — never cache 9095 / 9096; read the `_spotify-connect._tcp` SRV record.
- **Who started this daemon?** `/proc/<pid>/environ` of an init-started process has no `SSH_*`; one launched from an ssh
  session (a services-pane toggle) carries `SSH_CLIENT` / `SSH_CONNECTION` — how the 2026-09-04 engine switch was traced.

---

## 12. Official specs (Arylic) & reconciliation

From Arylic's product page (the published spec sheet), with **how this teardown confirms or
differs**:

| Spec | Arylic says | This teardown found |
|---|---|---|
| Category | AirPlay 2 & Google Cast music streamer | ✓ network audio streamer, line-level (no amp) |
| Dimensions / weight | 108 × 72 × 26.6 mm / 500 g | (not measured) |
| Display | 0.91″ OLED | ✓ 128×32 mono **blue** OLED, **MCU-rendered** |
| Controls | 4 touch buttons + IR remote | ✓ `gpio_keypad`/`adc_keypad` + `input_btrcu`; **no rotary** (`rotary-encoder` DT disabled) |
| Wi-Fi | Dual-band 802.11ac 2.4/5 GHz | ✓ both bands live (phy0/phy1, VHT ≈390 Mbps) |
| Bluetooth | 5.0 | ✓ HCI/LMP 5.0 (BlueZ) |
| Ethernet | 10/100M RJ45 | ✓ `eth0` |
| Analog in / out | 3.5 mm, 1 Vrms each | ✓ WM8904 ADC (in) / DAC (out) |
| Optical out | up to 24-bit/192 kHz | ✓ SoC S/PDIF DAI → TOSLINK |
| USB-A | flash playback (≤1000 songs / 16 GB) | ✓ host port |
| USB-C | power **+ PC audio** | ✓ 5V/2A + RNDIS/ADB gadget; the "PC audio" UAC path wasn't separately confirmed (`snd_usb_audio` not loaded) |
| Streaming | Cast / AirPlay 2 / Tidal / Spotify / Qobuz / DLNA | all present; **enabled now: Spotify, AirPlay 2, DLNA, BT**. Cast/Tidal/Qobuz/Roon/Alexa/Matter/QPlay installed but **off** (env-gated) |
| Power | USB-C 5 V/2 A | ✓ |
| In box | unit, manual, 3.5 mm aux cable, 2-to-1 RCA cable, PSU, USB-C cable, IR remote | — |

The interesting *deltas* are all "more than advertised": a fuller LibreWireless protocol set
(Roon/Alexa/Matter/QPlay) shipped but mostly disabled, the MCU-driven OLED + host-UI protocol,
the dual PKI, and the wide root LAN management surface (§9).

---

## 13. Build provenance

Daemons built from LibreWireless's tree:
`…/buildroot-openlinux-2024r1-a113l-ad403-spk/output/libre_ls8_24G_v1_c4a_debug_release/build/
{spotify_hifi, libre_airplay_v2/AirPlaySDK, roon/raat-master, …}`. Platform **LS8**, buildroot
**openlinux 2024r1**, SoC **a113l-ad403** "spk", target **EVK**. Kernel 5.15.137
(`arm-none-linux-gnueabihf-gcc 10.3.1`, built 2025-12-24; the 8530 kernel is the same 5.15.137 rebuilt
2026-01-12 18:55 IST, Buildroot `2020.02.1-svn317`, build tree `QA_validation` instead of `Dev_validation`).
Spotify eSDK `v3.203.239` (HiFi) / `v3.211.130` (Pro, 8530).

---

## 14. Firmware history

| When | Event | Firmware / MCU | Where |
|---|---|---|---|
| ≤ 2026-06 | the box as bought; June teardown | `AR241CE_9243.16.2` / **16** (`build_date 2025-12-24`, svn 312) | §14.1 |
| 2026-08-2x | the vendor OTA taken (bundle on the CDN 2026-08-20) | **`AR241CE_8530.23.2` / 23** (`2026-01-12`, svn 318) | §14.2 |
| 2026-08-25 | `rakoit_app` **v32** installed by its loader; box rebooted ≈16:36 | — | §10.2 |
| 2026-09-04 14:55 | **power loss** (cold boot); 15:06 Spotify switched to the **Pro** engine from `lp10` | — | §14.3 |
| 2026-09-12 | re-sweep — **no newer OTA**; manifest, CDN and loader unchanged | — | §14.3 |

### 14.1 AR241CE_9243.16.2 — the June 2026 baseline

What the June teardown recorded that later moved: MCU **v16**; Spotify **HiFi** engine on ZeroConf **9095**, Pro engine at
eSDK **3.205.187**; factory `SpotifyEnabled=1 / SpotifyProEnabled=0`; the manifest at **`lp10-ota.rakoit.com/v1`**;
`WACServer` bound tcp **5000**; the vendor `csys/bin/daemon` a simple "reads a `daemon.ini` URL, PROTOCOL v1.0" glue
(no loader records — `rakoit_app`'s version then is unknown); 50 Cast locale `.pak` files in the rootfs. Everything else
in chapters 1–13 was true then and is true now.

### 14.2 AR241CE_8530.23.2 — the August-2026 OTA (re-analysed 2026-09-02)

The box took the vendor OTA in August 2026 (bundle dated 2026-08-20 on the CDN; the app loader
then installed `rakoit_app` v32 on 2026-08-25). Everything here is from a **file-by-file diff of
the two public bundles**, string-level reverse of the two `mcu.bin` images, the vendor Rust app
pulled off the box, and a live read-only pass. **The device runs exactly the bundle's files** —
sha256 of `luciserver`, `tcptunnelling`, both Spotify engines, `airplaydemo`, the vendor
`daemon` and `factoryEnv.conf` all match the CDN image.

**Identity now:** `AR241CE_8530.23.2` · MCU **23** (`VER:23-4ef47210-9`) · `build_date=2026-01-12` ·
`app_svn_version=318` · kernel 5.15.137 rebuilt 2026-01-12 · Buildroot `2020.02.1-svn317`. The
manifest server says 8530 is current (2026-09-02). Live uptime 7 d → last boot 2026-08-25 ≈16:36.

#### The bundle

- `https://cdn.rakoit-ota.com/lp10/lp10_AR241CE_8530_23_ca8e6abc.swu` — 89,751,552 B,
  `Last-Modified: 2026-08-20`. Naming changed to lower-case + a content-hash suffix. The old
  `LP10_AR241CE_9243_16.swu` (86,704,128 B) is still served, which is what made a full diff possible.
- Same shape: outer cpio (`070701`) → `software.swu` → inner cpio (`070702`, newc+CRC) with the same
  five images + `update.sh` (**byte-identical script**). `sw-description`: `fwversion=AR241CE_8530`,
  `mcuversion=23`, `forceupdate=true`, `custversion=1`; all five image hashes changed.
- `mcu.bin`: **872,403 B** (+16,200), sha256 `d98284c45b1fad6485a651db93e72e81afdf970fdb146c6baba810d77413329f`,
  at outer-file byte offset **13,617,636** (unchanged) — one Range request still pulls it.
- Manifest API: `factoryEnv.conf` now points `fwdownload_xml` at **`https://lp10.arylic.rakoit-ota.com/v1`**;
  the old `lp10-ota.rakoit.com/v1` still answers (same axum/serde shape, still unauthenticated).

#### What changed in the rootfs (2,651 → 2,601 files; 327 differ by content)

A whole-tree rebuild — every kernel module, busybox, util-linux, avahi, bluez, dbus, and every
daemon has a new binary — but the **functional** text diffs are few:

- **Cast:** the 50 `chromecast_locales/*.pak` files dropped; `S84librecast_lite` refactored
  (config split into the new `/etc/libre_castlite`; `FriendlyName` read from MB#90 with retries;
  an **overlay mount over `/etc/alsa/conf.d` + `/usr/share/alsa`** for `default`/`fixed48k`/`fixed96k`;
  unique IDs from `$netif_id` instead of a hardcoded `wlan0`; sample rate from the new `outputfs` env,
  live `44100,48000,64000,88200,96000,176400,192000`). `cast_lite_aml.conf` gains log rotation.
- **Qobuz:** `S99qobuzConnect` now starts `avahi-daemon` **only when `QobuzConnectEnabled=1`** and
  kills it on stop (it used to leave Avahi alone).
- **New `libspdif.so`** — Android's `SPDIFDecoder`/`SPDIFFrameScanner` (encoded-audio burst
  handling for the S/PDIF path). Not yet referenced by any init script.
- **`S89usbgadget.rej`** — a stray svn patch-reject file shipped in `/etc/init.d` (harmless; it
  shows the vendor meant to uncomment `usb_net_ipconfig` at r288).
- **`/etc/shadow`:** the root hash was re-salted (`$5$`) — **same password** (checked locally with
  `openssl passwd -5` against both hashes; never on the box).
- **`factory_customer.squashfs`:** `csys/bin/daemon` rewritten (below) and `factoryEnv.conf` changed —
  `SpotifyEnabled` **1→0**, `SpotifyProEnabled` **0→1** (the flip behind §8.1's dual-flag bug),
  `USBDisplaySongs` 1→0, new keys `TidalGain=1.0`, `bleencryptiontype=1`, `mcuusblist`,
  `OtaStandbyUpdate=0`, `NetworkAlerts=0`, `USBFS=vfat,exfat,ntfs,ext4`, `netcontrolmask=7`,
  `CustomVolControl=0`, `opresamplelist`, `AutoStandbyTime`, `MaxAuthTimeout`. The user DB also
  now carries `GEN_FAV_0…19` (all empty) and `MCUVersion=23`.
- **Unchanged (verified):** `luciserver` (the only diff is a dropped MAC-lookup helper — the MsgBox
  table of §6.5 stands), `tcptunnelling` (build-path strings only), the ALSA control set (same 94
  controls; the WM8904 is still declared and still absent from I2C), the PCM/DAI map, the MTD
  layout, `update.sh`, mDNS names (`fv=p20.AR241CE_8530.23.2` in the AirPlay TXT).

#### Spotify

- HiFi engine `newspotifyhifi` / `libspotifyhifi.so`: **eSDK 3.203.239-g1d6bd565 — unchanged.**
- Pro engine `spotifymusicpro` / `libspotifypro.so`: **3.205.187-g0b65d3e0 → 3.211.130-g110e3e03.**
- ZeroConf endpoint moved **9095 → 9096** (mDNS SRV + `GET /zc?action=getInfo` → `version 2.9.0`,
  `libraryVersion 3.203.239-g1d6bd565`, `deviceType SPEAKER`, `modelDisplayName LP10`).
- Live on 2026-09-02: HiFi running, `SpotifyEnabled=1 / SpotifyProEnabled=0` (the user-pinned pair). Whether the pin
  would survive a reboot was left open here; §5 (2026-09-12) closes it — it does.

#### The vendor app and the MCU

The rewritten loader and `rakoit_app` v32 are described in §10.2; the MCU v23 image (same command table, `PlayView`
still the only host view, the light-effect module and the new MCU→host notifications) in §10.3.

#### Live surface (2026-09-02)

tcp **22 23 80 2018 2345 5037**(lo) **5555 7000 7777 9096 43761**(dynamic, HTTP 404 — `rakoit_app`'s second listener; 33719 after the 2026-09-04 boot, §7) **49494**;
udp **68 123 1800 1900 3721 5353** + dynamic. `WACServer` runs but no longer holds tcp 5000.
LSSDP (udp 1800) now answers `USN:d8f710710ad6` (the wlan MAC, no colons), `TCPPORT:2020`,
`SPEAKERTYPE:Wireless Speaker`, `CAST_MODEL:LP10`. `:2018` values: `MXV:100 EQE:0 EQS:0 BAS:0
MID:0 TRE:0 VBS:0 VBI:50 BAL:0` (§6.3 has the rest).

**Net effect on `lp10`:** no behavioural change was needed — only the version pins, fixtures and
docs. The three things worth remembering: the Spotify flag flip (the services pane's raison
d'être), the per-engine ZeroConf port, and "one tunnel connection at a time".

---

### 14.3 Re-sweep 2026-09-12 — no newer OTA; a power-cycle and an engine switch

Prompted by a suspicion that the box had taken another OTA. **It had not.** One ssh-free pass (mDNS, LSSDP, Spotify
ZeroConf, the vendor manifest and CDN) plus seven short **read-only** ssh passes. Nothing was written to the device; the
two env sqlite files were copied off for a schema read and deleted afterwards. What it settled lives where it belongs:
the env store in §5, the current listener map in §7, the OTA cadence and payload in §10.1, the engine table in §8.1.

#### Identity — unchanged

- `/etc/fwVersion.conf`: `AR241CE_8530` · `build_date 2026-01-12` · `app_svn_version 318`. MCU **23**
  (MsgBox 770 → `{"name":"mcu","version":"23","type":"xmodem"}`; tunnel `VER:23-4ef47210-9`). Kernel 5.15.137
  built 2026-01-12 18:55 IST. AirPlay TXT `fv=p20.AR241CE_8530.23.2`; LSSDP `FWVERSION:AR241CE_8530.23.2`.
- The rootfs is the read-only squashfs on mtd8, so an unchanged version string means the 8530 bundle by
  construction. The mutable pieces were re-hashed anyway (sha256 prefixes): `luciserver 465c90d4…` ·
  `tcptunnelling 21776ba9…` · `newspotifyhifi 80a783a4…` · `spotifymusicpro 26e2f5a4…` · `airplaydemo c63a3264…` ·
  `csys/bin/daemon 7da813d3…` · `factoryEnv.conf f11a5e69…` (both `/factory/custom` files still dated 2026-08-19).
- Vendor app: `/lsync/app-0.json` still `rakoit_app` **v32**, md5 `9aa7f360…`, binary dated 2026-08-25 — the
  loader has installed nothing since. Its index URL (`cdn.rakoit-ota.com/download`, fallback
  `ota.rakoit.com/download`) answers 404 / 301 to a bare GET; the exact index path is still unlocated.
- Manifest server (`lp10.arylic.rakoit-ota.com/v1`, and the old host identically): `fwVersion=AR241CE_8530` →
  `{"errorCode":1001,"errorString":"No update available"}`; `AR241CE_9243` → still offered
  `lp10_AR241CE_8530_23_ca8e6abc.swu` (`version AR241CE_8530`, `mcuOnlyUpdate:false`). CDN `HEAD`: the same
  89,751,552 B, `Last-Modified 2026-08-20 07:55:48 GMT`, etag `6a86b304-5598000`. **8530 is the newest build the
  vendor has.**
- The box asks on its own every **4 h** — `ota`: `CheckInternetUpgradeOnTimeOut Periodic OTA trigger`, at
  boot-time + 4 h multiples (02:56 / 06:56 / … local this boot). It posts
  `{"device":{"brand":"Arylic","buildInfo":"AR241CE","castVersion":"0.0.0","custVersion":"2","deviceId":"<eth MAC>",…}}`
  ("Mutual Authentication" enabled on its side), gets `No update available`, and the MCU then reports `NO_UPDATE`
  on MsgBox 223 (98 such reports in `/lsync/app.log` since 2026-08-25). The `ota` log also warns
  `GoogleCast is set to False on a CAST product` on every check.

#### What actually happened

- **Cold boot 2026-09-04 14:55:42 -03** (`/proc/stat btime 1788544542`; kernel cmdline `reboot_mode=cold_boot`,
  i.e. power loss rather than a software reboot; uptime 7 d 9 h at the sweep). Everything under `/tmp`,
  `/lsync/source_service` and `cache.redb` is stamped 14:56.
- **Spotify now runs the Pro engine.** `SpotifyEnabled=0 / SpotifyProEnabled=1`; `spotifymusicpro` (pid 5673)
  started **15:06:57**, 11 min after the boot, and its `/proc/<pid>/environ` still carries
  `SSH_CLIENT=<laptop-ip> 51411 22` — it was launched from an ssh session (dropbear's `secure` log has that
  session: `<laptop-ip>:51411`, 15:02:55 → 15:07:29; the 15:02:28 one before it was the first ssh of that boot,
  where dropbear generated the ramfs host key). The env writes are byte-for-byte the services pane's "new
  engine" action (`killall` both engines, `setenv` `0/1`, kick `S99spotifymusicpro netready`). **Not an OTA and
  not a boot side-effect — a deliberate switch from `lp10`.** A daemon started by an init script would carry no
  `SSH_*` in its environment; this is a cheap forensic for "who started this process".
- The switch moved the ZeroConf SRV back to **9095**: `getInfo` → `version 2.10.0` (HiFi answers 2.9.0),
  `libraryVersion 3.211.130-g110e3e03`, `deviceType SPEAKER`, `remoteName Living`, `tokenType accesstoken`,
  `supported_drm_media_formats` × 2 (`formats 65606`). So on 8530 the port is *per engine* (HiFi 9096, Pro 9095) —
  the reason `lp10` takes it from the SRV record every time and never remembers it.
- The Pro engine **drops and re-establishes its Spotify connection often**: 54 × `The connection to Spotify has
  been lost` (eSDK `ConnectionNotify = 3`) in the 23 h of syslog retained, each followed by a re-login with the
  stored credentials (`ConnectionNewCredentials`, 53 ×) — irregular, 1 min to 1.5 h apart — while the DLNA
  subscription renewals and ssh sessions over the same link stay up. Whether HiFi did the same is unknown (its
  syslog is gone); noted, not diagnosed.

---

## 15. Verify it yourself (read-only)

Host `<device>.local` (or the current eth0 lease; the box's mDNS instance name is its FriendlyName, so `dns-sd -B
_spotify-connect._tcp local.` finds it when the hostname does not resolve). Root password in macOS Keychain (service
`lp10`, account `root`); use `SSH_ASKPASS` (one-shot, no TTY needed). **Never write to the device fs; never touch playback
state** (it may be in use). Batch reads into one ssh session and space sessions a minute apart — dropbear stalls the
~5th rapid connection (§11).

```sh
# one-shot read-only SSH (password from Keychain via askpass)
cat > /tmp/askpass.sh <<'EOF'
#!/bin/sh
exec security find-generic-password -s lp10 -a root -w
EOF
chmod +x /tmp/askpass.sh
sshx(){ SSH_ASKPASS=/tmp/askpass.sh SSH_ASKPASS_REQUIRE=force DISPLAY=:0 \
  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  root@<device>.local "$@" </dev/null; }

sshx 'cat /proc/cpuinfo; cat /etc/fwVersion.conf'              # hardware + firmware
sshx 'cat /proc/asound/pcm; aplay -l; arecord -l'             # audio DAIs / capture devices
sshx 'amixer -c0 scontrols'                                   # mixer (WM8904 + AED) — READ only
sshx "for a in 0-001a 0-004a; do cat /sys/bus/i2c/devices/\$a/name; done"   # WM8904 + PCM1863
sshx 'iw phy | grep -E "Band [0-9]:|[0-9]{4} MHz" | head'     # Wi-Fi bands (dual-band)
sshx 'hciconfig hci0 version'                                 # Bluetooth (5.0)
sshx 'netstat -tln; netstat -uln'                             # listening ports
sshx 'LUCI_local -a'                                          # full MsgBox state (read)
sshx 'getenv SpotifyEnabled; getenv SpotifyProEnabled; getenv TidalEnabled'   # the engine pair (§8.1)
sshx "sqlite3 /data/libre/env/env.db \"select key,value,dbit from ENV_systemENV where key like 'Spotify%Enabled'\""  # §5 — key names + these two values only
sshx 'ls -l /proc/$(pidof spotifymusicpro || pidof newspotifyhifi)/fd; cat /proc/net/tcp'   # Spotify sockets
sshx 'tr "\0" "\n" < /proc/$(pidof spotifymusicpro)/environ | grep ^SSH_'   # empty = started by init (§11)
sshx 'cat /proc/stat | grep btime; cat /proc/cmdline'          # last boot + reboot_mode
sshx 'grep -h "Periodic OTA trigger\|error string" /tmp/syslog/messages.log | tail'   # the box's own 4-hourly check (§10.1)
sshx 'openssl x509 -in /factory/libre/luci/deviceCert.pem -noout -subject -issuer'
# env KEY NAMES only — never dump values (SP_BLOB / *Secret / *Password / PSK):
sshx "sqlite3 /data/libre/env/env.db 'select key from ENV_systemENV order by key'"
```

**Without ssh** (what `lp10` does, Appendix B): `dns-sd -L <device-name> _airplay._tcp local.` (TXT `fv=` is the
firmware); `dns-sd -L <device-name> _spotify-connect._tcp local.` then `curl http://<ip>:<port>/zc?action=getInfo`
(engine + eSDK build); a unicast SSDP `M-SEARCH` to udp 1800 (LSSDP, §7); a bare `CODE;` query on tcp 2018 over one
held connection (§6.3); and the manifest — `curl -X POST -H 'Content-Type: application/json' -d
'{"device":{"brand":"Arylic","deviceId":"000000000000","fwVersion":"AR241CE_8530","model":"LP10"}}'
https://lp10.arylic.rakoit-ota.com/v1` (§10.1).

> **Hygiene, learnt the hard way.** `grep -i spotify` over `/tmp/syslog/messages.log` returns the engine's
> `SAME USERNAME IS THERE STORE THE BLOB …` line — the reusable login blob and the account id in clear: filter
> `BLOB|username` *before* anything leaves the box. `/lsync/app.log` is partly Chinese — read it with `LC_ALL=C` on the
> Mac side or `sed` aborts. And the env DB holds the Wi-Fi PSK and the web password beside the flags — select key names,
> never `*`.

---

## Appendix A — Env capability keys (names only)

> Feature flags & identity in the live store `/data/libre/env/env.db` (§5; `/lsync/env/env_bk.db` is the first-boot
> snapshot). **Values omitted** (several are secrets).

- **Identity:** `Brand`(Arylic) `Manufacturer` `ModelName`(LP10) `ModelVariant` `ProductID`
  `BrandID` `AppMacId` `Country`(US) `FactoryLocale`(en) `TimeZone` `FwVersion` `MCUVersion`
  `Hardware_version` `HardwarePlatform`
- **Spotify:** `SpotifyEnabled` `SpotifyProEnabled` `SpotifyClientId` `SpotifyProductID`
  `SpotifySpeakerType` `SP_BLOB` `SP_USERNAME`
- **Tidal/Qobuz:** `TidalEnabled` `TidalClientId` `QobuzConnectEnabled` `QobuzConnect_AppId`
  `QobuzConnect_AppSecret` `QobuzConnect_HttpPort`
- **Roon:** `RoonEnable` `RoonOutputType` `RoonMQACapabilities` `RoonDOPEnable`
- **Cast:** `GoogleCast` `CastSetup` `CastTOS` `CastUsageReport` `CastSSIDSuffix`
  `CastAlsaOutput*` `GCASTVersion`
- **AirPlay/Alexa:** `AirPlayHomeAccessControlEnabled` `AirPlayAppleHomePassword`
  `AirPlayMetaData` `AVSEnabled` `AlexaClientID` `AlexaProductID` `AlexaRefreshToken`
  `AlexaEndpointURL`
- **Airable/TuneIn:** `AirableEnable` `AirableAuth` `AirableSecret` `AirableBaseURL`
  `TuneInPartnerID` `TuneInPartnerSecret`
- **Home/IoT:** `MatterEnabled` `IoTEnabled` `IoTEndpointURL` `CVMKey` `SddpEnable`
  `SDDP{ConfigURL,Driver,Type,Version}` `QPlay_{DID,HashKey,MID}`
- **Media/USB/audio:** `DMREnable` `DMPEnable` `USBEnable` `USBDisplaySongs` `ExternalDAC`
  `DiracUSBVendor` `LRCK`(48000) `MCLK` `MCULatency` `PlayerLatency` `MRMPlayOffset`
  `Gcurrent_volume` `alsaoutputdevice`
- **Web/Wi-Fi/system:** `Defaultwebusername` `Defaultwebuserpassword` `Newweb*`
  `Vluciclientverification` `WAC_SSID`(Arylic LP10) `WACMode` `WifiPowersave`(off)
  `FriendlyName`(<device-name>) `IsFDR` `GEN_FAV_0…19` (20 presets)
- **Added by the 8530 OTA (in the live DB only, §5):** `AutoStandbyTime` `CustomVolControl` `FlightMode` `MaxAuthTimeout`
  `NetworkAlerts` `OtaAckTimeout` `OtaSkipCount` `OtaSkipEnableMask` `OtaStandbyUpdate` `OtaUpdateSchedule` `TidalGain`
  `USBFS` `UserLocale` `WebServer` `apn2` `artworkformat` `bleencryptiontype` `btmodes` `btscantimeout` `castdolby`
  `custom_{Brand,FriendlyName,Manufacturer,ManufacturerURL,Model,fwdownload_xml}` `esim_info` `logpolicystate`
  `mcuusblist` `netcontrolmask` `opdevicelist` `opresamplelist` `opvolsetting` `presetartworksize`

---

## Appendix B — the `lp10` controller

The companion TUI (`~/code/lp10`, Go) is what most of this doc's live state was read
through. It is **strictly read-only** beyond a small whitelist of control writes, and
drives the device over **two independent channels**:

1. **The SSH record loop** (§6.2) — one ssh connection runs a BusyBox-ash loop streaming
   `@@`-framed records (player state, and — only while the diagnostics overlay is open —
   resource/link stats); its stdin takes whitelisted `40`/`64` commands plus the app-only
   `90` stats toggle. The player and the diagnostics overlay ride this.
2. **The :2018 control tunnel** (§6.3) — a separate plain-text socket for tone/EQ/deep-bass
   /max-volume; the equalizer rides this. A dead tunnel only greys out the EQ.
3. **Two ssh-free probes** (since 2026-09-02) — the LSSDP responder (udp 1800) for liveness, and the
   Spotify engine's ZeroConf `getInfo` (port from the `_spotify-connect._tcp` SRV record, §8.1) for
   "is the engine up, on which eSDK, signed in as whom". The overlay's update line is the verdict the
   box fetched itself (its `ota` daemon asks the vendor every 4 h and logs the answer — the loop's `@@o`
   digest, §10.1); `u` inside the overlay asks the manifest directly — the one request that leaves the
   LAN, and only on that keystroke. **`lp10 sweep`** (2026-09-12) automates §14.3's inventory: one ssh session
   of fixed reads plus the ssh-free and vendor probes, diffed against the previous run's baseline in the state dir. The log pane can tail `/lsync/app.log` (§10.2) beside the syslog.

- **Discovery:** startup mDNS for the **`am=LP10`** advertisement resolves the current IP
  (so a changed DHCP lease never needs a config edit), falling back to the configured host.
- **Views:** *player* (now-playing + transport), *equalizer* (the :2018 controls), and
  *diagnostics* (`?`) — which gathers the live `@@s` metrics + link health, and also surfaces
  the device's **`@@c` streaming-capability matrix** (the marketed services, on/off) plus the
  model's static hardware facts from this teardown.
- **Auth:** the root password comes from the OS secret store via `SSH_ASKPASS`; host-key
  verification is disabled **by design** (the box regenerates its ramfs host key each boot,
  §3/§15) — the one deliberate tradeoff, fine only on a trusted LAN.
- **Source ids:** the app maps MID 42 `Current Source` as **1 AirPlay · 2 DLNA · 3
  Bluetooth · 4 Spotify · 5 Line-In · 6 USB** — an app-side interpretation; only **4
  (Spotify)** was confirmed live here.

---

## Inspection log & sources

- **Arylic LP10 product page / spec sheet** — https://www.arylic.com/products/lp10-music-streamer
- Reviews (cross-check): HomeTheaterHifi · ThePhonograph.net · HomeKitNews · PrimeAudio
- Datasheets: Amlogic A113L "A1" audio SoC · Cirrus **WM8904** codec · TI **PCM1863** ADC
- Protocols/platform: Spotify ZeroConf/Connect (commercial-hardware) · Apple AirPlay
  AccessorySDK · Google Cast · Roon RAAT · Airable · LibreWireless LS8

*Inspected read-only **2026-06-28 → 06-29** (4 passes; final airtight state re-verification)
against `<device-ip>` (fw `AR241CE_9243.16.2`, MCU v16, up 5 days; last seen streaming
Spotify — Pink Floyd). Spotify chapter originally **2026-06-27**. Control-plane protocol
(§6.2–6.3, Appendix B) reconciled against the `lp10` controller source **2026-06-29**. Specs
reconciled against the Arylic product page.*

*Control-plane pass **2026-06-30**: identified the `LUCI_local -remote` (`writeRemoteMessage`)
verb — the one that reaches the MCU — and used it to **draw custom text on the OLED** via MID 42
(§6.4); refined the volume model (two states; plain writes are audible but don't sync the MCU,
§6). Unlike the earlier read-only passes, this one made a few **deliberate, benign, reversible
writes** (a custom now-playing string; volume sets) to verify the control path — no settings,
env, flash, or MCU firmware were touched. Also re-confirmed independently: the remote is
**Bluetooth, not IR**; `wlcdmi`/`wl_cdmi` is **Widevine DRM decryption** for Cast (not the
panel); the MCU firmware `mcu.bin` is OTA-only (no MCU MTD partition). A `-remote 64` volume-sync
attempt was tested and **reverted** — inert, since the MCU owns volume (§6).*

*Offline passes **2026-07-01**: the OTA manifest probed read-only, `mcu.bin` pulled from the CDN by a single Range request
and reversed (§10.3); `luciserver`'s MsgBox descriptor table reversed (§6.5). **2026-08-22**: the `:2018` socket re-read
against the Arylic UART API — `EQS` is the preset index, `EQE` the enable (§6.3); LSSDP adopted as the ssh-free probe.*

*Firmware re-analysis **2026-09-02** (§14.2): after the August-2026 OTA to `AR241CE_8530.23.2` / MCU v23, both
vendor bundles were pulled from the CDN and diffed file-by-file, the MCU image re-strung, the vendor Rust app
pulled off the box, and the live state re-read in three short SSH passes plus ssh-free LSSDP / ZeroConf /
:2018 queries. Nothing was written to the device.*

*Re-sweep **2026-09-12** (§14.3): a suspected further OTA — there was none; identity, bundle, manifest and vendor app
all unchanged. Seven short read-only ssh passes plus ssh-free LSSDP / ZeroConf / :2018 / manifest / CDN queries;
the env store was read through sqlite (key names and the two Spotify rows only). What had changed was a
power-cycle on 2026-09-04 and, eleven minutes after it, a switch to the Spotify Pro engine made from `lp10`.
Nothing was written to the device.*
