# Arylic LP10 — Device Teardown & Technical Reference

> **What it is:** Arylic **LP10** — officially an *"AirPlay 2 & Google Cast Music Streamer"*:
> a LibreWireless-based **network audio streamer / line-level streaming source** that adds
> Wi-Fi/streaming to existing audio gear. Ethernet / Wi-Fi / USB / Bluetooth in → **3.5 mm
> line-out + optical (TOSLINK) out**, plus a **3.5 mm line-in**. **No power amp** (no speaker
> terminals) and **no phono stage** (line-level aux input, not RIAA).
>
> **This unit, as of 2026-10-06:** SoC Amlogic **A113L "A1"** (`a1-a113l-ad403-spk`) · platform **LibreWireless LS8** ·
> serial `RKARYLLP10<redacted>` · firmware **`AR241CP_8747.29.2`** / MCU **v29** — a **production** build
> (`libre_ls8_24G_v1_c4a_production_release_defconfig`; 8530 was the `…_debug_release` one), taken by the box's own OTA
> check on 2026-09-30 (§14.5) · **no ssh, telnet or adb** (the build deleted `dropbear`, `telnetd` and `adbd`, §9) ·
> vendor app `rakoit_app` **v42** (installed 2026-09-17; the vendor's CDN index still names v42 on 2026-10-06) · Spotify
> on the **Pro engine** (eSDK **3.216.31**, ZeroConf `:9095`) · wired `eth0` at **`<device-ip>`** / `<device>.local`
> (over a TL-WPA4220 powerline link).
>
> **How to read it.** Chapters 0–13 describe the device *as it is now*; where a fact changed with firmware, the old value
> follows in parentheses with its date. §14 is the dated history (June baseline → August OTA → September re-sweeps →
> the 2026-09-30 OTA), §15 is how to re-verify any of it read-only, and the inspection log at the end records every pass
> and what it touched. Everything up to 2026-09-23 was verified live on this unit over ssh (process table, `/proc`,
> `/sys`, ALSA, `iw`, binary `strings`, mDNS, the LUCI bus, the `:2018` tunnel, on-device certs, the sqlite env store)
> and cross-checked against Arylic's published specs (§12) and the `lp10` controller source (Appendix B). **8747 has no
> ssh:** the chapters that rest on a shell read were verified on 8530 or earlier, and where a reader could take such a
> fact for current it is marked *(verified on 8530 over ssh; not re-readable on 8747)*. The 8747 facts come from the
> LAN (LSSDP, mDNS, a port scan, the `:2018` getters, UPnP, ZeroConf), a static diff of the two vendor bundles and the
> web UI's log download (§14.5). **Nothing was written to the device** on the read-only passes; the passes that wrote
> (2026-06-30, benign and reverted; the 2026-10-01 tunnel tests' transport, volume and mute) are described in the log.
> Secrets (`SP_BLOB`, PSKs, passwords) were never read on the ssh passes — key names only; the 2026-10-01 log download
> contains them (§10.4), and only its non-secret keys are quoted here.
>
> **Revision log** — 2026-06-27 Spotify chapter · 06-28→29 full teardown (4 passes, `AR241CE_9243.16.2` / MCU 16) ·
> 06-30 control-plane writes (OLED via `-remote 42`) · 07-01 OTA endpoint probed, `mcu.bin` pulled and reversed, MsgBox
> table reversed · 08-22 `:2018` identified as the Arylic UART API (`EQS`/`EQE` corrected) · 09-02 the 8530 OTA
> re-analysed (bundle diff, MCU v23, `rakoit_app` v32) · 09-12 re-sweep: no newer OTA; env store decoded; Pro engine
> since 09-04 · 09-23 re-sweep: `rakoit_app` v42; the manifest offers 8530 to no older build (likely the per-`deviceId`
> quota found on 10-03, §14.6); the syslog's rotated
> history on flash; eSDK reconnects on both engines · **10-01 re-sweep without ssh (`AR241CP_8747.29.2` / MCU 29): the
> production build drops dropbear, telnet and adb; the `:2018` tunnel pushes the track, play state and volume; the
> Spotify app's volume no longer reaches the softvol; the web UI's log download read in place of ssh (§14.5).** · 10-01
> later: the web UI's Spotify switch decoded (the HiFi flag only, §8.1); §0–§2 corrected to the August finding that the
> WM8904 is absent and the BP10xx MCU is the DAC (§2). · 10-03 re-sweep: the box and the vendor unchanged; the manifest
> counts its offers per `deviceId` — five, then "no update" (§10.1, §14.6); two counts from the log download corrected.
> · 10-06 re-sweep: nothing changed; §15's manifest recipe sends a fresh `deviceId` (§14.7).

---

## 0. TL;DR

A dual-core Amlogic A1 (32-bit ARMv7, Buildroot/Linux 5.15, Android-derived IPC) running the
**LibreWireless LS8** stack. It speaks a huge range of audio protocols natively — **Spotify
Connect, AirPlay 2, Google Cast, Roon (RAAT), Tidal Connect, Qobuz Connect, Bluetooth 5.0
A2DP (sink + source), DLNA/UPnP, Airable/TuneIn radio, QPlay, Alexa/AVS, Matter** — each its
own daemon, gated by a persistent env flag. **Enabled on this unit: Spotify, AirPlay 2, DLNA,
Bluetooth** — the rest are installed but currently off (§8). Audio leaves the Amlogic
**"AUGE"** complex as I2S (TDM-B) for the front-panel **MCU (MVSilicon BP10xx), which is the DAC** and runs every
tone / EQ / virtual-bass / balance / max-volume stage (§2, §6.3); an **S/PDIF** path feeds the optical out (up to
**24-bit/192 kHz**). The device tree also declares a **WM8904 codec**, but no chip answers at its I2C address — its
mixer controls reach nothing (§2). Of the SoC's **AED** EQ/DRC block only the multi-band DRC is audible, and the **two
HiFi4 DSP cores** are not in the audio path. There is **no power amp** — the loaded
`tas5707` driver is unused generic-image baggage (confirmed by the I2C scan and the line-only
back panel). Control is the LibreWireless **LUCI/MsgBox** bus plus a **GoAhead web server**, and the
**`:2018` tunnel** relays the MCU's Arylic UART API to the LAN (what `lp10` drives — since 8747 its only channel,
§6.3); the front **0.91″ OLED** + **4 touch buttons** are run by an on-board
**MCU** (MVSilicon **BP10xx**, C-SKY core) over serial (the `LP10` text itself is silkscreen). The
host can drive **only the now-playing line** of the OLED — push MsgBox 42 (`RemoteUI`) via
`LUCI_local -remote 42`; a **full-screen custom message is not host-reachable**, confirmed by
reversing the MCU firmware (§6.4, §10.3). Three baked-in identities: a
LibreWireless device cert (LWT Root CA), a Google Cast cert (RAKOIT), and a Spotify OEM identity.

**What leaves the house** (audited 2026-09-13, §10.4): live, only the Spotify session; on timers, a 4-hourly OTA
check that carries the unit's MAC and serial and a 10-hourly app-index fetch that carries nothing; NTP and one
`Date:` header from google.com at boot. No cloud relay, no remote syslog, no cron. A LibreWireless **log-report
uploader is installed and armed** (crash / button / LUCI 651) and would ship the whole env store, encrypted, to the
vendor — it had never fired here by 2026-09-13; the web UI's log download writes an env dump, secrets included, into
the syslog (seen 2026-10-01, §10.4). 8747 adds a metrics uploader, dormant (`metrics_url` empty). The real exposure
was LAN-side: through 8530 **network ADB handed out a root shell with no authentication**; the 8747 production build
deleted `adbd`, `telnetd` and `dropbear` (§9). The unauthenticated `:2018` tunnel and a serial root console remain.

> **State** below = verified on **this unit** (env flag **and** running daemon); first read 2026-06-28/29, re-read over
> ssh until 2026-09-23; on **2026-10-01** the running daemons and the Spotify / Qobuz / Roon flags were re-read from the
> log download's `ps` and env dump (§14.5) — the other flags are as of 09-23.
> Everything is installed in firmware; most services are env-gated and togglable via the app/web.

| Capability | Daemon / mechanism | Port(s) | State (this unit) |
|---|---|---|---|
| Spotify Connect | two engines, one runs: `spotifymusicpro` (Pro, eSDK 3.216.31 on 8747, 3.211.130 on 8530) **now**; `newspotifyhifi` (HiFi, eSDK 3.203.239) until 2026-09-04 — §8.1 | per engine: 9096 (HiFi) · 9095 (Pro), 5353 | **ON** — **Pro engine** since 2026-09-04 (`SpotifyEnabled=0 / SpotifyProEnabled=1`, switched from `lp10`'s services pane, §14.3; HiFi ran 1/0 before); the pair held through the 8747 OTA |
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
| Line-in (3.5 mm) → stream/local | ADC (not established) → `audionexus` | — | hardware-present |
| USB-A flash playback | `UIframework` + GStreamer + `automount` | — | **OFF** (`USBEnable=0`) |

---

## 1. Hardware

### Back-panel I/O (physical)

| Connector | Purpose |
|---|---|
| **RJ45 Ethernet** | Wired LAN (`eth0`), 10/100M — the primary network path here (powerline-fed) |
| **USB-A** | Host port — USB flash-drive playback (Arylic caps it at ~1000 songs / 16 GB; `USBEnable=0` at inspection) |
| **3.5 mm line-in** (TRS, 1 Vrms) | **Line-level aux** input (not phono); which ADC digitizes it is not established (the WM8904 the DT names is absent, §2.1) |
| **3.5 mm line-out** (TRS, 1 Vrms) | Analog line output from the **BP10xx MCU's DAC** (§2) |
| **Optical out** (TOSLINK) | S/PDIF digital output, up to **24-bit/192 kHz** |
| **USB-C** | Power (5 V/2 A) + the RNDIS/ADB service gadget (§7; on 8747 the RNDIS function gets no IP and `adbd` is gone); Arylic also lists it as **"PC audio"** (USB-audio function) |

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
| **DAC** | The **MVSilicon BP10xx MCU** (row below), fed I2S on TDM-B: its on-chip DAC drives the **3.5 mm line-out**, and it hosts the tone / EQ-preset / virtual-bass / balance / max-volume DSP (§6.3). The DT also declares a **Cirrus/Wolfson WM8904** at I2C `0x1a` and its driver binds, but the chip does not answer (`i2cget` on `0x1a` → ENXIO, 2026-08): its EQ1-5 / DRC / volume controls write a register cache and reach nothing (ear-tested) |
| **Spare ADC (unused)** | A **TI PCM1863** stereo ADC sits at I2C `0x4a` (`status=okay`), but its `pcm186x` driver is **unbound** and it's absent from the ASoC graph → **not used** (LS8 reference-design leftover) |
| **Power amp** | **None** — line-level device (`i2cdetect` finds no amp chip; `tas5707` module is unused baggage) |
| **DSP** | Amlogic **AED** HW block (5-band EQ, multi-band + full-band DRC, crossover, limiter, 2×2 mixer — of these only the **multi-band DRC enable** is audible; the EQ / DRC / crossover coefficient tables accept writes and never change, 2026-08 ear tests) **+ two Tensilica HiFi4 cores** (`/dev/hifi4dsp0/1`, not in the audio path); the boot script loads `dspbootA.bin` onto core A and starts its RTOS (`dsp_util`) |
| **Digital out** | **S/PDIF** DAI → the optical/TOSLINK jack (≤24-bit/192 kHz); internal **loopback** capture (for multiroom/cast) |
| **Wi-Fi / BT** | **Dual-band 2.4 + 5 GHz 802.11ac/VHT** SDIO Wi-Fi (driver `aml_w1`/`amlogic_wireless`; `fwVersion.conf` tags `wifi_hw=NXP`; both bands live on phy0/phy1, ≈390 Mbps VHT) + UART **Bluetooth 5.0** (`hciattach … aml` on `ttyS1`, BlueZ) |
| **MCU** | Companion microcontroller on **`/dev/ttyS2` @ 115200** (firmware **v29** since the 2026-09-30 OTA, `VER:29-1d316f0c-10`; 23 from the August-2026 OTA, 16 before), owned host-side by `luciserver`. Drives the **OLED + touch buttons + IR remote + standby**; its firmware updates over this UART via XMODEM (§6.1) |
| **Controls** | **4 touch buttons** (Mode / Vol− / Play-Pause / Vol+ → `gpio_keypad`/`adc_keypad` → MCU → MsgBox 64) + the bundled **IR remote** (MCU-decoded — there is **no** Linux IR/lirc subsystem). An `input_btrcu` BT-HID remote input (under `aml_bt`) also exists. The `rotary-encoder` DT node is **`disabled`** — no knob |
| **Front display** | **0.91″ 128×32 monochrome (blue) OLED**, **MCU-rendered** (§6.1). Not a Linux framebuffer, not `spi_led` |
| **USB** | USB2/USB3 PHYs; **USB-A** host (flash-drive playback; `USBEnable=0`); **USB-C** = power + a device-gadget (**RNDIS + ADB**, `usb0`=192.168.5.1 through 8530; on 8747 no `usb0` IP and no `adbd`, §7). Arylic also lists USB-C "PC audio" (a USB-audio function); `snd_usb_audio` was not a loaded module on 8530 — 8747 ships `snd-usb-audio` and loads it at boot (§14.5) |
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
| 00-01 | **TDM-B** | **wm8904** (declared; chip absent) | play+cap | I2S → the **BP10xx MCU** (the DAC) → **3.5 mm line-out**. The DAI is bound to the WM8904 driver, but no WM8904 answers on I2C (§2.1) |
| 00-02 | **PDM** | dummy | cap | Digital-mic port — supported, unused on the LP10 |
| 00-03 | **SPDIF** | dummy | play+cap | **S/PDIF** → optical out (≤24-bit/192 kHz; the input side is unexposed) |
| 00-04 | **LOOPBACK-A** | dummy | cap | Internal loopback (capture the mix → multiroom/cast) |

**Signal flow:**

```
 NETWORK / USB / BT sources ─► sw decode ─┐
                                          │  audionexus       ┌─► TDM-B (I2S) ─► BP10xx MCU ─► 3.5mm LINE-OUT
 LINE-IN (3.5mm) ─► ADC (not established) ►│  (router +        │   (DAC + tone/EQ/vbass/balance/max-vol)
                                          │   softvol +        │
                                          │   AED DRC) ────────┴─► S/PDIF ─► OPTICAL (TOSLINK) OUT
 [loopback tap] ◄──────────────────────────                       (+ loopback → multiroom)
   Volume = softvol Master (SoC, set by sourceswitch from the MCU's level) · Source select = sourceswitchservice
```

- **`audionexus`** is the central router/mixer (exposes `AUX_START/STOP`, `SPDIF_START/STOP`);
  **`sourceswitchservice`** selects the active input. Source `4` = network/Spotify.
- The line-in can be played locally and/or **streamed** to other LibreWireless rooms via the
  loopback tap.
- **Sample rates:** optical out up to **24-bit/192 kHz** (Arylic spec); the analog line-out is
  line-level (1 Vrms) from the MCU's DAC (its rate limit is not established). Observed live:
  44.1 kHz/16-bit (Spotify's native Ogg/Vorbis — §8.1: no FLAC reached the box in 17 days of log).
- **94 ALSA mixer elements** (93 at an idle boot: the softvol **`Master`**, numid 94, appears when the first stream
  opens — it is the audible volume stage, 0–99 = volume − 1): the inert **WM8904** driver's (`Headphone`, `Line Output`, `Capture`,
  `EQ1-5`, `DRC`, `High Pass Filter`, `*Capture Mux/Mode`, `HPL/HPR/LINEL/LINER/DACL/DACR/
  AIFOUTL/R Mux`); **AED** HW DSP (`AED EQ/DC-cut/DRC/Noise-Detect/Crossover/Clip-THD/
  Mixer-Gain/master+L/R volume`); **SoC I/O** (`Audio In Source`, `Audio Out Sink`,
  `Audio spdif format/mute/in-source`, `Loopback datain source`, `PDM *`). SoC capture-mux
  options: `TDMIN_A/B/C, SPDIFIN, PDMIN, LOOPBACK_A/B, …`.
- Codec kernel modules: `amlogic_snd_codec_a1` (SoC internal: AED/SPDIF/PDM/loopback),
  `_tas5707` (loaded but **no chip**), `_tl1`, `_dummy`; WM8904 driver built-in. Local-file
  decode via FLAC / `faad` (AAC) / `mpg123` (MP3) / `sbc` (BT) + **GStreamer** + `aml_audio_player`.

> **Live read (during Spotify playback, 2026-06-28):** `pcm1` (TDM-B) opens at **44.1 kHz / `S16_LE` / stereo** while
> `pcm0p`/`pcm3p` stay `closed`. Source was Ogg/Vorbis (`Mime: Ogg`, `SampleRate: 44100`). Idle → all `hw_params`
> `closed`. dmesg also carries periodic `wm8904 … soc_component_read … -16` (EBUSY) register-read warnings.
>
> **Corrected (2026-08 recon and ear tests, on 8530 over ssh):** the June reading took the open TDM-B stream for the
> WM8904 at work. It is not: the chip does not answer at `0x1a` (`i2cget` → ENXIO; the EBUSY reads above fit a driver that
> cannot reach its chip), its EQ / DRC / volume controls change nothing audible, and the stream on TDM-B reaches
> the **BP10xx MCU**, which is the DAC and does all the tone / EQ / max-volume DSP (§6.3, §10.3). The SoC side's only
> audible levers are the softvol `Master` and the AED multi-band DRC enable (the old night mode, which needed `amixer`
> over ssh). Never `alsactl restore`: it rewrote `Master` from a stale state and dropped the room's volume.

### 2.1 I2C bus map (verified)

Checked two ways — `sysfs` (zero bus traffic) and a safe **`i2cdetect -y -r 0`** (SMBus
*read-byte*; driver-owned chips show `UU` and are never probed). Single bus, `i2c-0` (Meson):

| Addr | Result | Chip |
|---|---|---|
| `0x1a` | `UU` (driver-owned) | **WM8904** codec — declared (`status=okay`) and its driver bound, but the chip does not answer (`i2cget` → ENXIO, 2026-08): **absent**. `UU` only means the driver holds the address, so `i2cdetect` never probed it |
| `0x4a` | `status=okay` but driver **unbound** | **PCM1863** stereo ADC — populated but **unused** (not in the ASoC graph) |
| `0x10` | intermittent ACK, reads `0x00` | phantom (floating-bus read-byte artifact) |
| TAS5707 addrs | nothing | no power-amp chip on the bus |

The image bundles drivers for many board variants (`cs43130`, `pcm512x`, `pcm186x`, `tas5707`,
`wm8904`, `rtc-pcf8563`, `meson_pmic6b`, a touchscreen…); on this LP10 the **WM8904** driver binds to a chip that
is not there (above), and the PCM1863 is populated in DT but its driver never bound. The scan was
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

**Versions** (`/etc/fwVersion.conf`, from the 8747 bundle): `build_number=AR241CP_8747`, `build_date=2026-09-29`,
`app_svn_version=366`, new `ext_svn_version=4958`, `defconfig=libre_ls8_24G_v1_c4a_production_release_defconfig`
(8530: `AR241CE_8530` / `2026-01-12` / `318` / `…_debug_release`; June 2026: `AR241CE_9243` / `2025-12-24` / `312` —
§14.1), `kernel=5.15` (5.15.137), `platform=LS8`, `target=EVK`, `wifi_hw=NXP`, `ram=256MB`, `flash=512MB`. Userland =
**Buildroot 2020.02.1** (`os-release` `svn366`, was `svn317`), shell **BusyBox v1.32.0** (rebuilt for 8747).
`reboot_mode=cold_boot` (with an empty `/sys/fs/pstore` at every ssh pass) was read as "the last boot was a power-on,
not a software reboot" — but the 2026-09-30 OTA's reboot logged the same flag (§14.5), so it does not tell a power loss
from an OTA reboot (§11). `fw_printenv` reports a bad-CRC U-Boot env → the real cmdline is baked into
the boot image, not a live env *(verified on 8530 over ssh; not re-readable on 8747)*. (The `24G` in the defconfig is a
module tag — the radio is dual-band, §1.)

---

## 4. OS & service catalog

Busybox `init` runs `/etc/init.d/S*` (~50 scripts). The stack is **Android-derived**: an
Android `servicemanager` + `/dev/binder`, `adbd` (through 8530), a `jdwp-control` socket, `android::` symbols
across daemons. Live daemons and their roles (read over ssh through 8530; the 8747 process list is below the table):

| Process | Role |
|---|---|
| `rakoit_app` (`/lsync/rakoit_app`, Rust; **v42** since 2026-09-17, v32 from 2026-08-25) | **Main app** — play queue, now-playing, favorites/presets (the remote's FAV/NUM keys), the PlayView publisher, Qobuz/TuneIn/radio-browser client, a PlaylistServer on tcp 2345, and a loopback client on the :2018 tunnel (§10.2) |
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
| `tcptunnelling` | **Control tunnel + LAN alert relay** — TCP 2018: the MCU's plain-text UART API on the LAN — player, volume, mute, tone/EQ/max-vol (what `lp10` drives, since 8747 its only channel — §6.3) **and** a fan-out of LUCI alerts to whoever is connected. A boost.asio TCP **server**: it opens no outbound connection and knows no relay host (§10.4). 8747 rebuilt it with up to two listener slots (§6.3) |
| `system_monitor` · `usb_monitor` + `automount` | crash collector and log-report agent (§10.4) — since 8747 also a **metrics server/uploader**, dormant while `metrics_url` is empty · USB hotplug + mount |
| `io_handler` (new in 8747, `S38io_handler`) | GPIO / LED handler over LUCI — **inert**: env `iohandler=0`, it logs `Feature IO_handler is not Supported!!!` |
| `dropbear` · `inetd`→`telnetd` · `adbd` | through 8530: **SSH 22** · **telnet 23 (root)** · **ADB 5555/5037**. **8747 deleted all three binaries**; `inetd` still runs with an empty `inetd.conf`, serving nothing (§9) |
| `dhcpcd` · `ntpd` · `wpa_supplicant` · `netmonitor` | DHCP · NTP · Wi-Fi · link-event monitor |
| `daemon` (`/factory/custom/csys/bin/daemon`, signed) | Arylic **app loader** ("rak-loader" since 8530): fetches/verifies `rakoit_app` from `cdn.rakoit-ota.com/download`, independent of the OTA (§10.2) |
| `dbus-daemon`(×2) · `rsyslogd` · `mdev` · `getty`(×2) | system plumbing |

Also present: **SDDP/SUDP** (Control4) discovery, cast discovery, and a legacy **LinkPlay-style
UDP** surface (port 3721).

> **Running vs installed (this unit):** of the streaming endpoints, only `spotifymusicpro` (the Spotify Pro
> engine — `newspotifyhifi` until 2026-09-04), `airplaydemo`, `dmr`, and `bluetoothd`/`bluealsa` are running. `librecast`, `libreraat`,
> `tidalConnect`, `qobuzConnect` are installed but their env flags are off, so their init
> scripts don't start them (see the §0 state table).
>
> **On 8747 (2026-10-01, the log download's own `ps`, §14.5):** `airplaydemo` `audionexus` `automount` `bluealsa`
> `bluetoothd` `daemon` (rak-loader) `dbus-daemon` ×2 `default_agent` `dhcpcd` `dmr` `env_service` `gatt-server`
> `getty` ×2 `hciattach` `inetd` `librewebserver` `luciserver` `mdev` `mdnsd` `messageboxhandler` `netmonitor` `ntpd`
> `ota` `rakoit_app` `rsyslogd` `S99roon_monitor` (sh) `servicemanager` `sourceswitchservice` **`spotifymusicpro`**
> (the Pro engine only) `system_monitor` `tcptunnelling` `UIframework` `usb_monitor` `WACServer` `wlcdmi_server`
> `wpa_supplicant` `yaniserver`. Not running: `dropbear`, `telnetd`, `adbd`, `newspotifyhifi`, `qobuzConnect`,
> `tidalConnect`, `librecast_lite`.

---

## 5. Persistent settings — the env store

Every feature flag, identity value and user setting on the box lives in one store, read and written through
`getenv` / `setenv` (binder clients of `env_service`). Knowing how it persists answers most "why did it revert" and
"why did it stick" questions — including the one the 8530 OTA raised (§8.1). *(The store was read over ssh with
`sqlite3` on 8530; 8747 has no shell, and the only window into it now is the env dump the web UI's log download writes
into the syslog, secrets included — §10.4, §14.5.)*

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
- **Consequences.** A `setenv` from the app, the web UI or `lp10` (through 8530) is durable across reboots and — on the
  8530 precedent — across an OTA's factory flip; a flag you never touched follows the factory. (The 8747 OTA kept the
  Spotify pin `0/1`, but its own factory pair is also `0/1`, so it does not test the rule.) `sqlite3` is on the box
  (`/usr/bin/sqlite3`), so on 8530 `select key, value, dbit …` over ssh was a legitimate read — but the same table holds
  `SP_BLOB`, the Wi-Fi PSK and the web password: **select key names, never `*`.**

> The vendor app keeps its own state next door — `/lsync/source_service/{credentials.redb, secrets.json.enc,
> session_store}` and `/lsync/cache.redb` belong to `rakoit_app` (§10.2), not to the env store.

---

## 6. The LibreWireless control plane (LUCI / MsgBox)

Control is **not** LinkPlay (no `/httpapi.asp`, no port 8899). It's LibreWireless **LUCI**:
the `LUCI_local` CLI talks to `luciserver`/`messageboxhandler` over a numbered "MsgBox" bus.
`lp10` (Go TUI) drove exactly this over ssh until 8747 removed ssh; since 2026-10-01 it uses the `:2018` tunnel only
(§6.3, Appendix B). Everything in §6–§6.5 that needs `LUCI_local` was verified over ssh on 9243 or 8530 and is not
re-readable on 8747. `LUCI_local -r <MID>` reads, `-a` dumps all.
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

> **Mute (from `lp10`).** Through 8530 the controller never sent a device mute: it
> set **MID 64 to 0** and remembered the pre-mute level (persisted under
> `~/.local/state/lp10`), restoring it on unmute. The device's own LUCI mute path restored the
> *wrong* level, and **MID 63 is status-only** (writing `MUTE` doesn't actually cut
> audio), so volume-to-zero + a client-side restore was the reliable mute. (Accordingly
> that controller's write whitelist was just MID 40 `PAUSE|RESUME|NEXT|PREV`, MID 64
> volume, and an app-only "90" stats toggle — §6.2 — never a mute MID.) **Since 2026-10-01** `lp10` sends the tunnel's
> `MUT:1;` / `MUT:0;` — a real mute in the MCU: `STA`'s mute field reads 1 and the level stays where it is (§6.3).

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
42 — §6.4 — just not MCU-owned volume). Those were `lp10`'s ssh-era MID-64 writes; a tunnel `VOL:n;` takes the MCU's
own path instead — the MCU applies it and reports it up, and Spotify's slider follows (§6.3).

**Web API:** `librewebserver` (GoAhead/Embedthis Appweb) on :80 serves the setup/control UI,
`.asp` templates, and a `/action` **goform** API (e.g. `GoForm_SetTidalmode`), plus the
captive-portal setup + OTA pages and `logs → /tmp/libre/logdump/`. Routes/auth in
`/factory/custom/web/{route.txt,auth.txt}`; web creds from env (`Defaultweb*`/`Newweb*`). Its **Spotify** switch is the
HiFi engine's flag only — it reads "disabled" on a Pro box, and switching it on breaks Spotify at the next network event
(§8.1). Its "Volume Mode" sends `VOLUMEMODE:Variable|Fixed` to the MCU (Fixed locks the output level).

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
  own firmware (v16 at the teardown, v23 from the August-2026 OTA — §14.2, **v29** since the 2026-09-30 OTA — §14.5) updates over this UART via **XMODEM** (MsgBox 770 `type:xmodem`).

> Unrelated: `wlcdmi` is a **Wayland + DRM / Widevine-CDM** GStreamer element for the **Cast**
> media path (`wl_display_*`, `/usr/lib/wlcdmidrm`, `/dev/secmem`), not the front panel.

### 6.2 The `lp10` streaming loop (the `@@`-record protocol — retired with 8747)

> **Retired 2026-10-01.** The loop ran over ssh, which 8747 removed; `lp10` now rides the `:2018` tunnel alone (§6.3,
> Appendix B). This section stays as the record of how the 8530-era controller read the box.

`lp10` didn't poll `LUCI_local` per frame. It runs **one** BusyBox-ash loop on the
device (over its single SSH connection) that streams **`@@`-framed records** on stdout
and takes whitelisted commands on stdin — so the laptop side just parses a stream and
the per-tick device work is a handful of builtins. The sections of a record:

| Tag | Cadence | Contents |
|---|---|---|
| `@@i` | once per connection | static device/network: `net`/`ip`/`mac`/`gw`, link (`speed`/`duplex` or `ssid`/`freq`/`rate`), `build`/`app`/`platform`, the FriendlyName (reg 90), `/lsync` usage, `dns`, the vendor app version (`vapp`), the kernel's `reboot_mode` (`rboot`) — parsed from `ip route` / `iw` / sysfs / `fwVersion.conf` by shell parameter-expansion |
| `@@c` | at connect, and after a MID-92 switch (at once and 3 ticks later) | **capability block** (the services view and the diagnostics' services strip): the running daemons from one pass over `/proc/*/comm` (no `pidof` forks), the Spotify picture (`spotify.eng` running engine, `spotify.sdk` its eSDK build, `spotify.cfg` the env pair hifi/pro/both/none, `spotify.proc` engine age + ssh launcher, `dirty` the runtime-set env key names), `getenv` only for the flags an init script consults (`TidalEnabled`/`QobuzConnectEnabled`/`USBEnable`), and the unauthenticated listeners from `/proc/net/tcp{,6}` (telnet/adb/web/control). Roon/Alexa/Matter are **not** read. |
| `@@d` · `@@g` | once per connection | raw reg 92 (serial, MACs, MCU, full firmware) and reg 39 (multiroom group) JSON, parsed laptop-side |
| `@@n` | at connect, and after each MID-91 set | the AED multi-band DRC enable (night mode) as ALSA reads it back |
| `@@o` | at connect, and when the diagnostics open (the 0→1 change only) | the digest: `n` eSDK "connection … has been lost" count and `t` first stamp of the live syslog (the window), `u` the last MsgBox-223 report from `/lsync/app.log` (the box's own OTA verdict, §10.1) |
| `@@B` | on change (a 15-tick fallback; forced by a skip, a play-state change, a command burst or the player coming back) | MID 42 now-playing JSON (only re-shipped when it differs) |
| `@@p` | every 5th tick, only while the player is on screen and playing | MID 49 position ms (the UI extrapolates between reads; a detected skip forces a re-read) |
| `@@t` · `@@v` | one per tick, alternating (both on the first tick and after a burst) | MID 51 play-state · MID 64 volume |
| `@@s` | per tick **only while the diagnostics are open** | resource/link stats: uptime, loadavg, mem, SoC temp, iface byte and error/drop counters, Wi-Fi signal/link/noise, the ALSA DAC's *actual* rate/format/channels + buffer fill, CPU clock, run/total procs; every 3rd one adds three ICMP RTTs (laptop/gateway/internet — the internet target is an IPv4 resolved on the laptop, or none, so the box never waits on its own resolver) and the softvol `Master` level |
| `@@l` · `@@L` | on MID 93 (`1` syslog, `2` vendor app log) | the last 160 lines, each space-prefixed; luci_service chatter dropped from the syslog, and any `blob`/`username` line dropped from both **on the box** |
| `@@E` | — | end-of-record |

- **Adaptive cadence:** a 1 s tick while playing with the player on screen, 3 s when idle
  or while another view is up; per tick one LUCI read (state and volume alternate), the
  position every 5th tick, the metadata on change.
- **stdin = a command whitelist**, never `eval`: `40 PAUSE|RESUME|NEXT|PREV` (transport)
  and `64 <0-100>` (volume) are forwarded to `LUCI_local`; the loop handles the app-only
  numbers itself and never passes them to `LUCI_local` — `90 1|0` (stats on/off), `91 1|0`
  (night mode), `92 "<id> <state>"` (service switch), `93 1|2` (log tail), `94 1|0`
  (player on screen). So the loop's "90" does **not** touch the device's real **MID 90
  (FriendlyName)** — it is a controller-side use of the number on the same stdin
  channel, not a MsgBox write. A burst of queued commands is drained at most 8 at a
  time, so a held key cannot starve the record stream past the 8 s watchdog.
- **Self-reaping:** the loop detects a dead SSH session by read-timing and exits, so both
  ends are reaped however the controller died; the client reconnects with backoff, and
  holds a login back (30 s, doubling to 2 min) once logins end short twice in a row —
  the dropbear lockout (§11).

### 6.3 The :2018 control tunnel (the MCU's UART API — `lp10`'s only channel since 8747)

The `tcptunnelling` daemon's **TCP 2018** (no cloud relay — §10.4) speaks a **plain-text control protocol** on the LAN:
the player (play state, transport, volume, mute), tone, EQ, deep-bass and the output cap. It was the channel `lp10`'s
equalizer drove beside the ssh player stream; **since 8747 removed ssh it is `lp10`'s only channel to the box**, player
and equalizer both (Appendix B). Wire format: bare ASCII
**`CODE:VALUE;`** (semicolon-terminated, **no newline, no framing, no auth**); a bare
**`CODE;`** is a *query* for most codes — the device answers by **broadcasting `CODE:VALUE;` to every
connected client** — but `POP`, `NXT` and `PRE` are *actions* (below). The device clamps authoritatively and echoes the
applied value back.

| Code | Control | Range |
|---|---|---|
| `MXV` | **Max-volume cap** (output ceiling) | 30–100 (doc) |
| `EQE` | EQ **enable** — whether the selected preset is applied at all | 0/1 |
| `EQS` | EQ **preset index** into `PEQ` (`0@Flat,1@Classical,2@Pop,3@Jazz,4@Rock,5@Vocal`) — **not** an on/off switch | 0–5 |
| `BAS` / `MID` / `TRE` | **tone** (bass / mid / treble) — live regardless of `EQE` | −10…+10 dB |
| `VBS` | **deep/virtual-bass** switch | 0/1 |
| `VBI` | deep-bass **intensity** | 0–100 |
| `BAL` | **balance** (+ favours right) | −100…+100 |

> **Corrected 2026-08-22 (re-verified on MCU v23 on 2026-09-02 and 2026-09-12, on MCU v29 on 2026-10-01):** this
> socket is the full public **Arylic UART API** (developer.arylic.com/uartapi) tunnelled to the MCU — the same command
> table in every `mcu.bin` image (102 + 21 codes by the 09-02 count; the 10-01 diff counts 103 + 21 + 11, identical in
> v23 and v29 — §10.3). `EQS` is the preset *index* (an earlier `lp10` toggled it 0/1 and
> was selecting *Classical* for "on"); `EQE` is the real enable. Other safe getters answered live:
> `VER:23-4ef47210-9` · `STA:NET,0,68,0,0,3,0,0,1,0` · `SRC:NET` · `LST:NET,BT,LINE-IN,USBPLAY` ·
> `VOF:1` `VST:3` `DLY:60` `CFE:0` `CFF:110` `LED:1` `BEP:1` `POM:NONE` `CHN:L` `MRM:N` · `NAM` hex-UTF-8.
> **Never send bare** `WRS;`, `SYS:*`, `DEF:SAV`, `STP`/`PST`, `PMT:`/`COE:` (setup / reboot /
> transport). `POP`/`NXT`/`PRE` are the transport actions — they act when sent bare, so they are never a query; since
> 2026-10-01 `lp10` sends them on a keypress only. **Client gotcha:** `tcptunnelling` serialises clients — a burst of one-shot connections
> (26 in a row) left the next connect hanging; hold one connection and query sequentially. **Second gotcha
> (2026-10-01):** a fresh connection was once accepted and never served — no reply to any getter for 27 s — while the
> next connection answered at once; a client must treat a silent connection as dead. **Third (framing):** the frame
> ends at the first `;`, and the track fields are free text, so a title holding `;VOL:100` would read as a title plus
> a real `VOL:100` frame. Whether the device escapes a `;` in a title is not established (none seen); `lp10` drops the
> values it acts on from any read that carries a track field (Appendix B).

**The player over the tunnel (verified live 2026-10-01 on MCU 29, while Spotify played).**

- **Getters** (read-only): `STA;` → `STA:NET,0,83,0,0,3,0,0,1,0;` = source, mute, volume, treble, bass, net, internet,
  playing, led, upgrading · `VOL;` · `MUT;` · `PLA;` · `SRC;` · `LST:NET,BT,LINE-IN,USBPLAY;` · `WWW:0;` ·
  `VER:29-1d316f0c-10;` (API level **10**; 9 on MCU 23) · `MXV:100;` ·
  `PEQ:0@Flat,1@Classical,2@Pop,3@Jazz,4@Rock,5@Vocal;` · `EQE` `EQS` `BAS` `MID` `TRE` `VBS` `VBI` `BAL`.
- **Pushes to every TCP client:** on a track change `TIT:<title>;ART:<artist>;ALB:<album>;` in **plain UTF-8** (not
  the hex the UART doc describes); on resume `PLA:1;VND:spotify;`; on pause `PLA:0;`; after `NXT;` an echo `RAW:NEXT;`
  (new in MCU 29: `RAW:NEXT;` / `RAW:PREV;`); and `VOL:n;` when the Spotify app changes the volume (steps of 4 while the
  slider is dragged). Whether the knob, the remote or a mute push a frame was not checked. No `ELP` (the doc: "not
  work when sent in TCP"), so no position or duration — 25 s of passive listening while a track played produced no
  frame.
- **Actions:** `POP;` toggles play/pause (the reply is the `PLA` push) · `NXT;` skips (then `TIT`/`ART`/`ALB`) ·
  `VOL:43;` echoes `VOL:43;` and is applied · `MUT:1;` echoes `MUT:1;` twice and `STA`'s mute field reads 1 — a real
  MCU mute — and `MUT:0;` puts it back.
- **The `VOL` path.** A tunnel `VOL:n;` goes through the MCU: `[LS->MCU] MB# 112 VOL:43;` → the MCU applies it →
  `[MCU->LS] MB# 64 SET 43` → `IncomingHouseKeeping() Received new volume 43` → Spotify is told (`OnVolumeChangeEvent`,
  its slider follows) → `SourceSwitch: processVolumeChange() Applied volume: 43`. The knob and the remote take the
  same MCU path. The Spotify app's own volume does not reach the audible stage on 8747 (§8.1).
- **8747's `tcptunnelling`** (bundle diff): up to two TCP listener slots, each port set by a `TUNNEL_START` message;
  payloads are no longer logged (`TCP -> MCU from <ip>:<port> (listen 2018), N bytes`).

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

Interfaces as read over ssh on 8530 (not re-readable on 8747, except the `eth0` lease the LAN sees):

| Iface | Address | Notes |
|---|---|---|
| `eth0` | **<device-ip>/24** (+ IPv6 ULA + LL) | **Default route** (via <gateway>). MAC `<powerline-mac>` = the TL-WPA4220 powerline link |
| `wlan0` | link-local only — **DISCONNECTED** | Up but unassociated (device is wired). MAC `<wlan0-mac>`. Radio is dual-band (§1) |
| `p2p1` | — | Wi-Fi-Direct/P2P iface |
| `usb0` | **192.168.5.1/24** (through 8530) | USB-C device-gadget (below); 8747 gives it no IP |
| `lo`, `sit0` | 127.0.0.1 / — | loopback, inactive 6in4 |

DNS via DHCP (`fibertel.com.ar`; 1.1.1.1 / 8.8.8.8 + ISP). IPv6 active on the LAN.

**A second DHCP server answers on the LAN (seen 2026-09-19).** When eth0's carrier dropped at 03:03 (a powerline
resync), `dhcpcd` restarted, was NAKed, and took **192.168.0.100 for 180 s from 192.168.0.41** — with 192.168.0.41 as
the default gateway. The next carrier drop at 03:07 brought 192.168.0.13 back from the router (192.168.0.1). The
answering unit is TP-Link (MAC prefix `0C:EF:15`, a TP-Link web UI) — probably the TL-WPA4220 powerline extender's
own DHCP server, which answers when it does not see the router. For those four minutes the box's traffic went to
that unit, and the network event restarted `rakoit_app` and the Spotify engine. Setting that unit's DHCP server to
off (not "auto") keeps it from answering; that is a change on the TP-Link, not on the LP10.

**Again after the power loss (the log download, read 2026-10-01).** At the 2026-09-28 power-on `dhcpcd` logged
`offered 192.168.0.101 from 192.168.0.254` (a 180 s lease, default route via .254), renewed it with the route via
192.168.0.41, and only ≈7 min later `offered 192.168.0.13 from 192.168.0.1` (28,800 s). The 8747 boot (a reboot, the
powerline already up) leased .13 from .1 directly. 192.168.0.254 does not answer now (ARP incomplete); .41 is the
TP-Link (`0C:EF:15`, HTTP 200). TP-Link powerline/extender units default to 192.168.0.254 while they boot, so .254 and
.41 are **likely** the same TL-WPA4220 — likely, not verified. Either way, a unit on the LAN serves DHCP for minutes
after a power cut.

**Listening ports** — tcp from a full connect scan of 1–65535 on 8747 (2026-10-01); the owners and the udp rows from
`/proc/net/*` socket inodes on 8530 (2026-09-12, re-read 2026-09-23) — not re-readable on 8747, except LSSDP's udp
1800, which answers:

| Port | Owner | Service |
|---|---|---|
| tcp 22 / 23 | — (dropbear / inetd through 8530) | **closed since 8747** — SSH / telnet (root) through 8530; the binaries are gone (§9) |
| tcp 80 | librewebserver | Web UI / API (a v6 socket; reachable over v4) |
| tcp 2018 | tcptunnelling | **control tunnel + LAN alert relay** (§6.3, §10.4); one established loopback client — `rakoit_app` (8530) |
| tcp 2345 · 44317 | rakoit_app | PlaylistServer (`playlist_addr`, bound 0.0.0.0) · a second, dynamic-port listener that moves whenever the app restarts (43761 → 33719 → 46835 → **44317** on 2026-10-01; HTTP 404). Neither answers a now-playing route (§8.7) |
| tcp 5037 (lo) / 5555 | — (adbd through 8530) | **closed since 8747** — ADB (local / **network**) through 8530 |
| tcp 7000 | airplaydemo | AirPlay (RTSP/control) |
| tcp 7777 | luciserver | LUCI control |
| tcp 9095 **or** 9096 | spotifymusicpro **or** newspotifyhifi | Spotify ZeroConf — **per engine**: Pro `:9095`, HiFi `:9096` (HiFi sat on 9095 before 8530); on 8747 9095 is open and 9096 closed (HiFi not running); §8.1 |
| tcp 49494 | dmr | DLNA control + the UPnP `description.xml` (§8.7) |
| udp 68 / 123 | dhcpcd / ntpd | DHCP / NTP |
| udp 1800 / 1900 | luciserver / dmr | **LSSDP** discovery (below) / SSDP |
| udp 3721 | airplaydemo | LinkPlay-style UDP discovery |
| udp 5353 | mdnsd **and** the Spotify engine | mDNS (the eSDK runs its own responder) |
| dynamic udp | mdnsd · airplaydemo · dmr | — |

`WACServer` (Apple WAC setup) runs but **no longer binds tcp 5000** since 8530 (it did on 9243).

**LSSDP** (udp 1800, answered by `luciserver`): an SSDP-style `M-SEARCH` to the box (or the multicast group) gets
`HTTP/1.1 200 OK` with `USN:<wlan MAC, no colons>` · `Version:LSSDP 1.0` · `FWVERSION:AR241CP_8747.29.2` (8530:
`AR241CE_8530.23.2`) · `CAST_MODEL:LP10` ·
`PORT:7777` · `TCPPORT:2020` · `DeviceName:<device-name>` · `State:S` · `NETMODE:ETH0` · `WIFIBAND:ETH` ·
`SPEAKERTYPE:Wireless Speaker` · `SOURCE_LIST:LS8::01000030` · `MRAMode:DDMS` — unchanged by 8747 but for the build.
Pure UDP, no auth — the liveness, discovery and firmware probe `lp10` uses (Appendix B).

**USB-C device-gadget** (`S89usbgadget`): plugged into a host, the LP10 presents a composite
**RNDIS network (`usb0`=192.168.5.1) + ADB** gadget (VID `0x18D1` Google / PID `0x4e26`) — the
service/debug path. **On 8747** the script comments out `usb_net_ipconfig`, so the RNDIS gadget still enumerates with
no IP, and it still calls `adbd`, which the build deleted. Arylic also lists USB-C as **"PC audio"** (a USB-audio
gadget function); `snd_usb_audio` wasn't a loaded module on 8530, so that UAC path wasn't separately confirmed (8747
loads `snd-usb-audio` at boot — §14.5).

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
| **HiFi** ("legacy") | `newspotifyhifi` / `libspotifyhifi.so` | **3.203.239-g1d6bd565** (unchanged 9243 → 8530 → 8747) | SRV **9096**, `version 2.9.0` (9095 before 8530) | Ogg/Vorbis + AAC; its softvol volume path always worked on 8530 and earlier; never ran on 8747 (below) |
| **Pro** ("new") | `spotifymusicpro` / `libspotifypro.so` | **3.216.31-g317ae1c7** on 8747 (3.211.130-g110e3e03 on 8530, 3.205.187 on 9243) | SRV **9095**, `version 2.10.0` (9095 on 8747 too) | the only engine that negotiates **FLAC**; its volume bypassed the softvol right after 8530 — fixed by `rakoit_app` v32 (§10.2); on 8747 the app's volume no longer reaches the softvol (below) |

**This unit:** HiFi from June to 2026-09-04; **Pro since 2026-09-04 15:06**, switched from `lp10`'s services pane (§14.3).
The pair held through the 8747 OTA: the 2026-10-01 env dump reads `SpotifyEnabled 0 / SpotifyProEnabled 1`, and 8747's
own factory pair is also `0/1`. The phone is only a remote — the **speaker** authenticates and pulls audio itself
(`Server: eSDK`; Ogg/Vorbis via `decoder_vorbis.c`, FLAC on Pro when the account allows).

- **Env-gated and mutually exclusive.** `S99newspotifyhifi` starts HiFi only when `SpotifyEnabled=1 && SpotifyProEnabled=0`;
  `S99spotifymusicpro` starts Pro only when `SpotifyProEnabled=1 && SpotifyEnabled=0`. Each script is guarded on the *other*
  flag being clear, so **both set = neither engine starts** — exactly what the 8530 OTA produced (factory default flipped
  `1/0 → 0/1`, the user's dirty `SpotifyEnabled=1` survived the merge, §5). `lp10` therefore always wrote the pair
  together (`1/0`, `0/1` or `0/0`) while it had ssh, and either pin persists across reboots (§5); since 8747 it writes
  no env at all. No CLI args — all config from env.
- **The web UI's Spotify switch knows only HiFi** (read from the 8747 bundle 2026-10-01: `index.asp`,
  `script_index.js`, and `librewebserver` disassembled; nothing sent to the box). The page shows `GetSpotifySupport()`,
  which reads `kFeatureSpotify` = **`SpotifyEnabled`** (resolved in `libenvitems.so`, where `kFeatureSpotifyPro` =
  `SpotifyProEnabled`); the web server never reads `SpotifyProEnabled`. So on a Pro box (`0/1`) the page says
  **"disabled" while Spotify plays**. Saving runs `GoForm_SetSpotifymode`: **enable** = `SetEnvItemByName(SpotifyEnabled,
  "1")` + `/etc/init.d/S99newspotifyhifi netup` — with Pro at 1 the HiFi gate fails, Pro plays on, but its own gate now
  fails too, so after the next `netdown` (a link drop) or a reboot **no engine starts** (the both-at-1 state above).
  **Disable** = `SpotifyEnabled=0` + `S99newspotifyhifi netdown` (kills HiFi only): a no-op on a Pro box. Both are
  refused while something plays ("Fail to apply Spotify Connect settings, playback is going on"). Recovery from an
  accidental enable: disable it again, then reboot the box if Spotify is already gone. The page cannot switch to HiFi
  (it never clears `SpotifyProEnabled`), and nothing on it touches the volume bug below: that lives in sourceswitch's
  shared `UpdateAppVolume`, not in the engine flags.
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
- **Engine reconnects (seen 2026-09-12, measured over three weeks 2026-09-23, not diagnosed).** The eSDK logs
  `The connection to Spotify has been lost` (`ConnectionNotify = 3`), each followed within seconds by a re-login with the
  stored credentials (`ConnectionNewCredentials`) — 1 min to 1.5 h apart — while DLNA subscription renewals and ssh
  sessions on the same link stay up. The rotated syslog on flash (§11) shows it on **both** engines: HiFi 183 × in 61 h
  (2026-09-02 → 09-04, ≈ 3/h); Pro 1.3–2.5/h since (102 × in 65 h, 203 × in 83 h, 124 × in 67 h, 141 × in 86 h, 151 × in
  115 h — 2026-09-04 → 09-23). So it is not a fault of the Pro engine; the cause is still unknown.
  **Studied 2026-09-23** over the whole retained history (950 losses, 2026-09-02 → 09-23): about 43 a day (10–100),
  no hour-of-day pattern (23–57 per hour of the day), gaps median 15.6 min (p10 2.5 min, p90 70 min; 76 under 2 min —
  bursts). Each loss is the same `ConnectionNotify` run — 3 (lost) → 7 → 2 → 7 → 4 — and the engine is back within
  1–6 s, with **no error line before it** (nothing from TLS, DNS or the link), and only 3 of the 950 fall within
  5 min of any network event the box logs (carrier, DHCP, IPv6 router advertisement). So the drop is not on the LAN
  side the box can see. The next step is off the box: a capture on the router of the access-point connection
  (104.154.0.0/15), or a second Connect device on the same network to compare. `lp10 sweep` printed the per-day counts
  while it had ssh; since 8747 they come from the web UI's log download (§15).
  **It continues on 8747 (the log download, 2026-10-01).** `has been lost` per day: 09-14 29 · 15 69 · 16 26 · 17 20 ·
  18 32 · 19 10 · 20 36 · 21 51 · 22 42 · 23 16 · 24 55 · 25 49 · (26–28 the box off) · 29 54 · 30 33 · 10-01 35 —
  every one from `spotify_pro`, the only engine that ran. Since the 8747 boot: 61 in ≈37 h (≈40 a day) — eSDK
  3.216.31 did not change the rate.
- **The Spotify app's volume does nothing audible on 8747 (the log download, 2026-10-01).** An app volume change runs
  `SPOTIFY_CONNECT: Spotify localvolume = 46` → `SourceSwitch::UpdateAppVolume iAppVolume = 46` → `Failed to set volume
  using amixer command: amixer -D softvoldefault sset 'Master' 46%` → luci writes MID 64 = 46 and sends `[LS->MCU] MB#
  64 RESPONSE 46` (so `STA` / `VOL` then report 46) → **no `processVolumeChange()`**. The register, the MCU and the
  Spotify slider all say 46; the softvol — the audible stage — stays where it was. Counts over the log: on 8530
  (09-13 → 09-30) 384 of 445 Spotify volume events were followed by `processVolumeChange() Applied volume: <same>`
  (recounted 2026-10-03 by event order; the first count, 340, looked only 30–40 lines ahead), and
  `UpdateAppVolume` ran 58 times with **0** amixer failures; on 8747 every app-originated change fails (13
  `UpdateAppVolume`, 16 amixer failures including `lp10`'s test sets) and none is applied — the only applied ones were
  `lp10`'s tunnel `VOL:` sets. The knob, the remote and a tunnel `VOL:` take the MCU path (§6.3) and still work. The
  HiFi engine on 8747 is untested (it never ran); `UpdateAppVolume` lives in sourceswitch, shared by both engines, so
  the same failure is likely but not established. **Workaround while `lp10` runs (2026-10-01):** its volume bridge
  re-sends as `VOL:n;` the first status reading of each connection and any later device-reported level it did not set.
  Verified live: the MCU pushes `VOL:n;` to every tunnel client when the Spotify app changes the volume (steps of 4
  while dragging), `lp10` re-sent each level within ≈0.1–0.2 s, with no feedback loop (each level seen exactly twice:
  the push, then `lp10`'s echo), and the user confirmed by ear that the room followed the Spotify slider. Only while
  `lp10` runs; the real fix is the vendor's.

**The two engines in the syslog (the log download, 2026-09-13 → 10-01).** The Pro engine logs as `spotify_pro`
(`SPOTIFY_CONNECT:` prefix, 31,406 lines), HiFi as `spotify` (`SPOTIFY:` prefix, 44 lines).

- **Pro ran the whole span.** Its sessions by PID: 5673 from the log's start → 09-17 14:21:07; 26906 09-17 14:21:38 →
  09-19 03:03:20 (eth carrier lost); 32197 09-19 03:03:31 → 03:07:53 (the .41 DHCP episode, §7); 1667 09-19 03:08:03 →
  09-24 13:46:33; 31564 09-24 13:46:41 → 09-25 15:42:33 (then the power loss); two short ones at the 09-28 boot (eth
  drop / DHCP churn); 4523 09-28 23:59:35 → silent after a lost connection at 09-30 07:28, ended by the OTA reboot;
  1321 on 8747 from 09-30 07:57:22, still running at the download. No crash or segfault line for either engine.
- **HiFi ran only twice, ≈1 s each, both on 8530:** 09-17 14:21:37 (PID 26879) and 09-24 13:46:40 (PID 31538). Each
  time the running Pro process stopped mid-activity 7–30 s before (14:21:07 while resuming audio; 13:46:33 while
  handling a volume change); HiFi logged only its init (`initSpotifyLibrary`, `startSpotifyLogin`, `first start`,
  waiting for an active source — no login, no playback), and a new Pro process started 1 s later. The pattern matches a
  cycle Pro → off → HiFi → Pro (what `lp10`'s 8530 services pane did with `setenv` + an init `netready`), but the log
  holds no ssh, `setenv` or env line at those moments: **the trigger is not established.**
- **On 8747 HiFi never started** (`ps` lists only `spotifymusicpro`; `:9096` closed; flags `0/1`). `lp10` can no
  longer switch engines (it needed `setenv` over ssh); whether the web UI can is not established.
- **What actually played:** the now-playing JSON (MID 42 on 8530, `UIframework`'s PlayView on 8747) shows the Pro
  engine playing **Ogg Vorbis, 16-bit / 44.1 kHz, 320 kbps** — 5,172 records on 8530, 6 of 8 on 8747 (the other 2
  `Unknown`, bitrate 0); 24 records at 160 kbps on 09-25; `setSampleFormat` always 44100 Hz / 2 ch / 16 bit. **No FLAC
  record in 17 days**, so the Pro engine's lossless path was never used here; why (account, app quality setting,
  rollout) is not established from the log.

**Proven socket map** (fd → `/proc/net/tcp`, HiFi engine, one capture):

| fd | Target | Meaning |
|---|---|---|
| 6 | LISTEN on the engine's ZeroConf port (table above) | ZeroConf HTTP endpoint (= advertised SRV port) |
| 8 | → Google Cloud `:4070` | **Spotify control / access-point** (AP ports 4070 / 443 / 80 — `:80` on 2026-09-23) |
| 11 | → Akamai `:443` | **audio CDN stream** (present only while playing) |
| 9 / 10 | `controlC0` / `pcmC0D1p` | ALSA mixer / PCM out |
| 4 / 5 | `/dev/binder` / `/dev/urandom` | Android IPC / TLS-DH entropy |

> Two **device-owned** connections, exactly as Spotify Connect prescribes. Idle: control to an
> AP `…:4070`, no audio socket. Playing (verified): control `…:4070` **+** audio CDN `…:443`
> (Akamai), now-playing `Mime: Ogg`, `SampleRate: 44100`. (AP/CDN IPs vary per session.)

### 8.2 AirPlay 2
`airplaydemo -n -d`, built from Apple's **AccessorySDK** with **PTP** timing (`libre_airplay_v2`).
Ports 7000 (RTSP/control) and udp 3721; the WAC setup port 5000 (`WACServer`) has not been bound since 8530. Apple Home / access control + AirPlay
password (env `AirPlayHomeAccessControlEnabled`, `AirPlayAppleHomePassword`, `AirPlayMetaData`). 8747 keeps
`AirPlay/366.0` (TXT `srcvers=366.0`); its production build strips the self-test code (`airplaydemo` 4.98 → 1.90 MB).

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
`DMREnable`/`DMPEnable`. **Live on 8747 (2026-10-01):** `http://<device-ip>:49494/description.xml` names friendlyName
`Living`, Arylic, LP10, modelNumber 2.0, "C4A Wireless Speaker", with AVTransport, RenderingControl and
ConnectionManager. RenderingControl `GetVolume` returns the same level as the tunnel's `VOL` (83 at the time), but
AVTransport stays `STOPPED` with empty metadata while Spotify plays — it covers DLNA sessions only. `rakoit_app` is itself a UPnP client of this renderer (its
`/upnp/control/rendertransport1`), and its `:2345` PlaylistServer and dynamic port answer no now-playing route.

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

**Security posture — through 8530 the LAN management surface was wide:**
- **Telnet (root) :23**, **SSH (root) :22**, **network ADB :5555** — all reachable on the LAN;
  root login enabled (`/etc/passwd`: `root:…:/bin/sh`). Telnet and SSH ask for the root password. **ADB does
  not**: `adbd` (android-tools 4.2.2, no `adb_keys`) answers a bare `CNXN` with `CNXN` and the build-template
  banner `ro.product.model=Nexus 4` (verified 2026-09-13 with a 24-byte handshake) — `adb connect
  <device-ip>:5555; adb shell` is a **root shell for anyone on the LAN**, Wi-Fi PSK and Spotify blob included.
- Web UI creds in env (`Defaultwebuserpassword`, `Newweb*`).
- `androidboot.selinux=enforcing` is in the cmdline but **SELinux is not active** (no
  `selinuxfs`) — inherited Android boilerplate *(verified on 8530 over ssh; not re-readable on 8747)*.
- Secret env values **never read** (key names only): `SP_BLOB`/`SP_USERNAME`,
  `AlexaRefreshToken`, `Airable{Auth,Secret}`, `*AppSecret`, `*Password`, Wi-Fi PSK.

**Since 8747 (the production build, bundle diff + port scan, 2026-10-01): remote access removed.** Deleted from the
rootfs: `usr/sbin/dropbear`, `etc/init.d/S50dropbear`, `etc/dropbear`, `usr/bin/dropbearkey`, `dropbearconvert`,
`dbclient`, `ssh`, `scp`, `usr/sbin/telnetd` (and its `inetd.conf` line — `inetd` still runs, serving nothing),
`usr/bin/telnet`, `usr/bin/adbd` (`S89usbgadget` still calls it), `bin/su`, `bin/chmod`, `bin/chown`;
`S89usbgadget` comments out `usb_net_ipconfig`. tcp 22, 23, 5037 and 5555 are closed. The root hash in `/etc/shadow`
changed (whether the password changed: not established). There is no ssh server binary in the image, so no env flag or
web setting can bring ssh back on this build. **What remains:**
- the **serial console** — `ttyS0::respawn:-/bin/sh` (`inittab` unchanged), a root shell on the serial port with no
  login. Not used for this document: it needs the case opened, and the debug pads are not located. The console's baud
  rate was never read (115200 is the Amlogic default; not verified here);
- **`bluetoothd -n -d`** — still in debug mode: `S44bluetooth` drops `-d` only on a defconfig tagged `nodi`, and the
  production defconfig is not;
- the **`:2018` tunnel** — player, volume, mute and EQ for anyone on the LAN, no auth (§6.3);
- the web UI's **local OTA upload** (`index.asp` LocalOTA forms). `ota` passes no `-N` (no-downgrading) to swupdate in
  its format strings, but whether a downgrade to 8530 would be accepted is not established (not tried);
- the web UI's **log download**, which writes the whole env store, secrets included, into the syslog it serves (§10.4).

> Fine as a trusted-LAN appliance. Through 8530 the open root telnet/ADB/SSH was a real exposure — ADB to every guest
> and gadget on the same Wi-Fi; 8747 closed those three. Client isolation / an IoT VLAN is still the router-side fix for
> what remains; closing ports is a device write, which this doc never does (§15).

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
MsgBox 223 (98 such reports in `/lsync/app.log` between 2026-08-25 and 09-12, 167 by 09-23). It also warns `GoogleCast is
set to False on a CAST product` on every check. Related env, all new in 8530: `OtaUpdateSchedule`, `OtaSkipCount`,
`OtaSkipEnableMask`, `OtaAckTimeout`, `OtaStandbyUpdate=0`. **The 8747 install, from the box's own log** (§14.5): the
timer is 14,400 s, the first check 5 s after boot; the 2026-09-30 07:52:43 check found the update, logged `Device going
for Periodic Upgrade!!! Current Playback State = 1` / `CurrentPlayBackState: 'Stopped'` before it started — the OTA
installs only when playback is stopped — and the box ran 8747 by 07:57; every 4-hourly check since answers `Idle`. On
8747 `ota` first tries a key from unifykey `ota_key` (written to `/tmp/swupdate-public.pem`) and falls back to the
unchanged `/etc/swupdate-public.pem`.

**Where the verdict survives.** The syslog's `error string` line rotates out of the live file within half an hour of
playback (§11); the MsgBox-223 report stays in `/lsync/app.log` for weeks —
`[2026-09-23 14:56:15.634] [DEBUG] [luci-rx] … command=223 … payload="NO_UPDATE"`, at xx:56 every 4 h this boot. `lp10`
read the box's own verdict from there over ssh from 2026-09-23 (the last 256 KiB of the file: 0.03 s on the box, against
0.36 s for the whole 4.7 MB) until 8747 removed ssh; now the OTA lines are read from the web UI's log download (§15).

**Manifest API (probed read-only 2026-07-01, re-probed 09-02 and 09-12).** `/v1` is a **Rust/axum + serde** JSON
endpoint — **`POST`** only (`GET` → 405), `Content-Type: application/json`, body
`{"device":{"brand","deviceId","fwVersion","model", …}}` (extra keys ignored; a synthetic `deviceId` is accepted — **no
device auth**; the client's mTLS is not required). Up to date → `{"errorCode":1001,"errorString":"No update available"}`;
an **older `fwVersion`** → `{"errorCode":1000,"errorString":"SUCCESS","url":"…","version":"AR241CE_8530","otapackage":"…",
"castVersion":"0.0.0","mcuOnlyUpdate":false}`. **As of 2026-10-01 the newest build is `AR241CP_8747`:** an older
`fwVersion` (a synthetic `AR241CP_1` or `AR241CE_0001`) is offered `lp10_AR241CP_8747_29_6701c857.swu`, and
`AR241CP_8747` gets `1001`. All three bundles are still on the CDN.

**The offers are counted per `deviceId` (measured 2026-10-03, §14.6).** One id is offered the bundle five times; from
its sixth old-build request on it gets `1001`, for every build it names (requests about the current build, which get
`1001` anyway, do not count). A fresh id sent in the same minute still gets
the offer, so the count is per id, not per address; both manifest hosts share it. Whether it resets (with time, or with
a new release) is not established. `lp10` sent the fixed id `lp10` until that day, so `lp10 sweep` read "none offered"
once the id was used up, and `u` would have called an outdated box current; it now sends a fresh id with every request
(Appendix B). The 09-23 reading that the manifest offered 8530 to no build at all (§14.4) is likely this quota — likely,
not verified.

**The bundle is public and unauthenticated** — `https://cdn.rakoit-ota.com/lp10/` (Cloudflare CDN, `accept-ranges: bytes`):

| Build | File | Size | Last-Modified | `mcu.bin` — outer-file offset · size · sha256 |
|---|---|---|---|---|
| `AR241CE_9243` / MCU 16 | `LP10_AR241CE_9243_16.swu` | 86,704,128 B | (still served) | 13,617,516 · 856,203 B · `04af7091…` |
| `AR241CE_8530` / MCU 23 | `lp10_AR241CE_8530_23_ca8e6abc.swu` | 89,751,552 B | 2026-08-20 07:55 GMT · etag `6a86b304-5598000` | 13,617,636 · 872,403 B · `d98284c4…` |
| `AR241CP_8747` / MCU 29 | `lp10_AR241CP_8747_29_6701c857.swu` | 89,527,296 B | 2026-09-30 08:10 GMT · etag `6abcc3dc-5561400` | **13,631,972** · 874,755 B · `1d58029f…` |

Naming moved to lower-case plus a content-hash suffix with 8530. Shape (all three): a **double-wrapped cpio** — outer `070701`
cpio → single `software.swu` → inner `070702` (newc+CRC) cpio holding `sw-description` (+`.sig`) and the images.
`sw-description` (libconfig; 8530: `fwversion=AR241CE_8530`, `mcuversion=23`, `forceupdate=true`, `custversion=1`) lists
`rootfs.squashfs`→mtd8, `boot.img.encrypt`→mtd6, `dtb.img.encrypt`, `factory_customer.squashfs`→mtd7, **`mcu.bin`
(type=`mcu`)** and the script `update.sh` (byte-identical 9243 → 8530, a generic rootfs swap). 8747 ships a new
`update.sh` (542 → 5,478 B): a post-install **ECC check**, not a version check — it reads mtd6/7/8 whole with `dd`,
compares `ecc_failures` before and after, and on a read failure or new ECC errors reboots to reflash (a retry counter
in `/lsync`'s `retry_file.txt`, 3 tries at most). The `mcu.bin` offset moved in 8747: walk the cpio headers, never
assume a fixed offset. `mcu.bin` is
**unencrypted** and pullable with a single `Range` request (§10.3); the `type=mcu` handler streams it verbatim to the MCU
over the UART via XMODEM (§6.1).

### 10.2 The vendor app loader and `rakoit_app`

`/factory/custom/csys/bin/daemon` (signed, verified by `S99zcustomapp` against
`/etc/swupdate-public.pem`; rewritten by the 8530 OTA) is a curl-based **"rak-loader"** ("RAKOIT app-loader"): reads an
index from `https://cdn.rakoit-ota.com/download` (fallback `https://ota.rakoit.com/download`, override
`/lsync/daemon.ini`), downloads to a
`mkstemp` file, **verifies the binary before install** ("binary verify failed"), kills and
restarts the app. `/lsync/daemon.ini` is empty (URL default). `/lsync/app-0.json` records
`rakoit_app` version **42**, md5 `b1dadf70…`, installed **2026-09-17 02:57** (v32, md5 `9aa7f360…`, 2026-08-25 before
it) — the app updates on its own schedule, independent of the OTA and without a reboot *(the `/lsync` reads: on 8530
over ssh; not re-readable on 8747)*. **The index path, located 2026-10-01:** the loader builds `%s/%s/%s` = base /
model / file, so the index is `https://cdn.rakoit-ota.com/download/LP10/app-0.json` —
`[{"name":"rakoit_app","md5":"b1dadf70…","param":"","version":"42"}]`, the same md5 and version as on 09-23 — and the
binary is `…/download/LP10/rakoit_app` (7,199,780 B, md5 matches). (A bare GET of `…/download` answers 404 / 301.)

**v42 (2026-09-17)** is 7,199,780 B (v32: 6,532,124 B). New log modules: `[scheduled-playback]` (a timer — "no task is
enabled, timerfd released"), `[libre-progress]` (it drops a position report near the end of the track as wrong) and a
`report manager` that registers with the local LUCI server (`app_info … "ip":"127.0.0.1"`). Its URL strings name the same
providers as v32, plus a stray `http://192.168.1.10` (a developer LAN address; nothing connects to it). The app and the
Pro engine restarted together on 2026-09-19 ≈03:07, started by init (no `SSH_*` in their environment), in the same minute
that `/tmp/resolv.conf` was rewritten — two carrier drops and a lease from a second DHCP server on the LAN (§7);
the second listener moved to tcp 46835.

`rakoit_app` (v32: 6,532,124 B, static ARM Rust, stripped; `rustc 1.94.0`, tokio 1.47, hyper 1.7,
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

### 10.3 `mcu.bin` — the MCU firmware, reversed (v16 on 2026-07-01, v23 on 2026-09-02, v29 on 2026-10-01)

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

**What v29 changed (string-level diff against v23, 2026-10-01).** `mcu.bin` 874,755 B (v23: 872,403 B). The UART
command table (103 codes by this diff's count, in the same order), the 21-code and 11-code sub-tables and the `PEQ`
list (`Flat Classical Pop Jazz Rock Vocal`, then `Custom`) are **identical to v23**. The header grows by 16 bytes (byte
6 `0x03` → `0x13`), so the table moves from 0x79cb7 to **0x7a587**; the build stamp `Nov  9 2021 18:37:08` is the same,
and the hex token `4ef47210` → `1d316f0c` (it surfaces as `VER:29-1d316f0c-10`). Added: `FAV` in the key and panel
action list (between `EQ_6` and `LIGHT`); `KEY:FAV`, `KEY:NUM%d` and `KEY:RESET` now end with `;`; new `RAW:NEXT;` /
`RAW:PREV;` (the echo the tunnel now pushes after a skip, §6.3) with `FAV_PLAY:%d` and `GENERIC_FAV_…` near them;
`is_control: %d ms: %d`, `SUCCESS`, `log10`. Removed: `set pregain: %d.%d dB`, `[omited]`.

### 10.4 Telemetry, phone-home & the "remote relay" — audited 2026-09-13

**Method (read-only).** One snapshot of `/proc/net/{tcp,tcp6,udp,udp6}` with every socket inode mapped to its process;
a **25-minute sampler** listing every non-loopback, non-LAN endpoint every 2 s; `strings` over every running binary,
every `/etc/init.d` script and config for URLs and hostnames; the rsyslog configs, cron, the env flags (names and
boolean values only); the retained syslog (2026-08-27 → 09-13); reverse DNS / whois of each endpoint from the Mac.
Nothing was written; the vendor's hard-coded upload credentials found along the way were **not** copied anywhere.

**What actually leaves the LAN:**

| When | Process | Destination (host / owner) | What goes out |
|---|---|---|---|
| **Always** (one held socket) | `spotifymusicpro` → `libspotifypro.so` | `apresolve.spotify.com` → `ap.spotify.com` — an access point on **Google Cloud**, tcp **4070** (104.154.0.0/15; `:80` on 2026-09-23 — the eSDK picks among 4070 / 443 / 80) | The Spotify Connect session: your account, the device name (the FriendlyName, i.e. the room), brand/model, what plays, the eSDK's `EsdkPlaybackStats`. Inherent to Connect. Art and audio come from `i.scdn.co`, `audio-ak.spotifycdn.com`, `tts.spotifycdn.com`, `proxy-url.spotify.com` (syslog) |
| **Every 4 h** | `ota` (libcurl, mTLS with the device cert) | `lp10.arylic.rakoit-ota.com` — **Vultr, US** (45.32.73.238; same box as `ota.rakoit.com`) | `POST {"device":{"brand":"Arylic","buildInfo":"AR241CE","castVersion":"0.0.0","custVersion":"2","deviceId":"<eth MAC>","deviceType":"LS8_C4A","fwVersion":"8530","googleCast":false,"mcuVersion":"23","model":"LP10","serialNumber":"<serial>"}}` — the unit's identity and firmware; no name, SSID, IP or usage |
| **Every 10 h** | `rak-loader` (`/factory/custom/csys/bin/daemon`) | `cdn.rakoit-ota.com/download` — **Cloudflare** (fallback `ota.rakoit.com`) | `curl -Lsk` GET of the app index: nothing but your public IP and curl's User-Agent. `-k` = no TLS verification; the fetched app is checked against a baked-in key before install (§10.2) |
| Continuous | `ntpd` | `{0,1,2,3}.pool.ntp.org` | time |
| At net-up | `timesync` | `www.google.com` (HEAD; reads the `Date:` header) | nothing but the IP |
| Per lookup | libc resolver | **whatever DHCP hands out** — here 1.1.1.1 / 8.8.8.8 / 1.0.0.1 + the ISP's | the hostnames above. Router policy, not the box's |

Over the 25-minute sample the **only** non-LAN socket was the Spotify access point (held the whole time); across 17
days of retained syslog the only other non-LAN traffic is the OTA check and the loader fetch on their timers.

**What does *not* happen (checked, not assumed):** no `cron`/`at`; `rsyslog` writes files only — no `@host`, no
`omfwd`, `/data/rsyslogdconf/` empty, the runtime conf is rotation (`/etc/log_rotation_script.sh`, local); **the
":2018 remote relay" is a myth** — `tcptunnelling` is a boost.asio TCP *server* that fans LUCI alerts out to whoever
connects, with no outbound connect and no relay host in its strings, so **the Arylic app reaches the box on the LAN
only** (earlier revisions of this doc said otherwise); `IoTEnabled=false` and the generic
`openotaengine.libreiotcloud.com` OTA URL in `factoryEnv.conf` is overridden by `fwdownload_xml`; `AVSEnabled=0` with
`AlexaClientID` empty (never linked, the `avs-alexa-na.amazon.com` endpoints are dormant); `GoogleCast=false`, so
`CastUsageReport=true` and the Breakpad `crash_uploader` (`//third_party/castlite`, `clients2.google.com/cr/report`)
never run and `S84librecast_lite`'s `ping 8.8.8.8` / `curl -I google.com` loop never starts; `TuneInLoginStatus=Logout`
(the `report.core-api.tunein.com/report/{listen,stream}` calls in `UIframework` fire only while a TuneIn station plays);
Airable, Qobuz, Roon, Matter off; `libre_lft` is a factory-test tool nothing launches. `rakoit_app`'s hosts are its
providers (TuneIn, radio-browser, Qobuz, `api.sound-machine.com`) — none contacted with those services unused; the 398
`rsproxy.cn` strings are the Rust crate mirror it was built through, not an endpoint.

**LAN-only broadcasts** (anyone on the network, no auth): mDNS (`Living._spotify-connect`, `_airplay`, `_raop`),
LSSDP/SSDP (name, wlan MAC, firmware), the DHCP hostname (`dhcpcd -h Living`), the BLE GATT setup service — the room
name, the MACs and the firmware version are public to the household and its guests.

**The one thing to know about: the LibreWireless log report.** `system_monitor` (running since boot) is a Breakpad
crash collector and log-report agent. It listens on `/tmp/crash_socket` and on LUCI MsgBox **651**
(`LUCI_MESSAGEBOX_LOG_DUMP`). When triggered it runs **`GetAllENV`** — the *whole* env store: Wi-Fi PSK, Spotify
blob, web password, everything — plus `ifconfig`, `ps`, `logctrl -l`, `date`; tars `/var/log/syslog/` and
`/data/log/syslog/` (which carry the Spotify username and login blob in clear, §15) and `/tmp/cast/cast.log`;
writes `info.json` (serial, MAC, UUID, customer id, MCU version); AES-256-CBC-encrypts the bundle with a key the
strings suggest is SHA-256 of a **passphrase hard-coded in the binary**; and SFTPs it (libcurl + libssh2, **user and
password also hard-coded** — deliberately not reproduced here) to **`logs.librewireless.com`** (`sftp://…:53792`,
`/logs/LS8/`; AWS us-east-1, 3.232.244.152), retrying for a minute ("Terminating upload after 1 minute"). Triggers:
(1) a crash in any daemon linked with `libcrash_handler.so` — `airplaydemo`, `audionexus`, `system_monitor`, and
**everything that loads `libenvitems.so`**, the Spotify engine included; a crash may also `reboot`; (2) "User triggered
the log report using button" — a front-panel combo; (3) the web page's log download (`USBLOGS`); (4) MsgBox 651 from
any LUCI client — the LAN (:7777), the :2018 tunnel, the vendor app. **Status on this unit (2026-09-13): never fired.**
The retained logs show only its start-up lines (`Server listening..` on 2026-09-04 14:56); `/tmp/libre/logdump/`,
`/tmp/libre/system_monitor/`, `/data/libre/system_monitor/` are empty; `logpolicystate=false`. Armed, not active. As
the bundle is encrypted for the vendor, the concern is LibreWireless holding your Wi-Fi password and Spotify login
after a crash — not an eavesdropper. **On 8747** the SFTP uploader to `logs.librewireless.com:53792`, with its embedded
credential, is unchanged; `logpolicystate` is `true` in 8747's factory defaults and in the live env, and no binary,
library or script in the rootfs reads the key (what does, if anything: not established — §14.5).

**The web UI's log download writes your secrets into the log it serves (seen 2026-10-01).** A download from the web
page makes the box append `ps`, `ifconfig`, `date`, `logctrl --list` and a **full env dump, secrets included** (Wi-Fi
PSK, Spotify blob, web password …) to its own syslog, then serve the bundle — 472 such lines in the 2026-10-01
download (274 env, 119 `ps`, 54 `logctrl --list`, 24 `ifconfig`, 1 `date`). Anyone who holds a downloaded log holds
those secrets. On the web server `/logs` is a symlink to `/tmp/libre/logdump/`; `/logs/` itself redirects to
`index.asp`; whether a dump file is served without login is not established.

**The 8747 metrics uploader — dormant.** `system_monitor` grew a metrics server: it listens on the Unix socket
`/tmp/libre/metrics.sock` (fed by `metrics_client_test network_up` from `S98system_monitor` on each network event),
queues events in `/data/libre/metrics/metrics_state.json`, and POSTs them over mTLS (the device cert plus a unifykey it
writes to `/tmp/metrics_mtls_*`) to env `metrics_url`, with the MAC, serial, firmware, RSSI, free memory, storage and
interface. A cloud reply `action_request: ota_trigger` starts an OTA check. `metrics_url` is empty in both env files
and live, so it posts nothing ("Metrics URL or CloudAPI not configured"); who can set it is not established.

**Who ends up knowing what (normal operation):**
- **Spotify** — your account, that a device called *Living* (Arylic LP10) is online, and what it plays. Like any Connect speaker.
- **Arylic / RAKOIT** — that the unit with this MAC and serial is on its current firmware (8530 at the audit) at your public IP, every 4 h; a bare index fetch every 10 h.
- **LibreWireless** — nothing, unless the log report fires; then potentially everything in the env store. (On 8747 also the metrics uploader, if `metrics_url` is ever set.)
- **Google** — hosts Spotify's access point; sees one `HEAD` at boot. **Cloudflare / Google / the ISP** see the DNS names, because the router hands out their resolvers.
- **Nobody** — the SSID/PSK, the LAN layout, the logs, who is home.
- **Your own LAN** — through 8530 a root shell over ADB (§9); on 8747 the unauthenticated `:2018` tunnel (§6.3); the room name, the firmware.

**Hardening without writing to the box** (router-side; device-side env edits are writes and out of scope here):
block `logs.librewireless.com` by name if the log report must never succeed (it gives up after a minute); block
`*.rakoit-ota.com` / `*.rakoit.com` to stop the 4-hourly identifier beacon and the loader — you lose OTAs, though
`lp10 sweep` still asks the manifest from the Mac; put the LP10 on an IoT/guest network **with client isolation** so
phones and gadgets cannot reach adb :5555 / telnet :23 (through 8530) or the `:2018` tunnel (still open on 8747); never
port-forward to it; and treat a downloaded log as a secret (above). `lp10` itself sends the box only an allowlist of
tunnel commands — transport, volume, mute and EQ (Appendix B).

**Vendor daemon:** `/factory/custom/csys/bin/daemon` (signed, verified at boot by `S99zcustomapp` against
`/etc/swupdate-public.pem`) is the app loader of §10.2 — started as `daemon 127.0.0.1`, it talks LUCI locally and
fetches the index above (on 9243 it was a simpler glue that read a `daemon.ini` URL, "PROTOCOL v1.0", and reported
fw/version).

---

## 11. Lifecycle gotchas

- **Spotify session is network-event-driven, not boot-driven** (§8.1) — any link blip rebuilds it.
- **Wi-Fi wedge** (§7) — root cause of past stream drops; mitigated by wired eth0.
- **Volume mute** — the LUCI mute path restores the wrong level, so the ssh-era `lp10` muted with set-0 + restore (§6);
  the tunnel's `MUT:1;` is a real MCU mute that keeps the level, and it is what `lp10` sends since 8747 (§6.3).
- **Panel *content*** (now-playing text, MID 42) can be pushed by the host with a **`-remote`**
  write (§6.4); a plain `lp10`/app MID write is local-only and never reaches the MCU. **Volume is
  different** — the MCU *owns* it, so a host `-remote 64` is **inert**: the on-screen **volume bar
  only pops for the physical buttons/remote**, and lp10's plain volume write (ssh era) was audible (SoC/DSP)
  but left the remote/panel on a separate, stale value (§6). A tunnel `VOL:n;` goes through the MCU instead (§6.3).
- **The Spotify app's volume is inaudible on 8747** — it reaches the MCU register and the app's slider but not the
  softvol (§8.1). The knob, the remote and a tunnel `VOL:` work; `lp10`'s volume bridge covers the app while it runs.
- **A `:2018` connection can be accepted and never served** (2026-10-01): no reply for 27 s, while the next connection
  answered at once. Treat a silent connection as dead and reconnect; never open a burst of connections (§6.3).
- **eth0 lease is dynamic** — discover via mDNS (`<device>.local`, or the `_spotify-connect._tcp` instance named after the
  FriendlyName) / LSSDP / the powerline MAC; don't hardcode. After a power cut a second DHCP server (likely the
  powerline unit) can hand out a different address for minutes (§7).
- **Qobuz Connect intentionally OFF** — an OTA could flip `QobuzConnectEnabled`; re-check after
  firmware updates.
- **Both Spotify flags set = no Spotify at all** (§8.1) — the state an OTA's factory flip can leave behind; always write
  the pair.
- **A `setenv` sticks** (a dirty row, §5) — through reboots and, on the 8530 precedent, through the next OTA's factory
  flip; a flag you never touched follows the factory.
- **Power loss leaves little trace** — `reboot_mode=cold_boot` and everything under `/tmp` re-stamped. **`cold_boot` is
  not proof of a power loss:** the 2026-09-30 OTA's reboot logged the same flag (§14.5). The *live*
  syslog (`/var/log/syslog/messages.log` = `/tmp/syslog/…`, tmpfs) is lost, but it is capped at **1 MiB**: rsyslog's
  `$outchannel` hands each full file to `/etc/log_rotation_script.sh`, which gzips it into
  **`/data/log/syslog/messagesN.log.gz`** on flash and keeps 50 (433 … 482 here on 2026-09-23, back to 2026-09-01) — that
  history survives a boot (a boot's first file starts with a pre-NTP `Dec 31 21:00` stamp). How far back the live file reaches
  depends on playback: while a track plays, `luci_service` writes three lines a second (the MID-49 position write to the
  MCU), so the file rotates every 20–30 min; idle, one file spans days (the "~23 h" of 2026-09-12 was an idle stretch).
  So a power cut loses the lines since the last rotation: the 2026-09-25 → 09-28 power loss is bounded only to that gap
  (§14.5). `/lsync/app.log` (5 MiB, then `app.log.old`) keeps weeks. The rotated files carry the Spotify login blob like
  the live one (§15) — count, never print.
- **8747 can delete the flash syslog history.** `S01aSystemSanity` runs at boot and, when `/lsync` is more than 20 %
  used, runs `rm -rf /lsync/shared/log/syslog/*` (= `/data/log/syslog`); the rewritten rotation script deletes the oldest
  archives past a size cap (COUNT × 100 KB + 1 MB); and `system_monitor`'s new `clean` log-dump action runs
  `rm -rf /data/log/syslog/*`. Read the history (the web UI's log download) before it goes.
- **Dropbear stalls the ~5th ssh connection made within a few minutes** (8530 and earlier; 8747 has no dropbear) — batch
  reads into one session; the ssh-era `lp10` record loop held a single connection for exactly this reason.
- **One ZeroConf port per engine** — never cache 9095 / 9096; read the `_spotify-connect._tcp` SRV record.
- **Who started this daemon?** `/proc/<pid>/environ` of an init-started process has no `SSH_*`; one launched from an ssh
  session (a services-pane toggle) carries `SSH_CLIENT` / `SSH_CONNECTION` — how the 2026-09-04 engine switch was traced
  *(8530 and earlier; 8747 has no shell to read `/proc` from)*.

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
| Analog in / out | 3.5 mm, 1 Vrms each | ✓ out = the BP10xx MCU's DAC; the line-in's ADC is not established (the WM8904 is absent) |
| Optical out | up to 24-bit/192 kHz | ✓ SoC S/PDIF DAI → TOSLINK |
| USB-A | flash playback (≤1000 songs / 16 GB) | ✓ host port |
| USB-C | power **+ PC audio** | ✓ 5V/2A + RNDIS/ADB gadget (8747: no IP, no `adbd`); the "PC audio" UAC path wasn't separately confirmed (`snd_usb_audio` not loaded on 8530; 8747 loads it at boot) |
| Streaming | Cast / AirPlay 2 / Tidal / Spotify / Qobuz / DLNA | all present; **enabled now: Spotify, AirPlay 2, DLNA, BT**. Cast/Tidal/Qobuz/Roon/Alexa/Matter/QPlay installed but **off** (env-gated) |
| Power | USB-C 5 V/2 A | ✓ |
| In box | unit, manual, 3.5 mm aux cable, 2-to-1 RCA cable, PSU, USB-C cable, IR remote | — |

The interesting *deltas* are all "more than advertised": a fuller LibreWireless protocol set
(Roon/Alexa/Matter/QPlay) shipped but mostly disabled, the MCU-driven OLED + host-UI protocol,
the dual PKI, and — through 8530 — the wide root LAN management surface (§9).

---

## 13. Build provenance

Daemons built from LibreWireless's tree:
`…/buildroot-openlinux-2024r1-a113l-ad403-spk/output/libre_ls8_24G_v1_c4a_debug_release/build/
{spotify_hifi, libre_airplay_v2/AirPlaySDK, roon/raat-master, …}`. Platform **LS8**, buildroot
**openlinux 2024r1**, SoC **a113l-ad403** "spk", target **EVK**. Kernel 5.15.137
(`arm-none-linux-gnueabihf-gcc 10.3.1`, built 2025-12-24; the 8530 kernel is the same 5.15.137 rebuilt
2026-01-12 18:55 IST, Buildroot `2020.02.1-svn317`, build tree `QA_validation` instead of `Dev_validation`).
8747 is the **production** defconfig (`libre_ls8_24G_v1_c4a_production_release_defconfig`, 8530 and earlier
`…_debug_release`): kernel still 5.15.137 (banner builder `librebuildls10@libre-build`), Buildroot `2020.02.1-svn366`,
build date 2026-09-29. Spotify eSDK `v3.203.239` (HiFi) / `v3.216.31` (Pro, 8747; `v3.211.130` on 8530).

---

## 14. Firmware history

| When | Event | Firmware / MCU | Where |
|---|---|---|---|
| ≤ 2026-06 | the box as bought; June teardown | `AR241CE_9243.16.2` / **16** (`build_date 2025-12-24`, svn 312) | §14.1 |
| 2026-08-2x | the vendor OTA taken (bundle on the CDN 2026-08-20) | **`AR241CE_8530.23.2` / 23** (`2026-01-12`, svn 318) | §14.2 |
| 2026-08-25 | `rakoit_app` **v32** installed by its loader; box rebooted ≈16:36 | — | §10.2 |
| 2026-09-04 14:55 | **power loss** (cold boot); 15:06 Spotify switched to the **Pro** engine from `lp10` | — | §14.3 |
| 2026-09-12 | re-sweep — **no newer OTA**; manifest, CDN and loader unchanged | — | §14.3 |
| 2026-09-17 02:57 | `rakoit_app` **v42** installed by its loader (no reboot) | — | §10.2 |
| 2026-09-19 ≈03:07 | a network event restarts `rakoit_app` and the Pro engine (init, not ssh) | — | §14.4 |
| 2026-09-23 | re-sweep — no newer OTA; the manifest now offers 8530 to **no** older build (likely the `deviceId` quota, §14.6) | — | §14.4 |
| 2026-09-25 15:45 → 09-28 ≈23:57 | **power loss**, somewhere in that gap (when: not established); 8530 cold boot, first NTP sync 09-28 23:59 | — | §14.5 |
| 2026-09-30 07:52 → 07:57 | the vendor OTA, found by the box's own 4-hourly check (07:52:43, bundle on the CDN 05:10 local), downloaded in 36 s, installed 07:53:21, reboot after 07:55:18; 8747 up, NTP 07:57:14 | **`AR241CP_8747.29.2` / 29** (`2026-09-29`, svn 366, production) | §14.5 |
| 2026-10-01 | re-sweep without ssh — no ssh, telnet or adb on 8747; the tunnel becomes `lp10`'s only channel | — | §14.5 |
| 2026-10-03 | re-sweep — the box and the vendor unchanged; the manifest counts its offers per `deviceId` | — | §14.6 |
| 2026-10-06 | re-sweep — the box and the vendor unchanged; no restart seen since 10-01 | — | §14.7 |

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
  `ota.rakoit.com/download`) answers 404 / 301 to a bare GET; the exact index path is still unlocated *(located
  2026-10-01: `…/download/LP10/app-0.json`, §10.2)*.
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
  `/lsync/source_service` and `cache.redb` is stamped 14:56. *(2026-10-01: an OTA's reboot logs the same `cold_boot`
  — §14.5 — so the flag alone does not prove this power loss; no OTA ran on 09-04, the build stayed 8530.)*
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
  syslog is gone); noted, not diagnosed. *(2026-09-23: the syslog was not gone — it rotates onto flash — and HiFi did
  the same, at ≈ 3/h; §8.1, §14.4.)*

### 14.4 Re-sweep 2026-09-23 — vendor app v42, a silent manifest, and the syslog's history on flash

Routine re-scan: one `lp10 sweep` (its first saved baseline), three short **read-only** ssh passes a few minutes apart,
and ssh-free queries (mDNS, the `:2018` getters on one held connection, the manifest for three builds, CDN `HEAD`s).
Nothing was written to the device.

- **Identity — unchanged.** `AR241CE_8530.23.2` / MCU 23 (`VER:23-4ef47210-9`), `build_date 2026-01-12`, svn 318,
  kernel 5.15.137; the seven hashes of §14.3 (`luciserver 465c90d4…` … `factoryEnv.conf f11a5e69…`) all match. No reboot:
  the last boot is still the 2026-09-04 14:55 power-on (up 19 d). Tunnel values as on 09-02 (`MXV:100 EQE:0 EQS:0 BAS:0
  MID:0 TRE:0 VBS:0 VBI:50 BAL:0`), the same `PEQ` list; LSSDP `AR241CE_8530.23.2 · State:S · ETH0`.
- **Vendor app → v42** (2026-09-17 02:57, md5 `b1dadf70…`, sha256 `6ab0b1d0…`, 7,199,780 B) — §10.2. On 2026-09-19
  ≈03:07 a network event restarted it together with the Pro engine; both were started by init.
- **Spotify** — still Pro (`0/1`), eSDK 3.211.130, ZeroConf `:9095` `version 2.10.0`. Running: `airplaydemo bluetoothd
  dmr spotifymusicpro`. 33 env keys set at runtime.
- **Listeners** — tcp 22 23 80 2018 2345 5037 5555 7000 7777 9095 **46835** 49494; udp 68 123 1800 1900 3721 5353 + three
  dynamic. Off the LAN while playing: only the Spotify access point (104.154.127.247, **tcp 80** this session) and the
  audio CDN (Akamai `:443`).
- **The vendor** — 8530 is still current, but the manifest now offers it to no older build (§10.1; *2026-10-03: likely
  the per-`deviceId` offer quota, not a vendor change — §14.6*); the bundles on the CDN are unchanged. The box's own
  4-hourly check keeps answering `NO_UPDATE` (MsgBox 223, 167 reports since 08-25).
- **The 09-19 restart, explained** (study, same day): two eth0 carrier drops at 03:03 and 03:07, and in between a
  180 s lease of 192.168.0.100 from a second DHCP server — a TP-Link unit at 192.168.0.41, probably the powerline
  extender (§7).
- **The reconnects, studied:** 950 losses in 21.8 days, no hour-of-day pattern, no error line before a loss, and only
  3 within 5 min of any network event the box logs (§8.1).
- **The syslog has a history.** The live file is capped at 1 MiB and rotated, gzipped, into `/data/log/syslog/` (50
  files, 4 MB, back to 2026-09-01) — §11. While a track plays it rotates every 20–30 min, which is why the sweep read
  0 reconnects and no OTA line from the live file alone. The history answers §14.3's open question: the eSDK reconnects
  on **both** engines — HiFi ≈ 3/h, Pro 1.3–2.5/h (§8.1).

**Net effect on `lp10`:** the `@@o` digest now takes the box's own firmware verdict from `/lsync/app.log`'s MsgBox-223
report instead of the syslog, and `lp10 sweep` counts reconnects over the rotated history and says when the vendor names
no package. *(Both needed ssh and ended with 8747 — §14.5.)*

### 14.5 AR241CP_8747.29.2 — the 2026-09-30 OTA, re-swept without ssh (2026-10-01)

**No ssh** — this build removed it (below). Four sources, all read on 2026-10-01: **live LAN probes** (≈20:30–21:15 -03:
LSSDP, mDNS, a TCP connect scan of every port, the `:2018` getters on one held connection, the UPnP description, the
Spotify ZeroConf, the manifest and the CDN); a **static diff of the two vendor bundles** (the encrypted `boot.img` /
`dtb.img` — the kernel and initramfs — are outside it); the box's own **log download** from the web UI (made by the user
at 21:10 -03: 53 MB, 698,481 lines, 09-13 21:51 → 10-01 21:10, box-local stamps, the pre-NTP epoch reading `Dec 31
21:00`); and a **live tunnel test** while Spotify played (≈20:47, its audible steps approved by the user). The only
writes were that test's `POP`, `NXT`, `VOL` and `MUT` (the mute set back with `MUT:0;`) and, at 21:2x, `lp10`'s volume
bridge re-sending the Spotify app's levels.

#### Identity, build and the OTA

- **Identity.** LSSDP `FWVERSION:AR241CP_8747.29.2`, AirPlay TXT `fv=p20.AR241CP_8747.29.2`, `srcvers=366.0`; LSSDP
  otherwise unchanged (`PORT:7777`, `TCPPORT:2020`, `NETMODE:ETH0`, `MRAMode:DDMS`, `SOURCE_LIST:LS8::01000030`). MCU
  `VER:29-1d316f0c-10` (API level 10, was 9). The build prefix moved from `AR241CE` to `AR241CP`; `lp10` parses both.
- **Build.** `etc/fwVersion.conf`: `build_date = "2026-09-29"`, `app_svn_version = "366"` (8530: 318, 2026-01-12), new
  `ext_svn_version = "4958"`, **`defconfig = "libre_ls8_24G_v1_c4a_production_release_defconfig"`** (8530:
  `…_debug_release_defconfig`). Kernel stays 5.15.137 (banner builder `librebuildls10@libre-build`); `os-release`
  `2020.02.1-svn366`. 345 files differ by sha256, most of them rebuilds (133 in `usr/bin`, 96 in `usr/lib`, 57 in
  `amlogic/modules`).
- **The OTA, from the box's own log.** The `ota` timer is 14,400 s, the first check 5 s after boot. **2026-09-30
  07:52:43 -03**: the check logs `Device going for Periodic Upgrade!!! Current Playback State = 1` /
  `CurrentPlayBackState: 'Stopped'` — the OTA installs only when playback is stopped — then `UpdateAvailable` → Download
  (`swupdate --recovery -b "0 1 2 3 4 5 " -k /etc/swupdate-public.pem -d '-u https://cdn.rakoit-ota…'`, 89.5 MB in
  36 s) → 07:53:21 Install (`swupdate -k /etc/swupdate-public.pem -i /data/software.swu -l 6 -L`) → 07:55:18
  WaitForReboot → reboot → 8747 up, NTP 07:57:14, first check 07:57:18 `Idle`; every 4-hourly check since: `Idle` (and
  one more at 10-01 21:10:39, outside the cadence, 8 s after the log download's web session opened — also `Idle`; what
  triggers it is not established). The
  bundle's CDN Last-Modified is 08:10 GMT = 05:10 -03, so the 03:52 check found nothing and the 07:52 one did. The Pro
  engine's session had gone silent after a lost connection at 07:28; the OTA reboot ended it.
- **`cold_boot` after an OTA.** The OTA's reboot logs `reboot_mode=cold_boot` — the flag a power-on sets — so
  `cold_boot` alone does not prove a power loss (§3, §11; §14.3's 09-04 reading leaned on it).
- **The power loss before it.** The log's previous boot is an 8530 cold boot whose first NTP sync is 09-28 23:59; the
  line before it is 09-25 15:45, the box playing. The live syslog since the last rotation is lost on a power cut (§11),
  so the power went somewhere between 09-25 15:45 and 09-28 ≈23:57 — when, not established.
- **Manifest and CDN.** `AR241CP_8747` → `1001`; an older build (`AR241CP_1`, `AR241CE_0001`) is offered
  `lp10_AR241CP_8747_29_6701c857.swu` (89,527,296 B, etag `6abcc3dc-5561400`, `Last-Modified: Wed, 30 Sep 2026
  08:10:04 GMT`); the 8530 and 9243 bundles are still served (§10.1). The vendor-app index (`…/download/LP10/app-0.json`,
  located this pass — §10.2) names `rakoit_app` v42 with the same md5 as on 09-23.
- **Bundle.** The same double-wrapped cpio. `sw-description`: `version 1.0.1`, `fwversion=AR241CP_8747`,
  `mcuversion=29`, `forceupdate=true`, `custversion=1`, the same six files. `mcu.bin` = MVSilicon `BT_Audio_APP` as
  before, now at outer offset **13,631,972**, 874,755 B, sha256 `1d58029f…` (§10.3's single-`Range` recipe needs the
  new offset). `update.sh` is new (5,478 B, was 542 B) — a post-install ECC check, not a version check (§10.1).

#### What the bundle diff shows (8530 → 8747)

- **Remote access removed** — `dropbear`, `telnetd` and `adbd` with their tools, `su`, `chmod` and `chown`; no `usb0`
  IP; the root hash changed (§9). What remains: the serial root console, `bluetoothd -n -d`, the unauthenticated
  tunnel, the web UI's local OTA upload and log download (§9).
- **New init scripts.** `S01aSystemSanity` (at boot, when `/lsync` is more than 20 % used: `rm -rf
  /lsync/shared/log/syslog/*`, the flash syslog history — §11); `S38io_handler` (a GPIO/LED handler, gated off by env
  `iohandler=0`); `S98system_monitor` replaces `S99system_monitor` and also runs `metrics_client_test network_up` on
  `netup` / `netready` / `netchange`. `S38messageboxhandler` now sends its output to syslog.
- **`system_monitor`** (179,152 → 236,888 B): the metrics server and mTLS uploader, **dormant** while `metrics_url` is
  empty (§10.4), and a `clean` log-dump action that deletes the flash syslog history. Unchanged: the SFTP log uploader
  to `logs.librewireless.com:53792` with an embedded credential.
- **Log deletion paths** — `S01aSystemSanity`, the rotation script's new size cap (COUNT × 100 KB + 1 MB),
  `system_monitor`'s `clean` (§11). `tcptunnelling` no longer logs payloads.
- **Audio.** `etc/asound.conf`: the alsaequal state moves to `/tmp/.alsaequal.bin`; the dmixer `buffer_size` 4096 →
  16384; a new `loopbackbt` (snd-aloop) PCM for BT output. New kernel modules `snd-usb-audio`, `snd-usbmidi-lib`,
  `snd-hwdep` and `snd-rawmidi` (loaded at boot) and `snd-aloop` (how it gets loaded for `loopbackbt`: not
  established).
- **`ota`** tries a primary key from unifykey `ota_key` (written to `/tmp/swupdate-public.pem`) and falls back to the
  unchanged `/etc/swupdate-public.pem`.
- **Versions.** Spotify Pro eSDK 3.211.130 → **3.216.31-g317ae1c7** (HiFi stays 3.203.239); Qobuz Connect library
  1.0.0 → 1.1.0; `wpa_supplicant` 2.9 → 2.10; AirPlay stays 366.0 (self-test code stripped, 4.98 → 1.90 MB); BusyBox
  stays 1.32.0; `UIframework` adds Amazon Music endpoints; `libreraat` adds a FLAC decoder; `messageboxhandler` adds
  `connect_saved_network`, `get_network_info`, static IP and a preset restore; `gatt-server` adds `facresetrequest`
  (what it triggers: not established beyond its name); OpenSSL's default `MinProtocol` is TLSv1.2.
- **Factory env.** The rootfs copy flips `SpotifyEnabled` 1 → 0 and `SpotifyProEnabled` 0 → 1; `logpolicystate`
  `false` → `true` (no binary, library or script in the rootfs reads the key — what does: not established); new
  `metrics_url=""`, `OtaStandbyUpdate=0`, `bleencryptiontype=1`, `TidalGain`, `mcuusblist`, `MaxAuthTimeout`,
  `ListManagement` and the `SoundTrack*` keys; `GCASTVersion` `""` → `1.52.272222` in the customer copy.
- **MCU v29** — the command table, the sub-tables and the `PEQ` list identical to v23; adds `FAV`, `RAW:NEXT;` /
  `RAW:PREV;`, and `KEY:*` strings ending in `;` (§10.3).
- **Unchanged:** the factory web UI (`factory/web`, byte-identical), `logctrl`, `rsyslog.conf`, `inittab`,
  `/etc/swupdate-public.pem`.

#### Live surface (2026-10-01)

- **TCP** (connect scan, 1–65535): open **80 2018 2345 7000 7777 9095 44317 49494**; closed 22, 23, 5037, 5555 and
  9096 (the HiFi ZeroConf — that engine is not running). 44317 is `rakoit_app`'s dynamic listener (HTTP 404; 46835 on
  09-23) — §7.
- **The tunnel** — the getters, the pushes (`TIT`/`ART`/`ALB` in plain UTF-8, `PLA`, `VND`, `RAW:NEXT`, and `VOL` on a
  Spotify-app volume change), the verified actions and the `VOL` path: §6.3. `VER:29-1d316f0c-10`, `MXV:100`, the same
  `PEQ` list; one fresh connection was accepted and never served (§6.3).
- **UPnP** — `dmr`'s description, and an AVTransport that stays `STOPPED` while Spotify plays: §8.7.
- **Spotify ZeroConf** on `:9095`, eSDK 3.216.31.

#### Running state (the log download's own `ps` and env dump, 21:10:45)

- **Processes** — the 8747 list in §4: of the two Spotify engines, `spotifymusicpro` only; not running: `dropbear`,
  `telnetd`, `adbd`, `newspotifyhifi`, `qobuzConnect`, `tidalConnect`, `librecast_lite`.
- **Env** (non-secret keys): `SpotifyEnabled 0`, `SpotifyProEnabled 1` — the 8530 both-at-1 trap did not recur: the
  09-04 pin held through the OTA, and 8747's own defaults are `0/1` — `QobuzConnectEnabled 0`, `RoonEnable 0`,
  `SoundTrackEnabled 0`, `OtaStandbyUpdate 0`, `logpolicystate true`, `metrics_url` empty, `GCASTVersion
  1.68.cast_20240119_0202_RC07.599752810`.
- **`io_handler`** — `Feature IO_handler is not Supported!!!` (env `iohandler` 0): inert.
- **The download itself** appends `ps`, `ifconfig`, `date`, `logctrl --list` and a full env dump, **secrets
  included**, to the syslog it serves (472 such lines in this one) — §10.4.

#### Spotify on 8747

- **The app's volume is inaudible.** The app's level reaches the MCU register and the slider, but `UpdateAppVolume`'s
  amixer call fails and no `processVolumeChange()` follows: 0 amixer failures in 58 `UpdateAppVolume` runs on 8530,
  every app-originated change failed on 8747. The knob, the remote and a tunnel `VOL:` still work; `lp10`'s volume
  bridge re-sends the app's level within ≈0.1–0.2 s while it runs. Chain and counts: §8.1.
- **The engines.** Pro ran the whole span (09-13 → 10-01) with no crash line; HiFi ran twice for ≈1 s on 8530 (the
  trigger is not established) and never on 8747; the Pro engine played Ogg Vorbis 320 kbps, with no FLAC record in 17
  days — §8.1.
- **The reconnect churn continues** — 61 losses in ≈37 h since the 8747 boot (≈40 a day): eSDK 3.216.31 did not
  change the rate. Per-day counts in §8.1.

#### DHCP after the power loss

At the 09-28 power-on the box leased from 192.168.0.254, renewed via .41, and reached the router (.1) only ≈7 min
later; the 8747 reboot leased from .1 directly. .254 and .41 are likely the same TL-WPA4220 (likely, not verified) — §7.

#### What could not be re-read without ssh

Whether the running binaries match the bundle (§14.2's hash check; the read-only squashfs makes them the bundle's by
construction, §14.3, but no hash was read); the `rakoit_app` version installed on the box (the CDN index names v42);
`/lsync/app.log` (the MsgBox-223 verdicts, the vendor app's tunnel log); socket owners and the UDP listeners; the
softvol and ALSA state; `/proc`, `/sys`, `dmesg`, pstore; whether the root password changed. **The partial substitute
is the web UI's log download** (§15): the syslog history — here 09-13 → 10-01, with the OTA lines, the DHCP leases, the
engine sessions and every eSDK reconnect — plus `ps`, `ifconfig`, `date`, `logctrl --list` and the env store, secrets
included, so the downloaded file is itself a secret.

**Net effect on `lp10`:** it became tunnel-only (Appendix B) — one `:2018` connection for the player and the
equalizer; the title shows from the next track change on; no cover art, seek bar or position (the tunnel sends none); a
real `MUT` mute; the volume bridge for the Spotify app's volume. The services view, the logs view, night mode (the AED
DRC needed `amixer` over ssh) and bedtime are gone. `lp10 sweep` dropped its ssh reads: it scans the TCP ports and reads
the tunnel getters, the CDN app index and the UPnP description beside LSSDP, ZeroConf, the manifest and the newest
bundle; the syslog reconnect history and the box's own OTA verdict left with ssh — the log download is the manual
substitute.

### 14.6 Re-sweep 2026-10-03 — nothing changed; the manifest counts its offers per `deviceId`

Routine re-scan two days after §14.5: `lp10 sweep` twice (01:12 and 01:24 -03), 32 manifest requests by hand with
synthetic ids, CDN `HEAD`s, and a second read of the 10-01 log download (the same file; no new download). Nothing was
written to the device.

- **The box — unchanged.** `AR241CP_8747.29.2` / MCU `29-1d316f0c-10`; TCP 80 2018 2345 7000 7777 9095 49494 and the
  same dynamic 44317 as on 10-01; 22 / 23 / 5037 / 5555 closed; eSDK 3.216.31, ZeroConf 2.10.0; LSSDP `State:S · ETH0`.
- **The vendor — unchanged.** `AR241CP_8747` → `1001`; the 8747, 8530 and 9243 bundles on the CDN with the same size,
  date and etag; the app index still names `rakoit_app` v42 (same md5).
- **The manifest counts its offers per `deviceId`.** The first sweep printed `newest bundle: AR241CP_8747 → none
  offered` — yet a request by hand, with another id, got the offer. Measured: a fresh id asking about `AR241CP_1` got
  `1000` five times in 8 s, then `1001` from the sixth request on — and `1001` for `AR241CE_8530` too; a second fresh
  id, asked seconds later, still got the offer for both builds; a third asked about `AR241CP_8747` five times (`1001`
  each) and then still got the offer for `AR241CP_1`. So the count is of offers, per id: not per address, not per build.
  The id `lp10` sent (`lp10`) and the one in this document's recipes (`000000000000`) are both used up. Whether the
  count resets is not established — §10.1.
- **What it retracts.** §14.4's "the manifest now offers 8530 to no older build" was likely this quota — likely, not
  verified.
- **The log download, re-read.** What §14.5, §7 and §8.1 quote from it reproduces — the OTA timeline, both boots, the
  DHCP sequence, the engine sessions by PID, the reconnects per day, the now-playing records, the 8747 volume
  failures, the `ps` list, the non-secret env keys — except two counts. The 8530 volume events followed by an applied
  volume are **384** of 445 by event order (383 within 1 s; the first count, 340, looked only 30–40 lines ahead). The
  download's own dump is **472** lines, not 1,750 (that is every line from the web session's start at 21:10:31). One
  line not noted before: an OTA check at 21:10:39, outside the 4-hourly cadence (§14.5).

**Net effect on `lp10`:** every manifest request — `u`, and the sweep's two questions — now carries a fresh `deviceId`
(`lp10-<8 hex>`); the second sweep read the 8747 bundle again.

### 14.7 Re-sweep 2026-10-06 — nothing changed

Routine re-scan three days after §14.6: one `lp10 sweep` (11:45 -03, a fresh `deviceId` per manifest request since
§14.6) and four manifest requests by hand, each with a fresh id. Nothing was written to the device.

- **The box — unchanged.** `AR241CP_8747.29.2` / MCU `29-1d316f0c-10`, `STA` upgrading flag `0`; TCP 80 2018 2345 7000
  7777 9095 49494; 22 / 23 / 5037 / 5555 closed; eSDK 3.216.31, ZeroConf 2.10.0; LSSDP `State:S · ETH0`; the UPnP
  description, presets, sources and EQ settings as on 10-03.
- **No restart seen.** The dynamic `rakoit_app` listener is still on 44317, as on 10-01 and 10-03. That port moves when
  the app restarts, so the app has likely run since before 10-01 and the box has likely not rebooted — likely, not
  verified (a log download would settle it).
- **The vendor — unchanged.** Both manifest hosts (`lp10.arylic.rakoit-ota.com`, `lp10-ota.rakoit.com`) offer
  `AR241CP_8747` to an old build (`AR241CE_0001`) and answer `1001` for `AR241CP_8747`; the 8747 bundle on the CDN has
  the same size, date and etag; the app index still names `rakoit_app` v42 (same md5).
- **A recipe fixed.** §15's manifest request used the fixed id `000000000000`, which is past the offer quota (§14.6).
  Asked about 8747 it still reads `1001` correctly today, but once a newer build is out it would have read `1001` too
  and called 8747 current. It now sends a fresh id.

**Net effect on `lp10`:** none.

---

## 15. Verify it yourself (read-only)

Host `<device>.local` (or the current eth0 lease; the box's mDNS instance name is its FriendlyName, so `dns-sd -B
_spotify-connect._tcp local.` finds it when the hostname does not resolve). **Never write to the device; never touch
playback state** (it may be in use).

**Without ssh — 8747 and later** (what `lp10` and `lp10 sweep` do, Appendix B). On the tunnel, send getters only, on
**one** held connection, spaced; never `POP`/`NXT`/`PRE` (they act) nor anything on §6.3's never-send list. If the
first getter gets no answer within a few seconds, close and reconnect once (§6.3's accepted-never-served case) — never
a burst of connections.

```sh
# LSSDP (udp 1800): firmware, state, net mode — no auth (§7)
printf 'M-SEARCH * HTTP/1.1\r\nHOST:239.255.255.250:1800\r\nMAN:"ssdp:discover"\r\nMX:1\r\nST:ssdp:all\r\n\r\n' \
  | nc -u -w 2 <device-ip> 1800
# the :2018 getters, one connection, 200 ms apart (§6.3) — read-only codes only
{ for q in VER STA SRC LST MXV PEQ EQE EQS BAS MID TRE VBS VBI BAL; do printf '%s;' "$q"; sleep 0.2; done; sleep 2; } \
  | nc -w 3 <device-ip> 2018 | tr ';' '\n'
# the debug ports 8747 closed (§9): all four should refuse
nc -z -w 1 <device-ip> 22 23 5037 5555
# AirPlay TXT (fv= is the firmware) and the Spotify engine's ZeroConf (engine + eSDK build, §8.1)
dns-sd -L <device-name> _airplay._tcp local.
dns-sd -L <device-name> _spotify-connect._tcp local.          # then: curl "http://<device-ip>:<port>/zc?action=getInfo"
# the DLNA renderer's description (§8.7)
curl -s http://<device-ip>:49494/description.xml
# the vendor: the manifest verdict for the running build (§10.1), the app index the loader fetches (§10.2), the bundle;
# a fresh deviceId each time — the vendor offers a bundle to one id five times only (§10.1)
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"device":{"brand":"Arylic","deviceId":"probe'"$(date +%s)"'","fwVersion":"AR241CP_8747","model":"LP10"}}' \
  https://lp10.arylic.rakoit-ota.com/v1
curl -s https://cdn.rakoit-ota.com/download/LP10/app-0.json
curl -sI https://cdn.rakoit-ota.com/lp10/lp10_AR241CP_8747_29_6701c857.swu
```

**The web UI's log download** (the box's own "download logs", on `http://<device-ip>/`) is the one window into the
syslog, the process list and the env store without ssh — **and it embeds secrets**: the request makes the box append
`ps`, `ifconfig`, `date`, `logctrl --list` and the whole env store (Wi-Fi PSK, Spotify blob, web password) to its
syslog before it serves the bundle (§10.4). Treat the file as a secret; count and grep for key names on the Mac
(`LC_ALL=C grep -a -c 'has been lost'`, `grep -a 'Periodic OTA trigger'`, `grep -a 'offered'`), never print env or
`BLOB` lines, and never share it.

**Over ssh — 8530 and earlier** (8747 has no ssh server; kept as the record of how chapters 1–13 were read). Root
password in macOS Keychain (service `lp10`, account `root`); use `SSH_ASKPASS` (one-shot, no TTY needed). Batch reads
into one ssh session and space sessions a minute apart — dropbear stalls the ~5th rapid connection (§11).

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
sshx 'amixer -c0 scontrols'                                   # mixer (inert WM8904 driver + AED) — READ only
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
sshx 'grep -h "Periodic OTA trigger\|error string" /tmp/syslog/messages.log | tail'   # the box's own 4-hourly check (§10.1) — only if it has not rotated out
sshx 'grep -aF "command=223 " /lsync/app.log | tail -1'                  # the same verdict as the MCU heard it, kept for weeks (§10.1)
sshx 'for g in /data/log/syslog/messages*.log.gz; do echo "${g##*/} $(zcat $g | head -c 15) $(zcat $g | grep -c "has been lost")"; done'   # rotated syslog: first stamp + eSDK reconnects per file (§11) — counts only, the files hold the blob
sshx 'openssl x509 -in /factory/libre/luci/deviceCert.pem -noout -subject -issuer'
# what talks to the internet right now (§10.4): sockets whose peer is neither loopback nor 192.168.x — hex, little-endian
sshx 'cat /proc/net/tcp /proc/net/tcp6 /proc/net/udp' | awk '$3 !~ /^(0+|0100007F|0100A8C0|0+A8C0):/ && $3 !~ /A8C0:/'   # F77F9A68:0FE6 = 104.154.127.247:4070
sshx 'for p in /proc/[0-9]*; do for f in $p/fd/*; do l=$(readlink $f); case $l in socket:*) echo "${p#/proc/} ${l#socket:} $(tr "\0" " " < $p/cmdline | cut -c1-40)";; esac; done; done 2>/dev/null' | grep -v username   # inode → process
sshx 'grep -v "^\s*#" /etc/rsyslog.conf /tmp/rsyslogdconf/*.conf | grep -E "@|omfwd|omrelp"; ls /etc/cron* /var/spool/cron 2>&1'   # no remote syslog, no cron
sshx 'grep -a "payload =" /var/log/syslog/messages.log | tail -1'          # exactly what the 4-hourly OTA check sends
sshx 'strings -n 6 /usr/bin/system_monitor | grep -nE "sftp://|librewireless|GetAllENV|crash_socket"'   # the log-report pipeline — do NOT copy the credential strings beside these
# from the Mac: adb needs no key (brew install --cask android-platform-tools) — a root shell, so look, do not touch
adb connect <device-ip>:5555 && adb shell id && adb disconnect
# env KEY NAMES only — never dump values (SP_BLOB / *Secret / *Password / PSK):
sshx "sqlite3 /data/libre/env/env.db 'select key from ENV_systemENV order by key'"
```

> **Hygiene, learnt the hard way.** `grep -i spotify` over `/tmp/syslog/messages.log` returns the engine's
> `SAME USERNAME IS THERE STORE THE BLOB …` line — the reusable login blob and the account id in clear: filter
> `BLOB|username` *before* anything leaves the box. `/lsync/app.log` is partly Chinese — read it with `LC_ALL=C` on the
> Mac side or `sed` aborts. And the env DB holds the Wi-Fi PSK and the web password beside the flags — select key names,
> never `*`. On 8747 the same rules apply to the log download, which carries the env dump, Spotify blob included: it
> has already left the box, so filter on the Mac and keep the file private.

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
- **New in 8747's factory defaults (bundle diff, §14.5):** `metrics_url` (empty — the dormant uploader, §10.4)
  `ListManagement` the `SoundTrack*` / `ST*` keys (`SoundTrackEnabled 0` live)

---

## Appendix B — the `lp10` controller

The companion TUI (`~/code/lp10`, Go) is what most of this doc's live state was read through — over ssh through 8530
(the record loop of §6.2, retired), and since 2026-10-01, when 8747 removed ssh, over the LAN alone. Nothing logs in.
It writes the box only an allowlist of tunnel commands and reads everything else without auth:

1. **One `:2018` connection** (§6.3) carries the player and the equalizer. On connect it seeds the state with `STA;`,
   every EQ control, `PEQ;` and `VER;`, 150 ms apart; then it polls `STA;` every **2 s** (source, mute, volume and play
   state in one frame) and applies what the device pushes (`TIT`/`ART`/`ALB`, `PLA`, `VND`, `VOL`, the echoes) — except
   that a read carrying a track field drops the values `lp10` acts on (`VOL`, `MUT`, `PLA`, `STA`, `SRC`, the EQ
   values): the tunnel has no framing (§6.3), and the next poll brings them back; the `VND` word and the seed's `PEQ` /
   `VER`, which nothing re-reads, are kept. A change of input (`NET` → `BT`) drops the shown track and service. A link
   that delivers no frame for **6 s** is dead — which also covers the accepted-never-served connection — and it
   reconnects with backoff (250 ms, doubling to 3 s). One connection at a time, never a burst.
2. **One allowlist** (`tunnel.Wire`) for everything sent: the actions `POP` / `NXT` / `PRE` (bare, on a keypress only —
   never as a query), `VOL` and `MUT` sets, the EQ sets, and the read-only getters; any other code is refused before
   the socket. A command older than 4 s when the link comes back is dropped with a notice; a held volume key writes the
   newest level at most every 150 ms. The mute is the MCU's own (`MUT:1;`), so the level stays where it is.
3. **The volume bridge** (§8.1): the first status reading of each connection, and any later device-reported level
   `lp10` did not set itself, is re-sent as `VOL:n;`. So the Spotify app's volume — which 8747 no longer applies to the
   softvol — reaches the room through the MCU path while `lp10` runs (verified 2026-10-01: each level re-sent within
   ≈0.1–0.2 s, no feedback loop). A volume key cancels a level still waiting to be bridged, so the bridge never undoes
   a step taken in the first seconds of a connection.
4. **Two probes that need no tunnel** — the LSSDP responder (udp 1800, §7) for liveness and the firmware build, and the
   Spotify engine's ZeroConf `getInfo` (port from the `_spotify-connect._tcp` SRV record, §8.1) for "is the engine up,
   on which eSDK, signed in as whom" (when the engine names a user). The first probe of each always runs — the connect
   greeting (`connected · firmware AR241CP_8747.29.2 · MCU 29 · Spotify eSDK 3.216.31`) and the update check need it;
   after that, while connected, they run only while the diagnostics show their answers (every 30 s), and while
   disconnected every 5 s (LSSDP) and 10 s (ZeroConf), so the connecting screen can say whether the box is on the LAN.
5. **`u` in the diagnostics** asks the vendor's manifest whether the LSSDP build is current — the one request that
   leaves the LAN, and only on that keystroke; a verdict answers repeats for 30 min. Each request carries a fresh
   synthetic `deviceId`, because the vendor stops offering to an id after five offers (§10.1).
6. **`lp10 sweep`** (2026-09-12; without ssh since 2026-10-01) automates §14.5's inventory, diffed against the previous
   run's baseline in the state dir: a TCP connect scan of every port — 768 connects in flight with a 400 ms timeout,
   ≈25–35 s, because a closed port on 8747 refuses only after ≈1 s; at that rate the box drops a SYN to an open port now
   and then (the first saved 8747 baseline lacked 9095), so the known listeners and the debug ports are re-dialled one
   by one (1.5 s, twice) before the sweep calls them closed (the fixed ports diffed; ports in the ephemeral
   range 32768–60999, except dmr's 49494, shown as dynamic; 22 / 23 / 5037 / 5555 called out if they ever answer
   again), the tunnel getters on one connection (`VER` / `PEQ` / `LST` compared, the settings printed; never a set or an
   action), the UPnP description, LSSDP, ZeroConf, the CDN app index (`rakoit_app` version and md5), the manifest's
   verdict and the newest bundle (size, date, etag). The syslog's reconnect counts and the box's own OTA verdict left
   with ssh; the web UI's log download is the manual substitute (§15).

- **Discovery:** startup mDNS for the **`am=LP10`** advertisement resolves the current IP, with LSSDP as the fallback
  and the configured host after that — so a changed DHCP lease never needs a config edit.
- **Views** (one at a time, `1`–`3` / `tab`): *player* (now-playing, transport, volume and mute; the title shows from
  the next track change on — no cover art, no seek bar, no position), *equalizer* (the §6.3 controls), and
  *diagnostics* (`i`) — the tunnel's audio read-out, the connection (tunnel, host, LSSDP, ZeroConf), the device
  (firmware from LSSDP, MCU from `VER`, eSDK from ZeroConf, what moved since the last sweep, the vendor's verdict after
  `u`) and the model's static hardware facts from this teardown. The ssh-era services view, logs view, night mode
  (the AED DRC, switched with `amixer` over ssh) and bedtime are gone.
- **No credentials:** the ssh password, the askpass and the Keychain lookup went with ssh.
- **Source names:** the tunnel names the input in `STA` / `SRC` (`NET`, `BT`, `LINE-IN`, `USBPLAY`) and the service in
  `VND` (`spotify`). (The ssh-era MID-42 `Current Source` mapping — 1 AirPlay · 2 DLNA · 3 Bluetooth · 4 Spotify ·
  5 Line-In · 6 USB — was an app-side interpretation; only 4 was confirmed live.)

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

*Phone-home audit **2026-09-13** (§10.4, §9): five short read-only ssh passes — socket tables with inode→process
mapping, a 25-minute 2-second sampler of non-LAN endpoints, `strings` over every running binary and every init
script/config for URLs and hostnames, rsyslog/cron/env flags, the retained syslog 2026-08-27 → 09-13 — plus reverse
DNS / whois of each endpoint and a 24-byte ADB handshake from the Mac. Live traffic was Spotify alone; the tunnel
daemon's "cloud relay" role was retracted; the LibreWireless log-report uploader was found armed but never fired.
Nothing was written to the device; the hard-coded upload credentials in `system_monitor` were not copied anywhere.*

*Re-sweep **2026-09-23** (§14.4): one `lp10 sweep` plus three short read-only ssh passes — the syslog and its rotation
config, the rotated history on `/data` (counts only), `/lsync` and `app.log` (MsgBox 223, log levels), the vendor app's
URL strings and the live sockets — and ssh-free mDNS / `:2018` getters / manifest / CDN queries. Found `rakoit_app` v42
and a manifest that no longer offers 8530 to older builds; corrected §11's "syslog lost on boot". Nothing was written to
the device.*

*Re-sweep **2026-10-01** (§14.5): the box had taken `AR241CP_8747.29.2` / MCU v29 on its own on 2026-09-30 — a
production build with no ssh, telnet or adb. No shell pass was possible. Live LAN probes (LSSDP, mDNS, a TCP connect
scan of every port, the `:2018` getters on one held connection, UPnP, ZeroConf), the manifest and the CDN; a static
diff of the 8530 and 8747 bundles; the web UI's log download, made by the user (09-13 → 10-01; it embeds the env
store's secrets — only non-secret keys and counts are quoted here); and a live tunnel test while Spotify played. The only writes were that test's play/pause, skip, volume and mute (the mute set back) and `lp10`'s volume
bridge re-sending the Spotify app's levels. Found: the remote-access removal, the tunnel's pushes, the Spotify app's
inaudible volume on 8747, `cold_boot` after an OTA reboot, the vendor-app index path, a second DHCP server after the
09-28 power-on.*

*Re-sweep **2026-10-03** (§14.6): two `lp10 sweep` runs, 32 manifest requests with synthetic ids, CDN `HEAD`s and a
second read of the 10-01 log download. The box and the vendor were unchanged. Found: the manifest counts its offers per
`deviceId` (five, then `1001`), which explains the sweep's "none offered" and likely the 09-23 "silent manifest"; two
counts from the log corrected (§8.1, §10.4). Nothing was written to the device.*

*Re-sweep **2026-10-06** (§14.7): one `lp10 sweep` run and four manifest requests with fresh synthetic ids, two to each
manifest host. The box and the vendor were unchanged; the dynamic app port suggests no restart since 10-01. Fixed: §15's
manifest recipe, whose fixed id was past the offer quota. Nothing was written to the device.*
