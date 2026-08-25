# remote_loop.src.sh — readable source for the on-device BusyBox-ash streaming loop.
# Minified into remote_loop.sh (the //go:embed artifact) by `go generate
# ./internal/transport` (see mkloop.go / loopgen). EDIT THIS FILE, not remote_loop.sh;
# TestEmbeddedLoopMatchesSource fails if the generated file is stale.
#
# Minify contract (loopgen.Minify): whole-line '#' comments and blank lines are
# dropped, every other line is trimmed and the rest are joined with ONE space. So:
#   - comments must be on their own line (no inline trailing '#');
#   - each code line must end with its own ';' / ';;' or a keyword that continues the
#     next line (do / then / else / in);
#   - break a command across lines only at a real, out-of-quote separating space.
#
# Two placeholders are substituted at spawn by transport.RemoteLoop:
#   __PING_HOST__  diagnostics internet-ping target (already host-sanitised)
#   __MIDS__       whitelisted command-id alternation, e.g. 40|64
#
# Footprint note: this whole prologue runs ONCE per connection; the per-tick cost is
# in the `while :; do` loop far below. Parsing is shell parameter-expansion only (no
# awk/sed/grep), and scans break as soon as their field is found.

# ── ping target + firmware/name (LUCI regs 5/6/90) + cpu/kernel facts ──
ph='__PING_HOST__';
fw=$(LUCI_local -r 5 2>/dev/null); fw=${fw#*Data:}; fw=${fw%% *};
fv=$(LUCI_local -r 6 2>/dev/null); fv=${fv#*Data:}; fv=${fv%% *};
# FriendlyName may contain spaces, so strip the trailing " Length:N" instead of
# taking the first word; a failed read (no Data:) yields an empty name.
fn=$(LUCI_local -r 90 2>/dev/null);
case "$fn" in *Data:*) fn=${fn#*Data:}; fn=${fn% Length:*};; *) fn=;; esac;
nc=0;
while read -r l; do
  case "$l" in processor*) nc=$((nc+1));; esac;
done < /proc/cpuinfo;
read -r kt < /proc/sys/kernel/ostype;
read -r kr < /proc/sys/kernel/osrelease;
nl=$(printf '\nx'); nl=${nl%x};
cip=${SSH_CLIENT%% *};

# ── pg(): one ICMP ping — the avg RTT (ms) via shared $o ("-" on failure), plus the
# target's resolved IPv4 via shared $oip ("" when unparsed), taken from BusyBox
# ping's "PING host (ip):" header before the RTT parse consumes $o. The caller can
# pin a hostname target to $oip after the first success so later ticks never
# re-resolve DNS: -W bounds the reply wait, NOT resolution — a dying resolver
# would otherwise stall the loop for seconds per attempt.
pg() {
  o=$(ping -c1 -W1 "$1" 2>/dev/null);
  oip=${o#*\(}; oip=${oip%%\)*};
  case "$oip" in *[!0-9.]*|'') oip=;; esac;
  case "$o" in
    *"min/avg/max = "*) o=${o#*min/avg/max = }; o=${o%% ms*}; o=${o#*/}; o=${o%%/*};;
    *) o=-;;
  esac;
};

# ── @@i: pick the active interface from the default route, read its link details ──
gw=; dv=;
ir=$(ip route 2>/dev/null);
case "$ir" in
  *"default via "*)
    r=${ir#*default via }; r=${r%%"$nl"*}; gw=${r%% *};
    case "$r" in *" dev "*) dv=${r#* dev }; dv=${dv%% *};; esac;;
esac;
[ -z "$dv" ] && dv=eth0;
mac=; read -r mac 2>/dev/null < /sys/class/net/$dv/address;
ip=$(ip -o -4 addr show $dv 2>/dev/null); ip=${ip#*inet }; ip=${ip%%/*};
net=eth; sp=; dx=; ss=; fq=; rt=;
if [ -d /sys/class/net/$dv/wireless ]; then
  net=wifi; wl=$(iw dev $dv link 2>/dev/null);
  case "$wl" in *"SSID: "*) ss=${wl#*SSID: }; ss=${ss%%"$nl"*};; esac;
  case "$wl" in *"freq: "*) fq=${wl#*freq: }; fq=${fq%%"$nl"*}; fq=${fq%% *};; esac;
  case "$wl" in *"tx bitrate: "*) rt=${wl#*tx bitrate: }; rt=${rt%%"$nl"*}; rt=${rt%% *};; esac;
else
  read -r sp 2>/dev/null < /sys/class/net/$dv/speed;
  read -r dx 2>/dev/null < /sys/class/net/$dv/duplex;
fi;

# ── build/app/platform from fwVersion.conf (quoted values) ──
bd=; ap=; pf=;
while IFS= read -r ln; do
  case "$ln" in
    *build_date*\"*) bd=${ln#*\"}; bd=${bd%%\"*};;
    *app_svn_version*\"*) ap=${ln#*\"}; ap=${ap%%\"*};;
    *platform*\"*) pf=${ln#*\"}; pf=${pf%%\"*};;
  esac;
done 2>/dev/null < /etc/fwVersion.conf;

# ── resolver + /lsync usage, then emit the @@i key=value block ──
dns=;
while read -r dk dvv drest; do
  case "$dk" in nameserver) dns=$dvv; break;; esac;
done 2>/dev/null < /etc/resolv.conf;
# BusyBox df wraps a >20-char device name onto its own line, leaving a 5-field
# stats-only last line (total used avail use% mount) instead of 6 — pick the
# used/total columns by field count so a long mount source can't silently swap
# the data= reading into avail/used.
set -- $(df -k /lsync 2>/dev/null | tail -1);
if [ $# -ge 6 ]; then duk=$3; dtk=$2; elif [ $# -eq 5 ]; then duk=$2; dtk=$1; else duk=; dtk=; fi;
echo @@i;
printf 'net=%s\niface=%s\nip=%s\nmac=%s\ngw=%s\nspeed=%s\nduplex=%s\nssid=%s\nfreq=%s\nrate=%s\nbuild=%s\napp=%s\nplatform=%s\nname=%s\ndata=%s %s\ndns=%s\n' "$net" "$dv" "$ip" "$mac" "$gw" "$sp" "$dx" "$ss" "$fq" "$rt" "$bd" "$ap" "$pf" "$fn" "$duk" "$dtk" "$dns";
echo @@E;

# ── @@c capability block ──
# Two independent truths per service, because they diverge and the divergence IS
# the interesting fault: `<id>` is what is actually RUNNING (pidof), `<id>.env`
# is what the config SAYS. The device's own web page only ever reports the env
# flag, which is how an LP10 can advertise "Spotify: on" with no engine running
# at all (the AR241CE_8530 OTA left both Spotify flags set and the XOR-guarded
# init scripts started neither).
#
# gv() reads an env flag. getenv prints " [ KEY ]: value", so the value must be
# cut off the tail before it is compared — matching the raw line against bare
# "1" silently answered "off" for every flag, whatever its real value.
gv() {
  v=$(getenv "$2" 2>/dev/null); v=${v##*: };
  case "$v" in
    1|true|TRUE|True|on|ON|yes|YES) echo "$1=on";;
    '') echo "$1=";;
    *) echo "$1=off";;
  esac;
};
# pr() reports a running daemon. $2 is deliberately unquoted: BusyBox pidof takes
# several names, and a service may ship under more than one binary.
pr() {
  if pidof $2 >/dev/null 2>&1; then echo "$1=on"; else echo "$1=off"; fi;
};
# sy() reports the whole Spotify picture in one pass: whether an engine is up,
# WHICH of the two it is, its Spotify eSDK build, and the state of the env PAIR.
# The eSDK build is the only honest signal for the codec ceiling — both engines
# link libFLAC, but only the newer one negotiates FLAC delivery, so the presence
# of a decoder says nothing about what actually arrives. One grep of the mapped
# library, once per connection. cfg=both is the pair that starts NEITHER engine.
sy() {
  eng=;
  for sb in spotifymusicpro newspotifyhifi; do
    pidof $sb >/dev/null 2>&1 && { eng=$sb; break; };
  done;
  echo "spotify.eng=$eng";
  sl=;
  case "$eng" in spotifymusicpro) sl=pro;; newspotifyhifi) sl=hifi;; esac;
  sk=;
  [ -n "$sl" ] && sk=$(grep -ao 'esdk:[0-9.]*-g[0-9a-f]*' /usr/lib/libspotify$sl.so 2>/dev/null | head -1);
  echo "spotify.sdk=${sk#esdk:}";
  se=$(getenv SpotifyEnabled 2>/dev/null); sr=$(getenv SpotifyProEnabled 2>/dev/null);
  case "${se##*: }/${sr##*: }" in
    0/1) sc=pro;;
    1/0) sc=hifi;;
    1/1) sc=both;;
    *) sc=none;;
  esac;
  echo "spotify.cfg=$sc";
};
# lp() scans /proc/net/tcp{,6} for LISTEN (state 0A) sockets on the ports the
# box exposes without auth — telnet :23 (0017), adb :5555 (15B3), the web config
# :80 (0050) and the :2018 control tunnel (07E2) — so the diagnostics can say
# what the LAN can reach. Read once at connect, like the rest of @@c.
lp() {
  xtl=off; xad=off; xwb=off; xct=off;
  for f in /proc/net/tcp /proc/net/tcp6; do
    while read -r sl2 la ra stt rest; do
      [ "$stt" = 0A ] || continue;
      case "${la##*:}" in 0017) xtl=on;; 15B3) xad=on;; 0050) xwb=on;; 07E2) xct=on;; esac;
    done 2>/dev/null < $f;
  done;
  printf 'telnet=%s\nadb=%s\nweb=%s\ncontrol=%s\n' $xtl $xad $xwb $xct;
};
# ct() emits the whole capability block: at connect, and again after every MID-92
# service toggle, so the pane paints device truth rather than an optimistic flip.
# The table is id:envkey:daemon — a service with a daemon reports what is RUNNING,
# one without (usb) can only report its flag. Cast is out of the table because its
# daemon has two possible names and the loop's word splitting cannot carry them.
ct() {
  echo @@c;
  sy;
  for e in airplay:airplay:airplaydemo dlna:DMPEnable:dmr bt:btenabled:bluetoothd tidal:TidalEnabled:tidalConnect qobuz:QobuzConnectEnabled:qobuzConnect usb:USBEnable: cast:GoogleCast:cast_sample_app; do
    ci=${e%%:*}; cr=${e#*:}; ck=${cr%%:*}; cb=${cr#*:};
    if [ -n "$cb" ]; then pr "$ci" "$cb"; gv "$ci.env" "$ck"; else gv "$ci" "$ck"; fi;
  done;
  lp;
  echo @@E;
};
ct;

# ── tg(): service toggle (MID 92, "<id> <state>") ──
# Writes the env flag the service is ACTUALLY gated on, then kicks its init
# script detached (setsid) so the daemon outlives this ssh session — the scripts
# background their daemon with a bare '&', so a foreground kick would take it
# down with the connection. `start)` is empty in nearly all of them; netready and
# netdown are the real entry points.
#
# Spotify writes BOTH flags as a coherent pair. The vendor's two init scripts are
# each guarded on the OTHER flag being clear, so writing one alone is exactly how
# a box ends up with neither engine running — which is what the AR241CE_8530 OTA
# did. Its state is off|hifi|pro, not 0/1: the engines are not interchangeable.
sn() { setenv "$1" "$2" >/dev/null; };
tg() {
  vid=${1%% *}; vst=${1##* }; vk=; vs=;
  case "$vid" in
    spotify)
      killall -9 newspotifyhifi spotifymusicpro >/dev/null 2>&1;
      va=0; vb=0;
      case "$vst" in
        hifi) va=1; vs=S99newspotifyhifi;;
        pro) vb=1; vs=S99spotifymusicpro;;
      esac;
      sn SpotifyEnabled $va; sn SpotifyProEnabled $vb;;
    tidal) vk=TidalEnabled; vs=S99tidalConnect;;
    qobuz) vk=QobuzConnectEnabled; vs=S99qobuzConnect;;
    usb) vk=USBEnable;;
    airplay) vs=S99airplay_v2;;
    dlna) vs=S99dmr;;
    *) return;;
  esac;
  [ -n "$vk" ] && sn "$vk" "$vst";
  if [ -n "$vs" ]; then
    vc=netready;
    case "$vst" in 0|off) vc=netdown;; esac;
    setsid /etc/init.d/$vs $vc </dev/null >/dev/null 2>&1 &
  fi;
  ct;
  cq=8;
};

# ── lg(): the @@l log block (MID 93) ──
# The tail of the device's own syslog — the only place the box records a service
# refusing to start (an init script's "not enabled" line lands here and nowhere
# else), so it is what turns "the toggle did nothing" into an answer.
#
# The path is NOT /var/log/messages: the box touches that file at boot and then
# never writes to it (it stays 0 bytes), while rsyslog writes the real thing to
# /var/log/syslog/messages.log. The severity grep does double duty — it keeps
# only real log lines, dropping the indented continuation lines of the multi-line
# PlayView JSON that luci_service logs, which are most of what survives otherwise.
# luci_service itself is dropped at the source: it is the overwhelming majority of
# the file and none of it concerns services. The severity FILTER is deliberately
# not here — it is a view over the same tail, applied laptop-side with no second
# round trip. sed prefixes every line with a space: a log line that itself began
# with "@@" would otherwise be read as a section header and break record framing.
lg() { grep " [EWIDNF]/" /var/log/syslog/messages.log 2>/dev/null | grep -v luci_serv | tail -160 | sed 's/^/ /'; };

# ── @@d device details (reg 92 JSON: serial / MACs / MCU + full fw version) and
# @@g multiroom group (reg 39 JSON: linked devices) — both once per connection,
# shipped raw and parsed laptop-side ──
echo @@d; LUCI_local -r 92 2>/dev/null; echo @@E;
echo @@g; LUCI_local -r 39 2>/dev/null; echo @@E;

# ── @@n night mode: the SoC's multi-band DRC enable (ALSA boolean on the AED
# block — the one host-writable audio effect on this box; the EQ/DRC coefficient
# tables are read-only and the WM8904 controls drive a chip that isn't there).
# Read once at connect (the value quit restores) and again after every MID-91
# set, so the laptop only ever paints what the device reports. nm() is shared. ──
nm() { echo @@n; amixer -c0 cget name='AED Multi-band DRC enable' 2>/dev/null; echo @@E; };
nm;

# ── main streaming loop ── (state: i=metadata countdown, idl=idle ticks, bw=burst
# window, dg=diag overlay flag, pc49=position-poll gate, pgc=ping gate, ef=EOF streak)
i=0; prev=; ef=0; idl=0; bw=0; dg=0; pc49=0; pgc=0; cq=0;
while :; do

  # @@B now-playing metadata (MB42), re-read every ~5 ticks, only when it changes
  if [ $i -le 0 ]; then
    b=$(LUCI_local -r 42 2>/dev/null);
    if [ -n "$b" ] && [ "$b" != "$prev" ]; then prev=$b; echo @@B; printf '%s\n' "$b"; fi;
    i=5;
  fi;

  # @@p position (reg 49) — polled every 3rd tick (pc49) unless idle; rd flags a read
  echo @@p; pn=; rd=0; pc49=$((pc49-1));
  if [ $idl -lt 5 ] && [ $pc49 -le 0 ]; then
    pv=$(LUCI_local -r 49 2>/dev/null); echo "$pv"; pn=${pv#*Data:}; pn=${pn%% *}; rd=1; pc49=3;
  fi;

  # @@t play-state (reg 51) and @@v volume (reg 64) — both every tick (watchdog)
  echo @@t; tv=$(LUCI_local -r 51 2>/dev/null); echo "$tv";
  echo @@v; LUCI_local -r 64 2>/dev/null;

  # @@s resource stats — gathered ONLY while the diagnostics overlay is open (dg=1)
  if [ "$dg" = 1 ]; then
    read -r la lb lc r1 r2 < /proc/loadavg;
    mt=0; ma=0;
    while read -r k v u; do
      case "$k" in MemTotal:) mt=$v;; MemAvailable:) ma=$v; break;; esac;
    done < /proc/meminfo;
    read -r up r3 < /proc/uptime;
    tp=; read -r tp 2>/dev/null < /sys/class/thermal/thermal_zone0/temp;
    rxb=; read -r rxb 2>/dev/null < /sys/class/net/$dv/statistics/rx_bytes;
    txb=; read -r txb 2>/dev/null < /sys/class/net/$dv/statistics/tx_bytes;
    # cumulative error/drop counters (the laptop shows session deltas, so the
    # powerline link's boot-lifetime noise never reads as a live fault)
    rxe=; read -r rxe 2>/dev/null < /sys/class/net/$dv/statistics/rx_errors;
    txe=; read -r txe 2>/dev/null < /sys/class/net/$dv/statistics/tx_errors;
    rxd=; read -r rxd 2>/dev/null < /sys/class/net/$dv/statistics/rx_dropped;
    txd=; read -r txd 2>/dev/null < /sys/class/net/$dv/statistics/tx_dropped;
    # Wi-Fi signal / link-quality / noise from /proc/net/wireless (the active iface)
    sg=-; lq=-; ns=-;
    if [ "$net" = wifi ]; then
      while read -r wf qa ql lv nz rest; do
        case "$wf" in "$dv:") lq=${ql%.}; sg=${lv%.}; ns=${nz%.}; break;; esac;
      done 2>/dev/null < /proc/net/wireless;
    fi;
    # ALSA chain: walk each playback sub for state/avail (status) + rate/fmt/ch/buf
    # (hw_params); colon may be attached (rate:) or detached (rate :) — handle both.
    as=-; ab=-; ar=-; af=-; ac=-; bs=-;
    for ad in /proc/asound/card*/pcm*p/sub*; do
      while read -r ak av ar2; do
        k=${ak%:}; [ "$av" = ":" ] && av=$ar2;
        case "$k" in state) as=$av;; avail) ab=$av;; esac;
      done 2>/dev/null < "$ad/status";
      while read -r ak av ar2; do
        k=${ak%:}; [ "$av" = ":" ] && av=$ar2;
        case "$k" in rate) ar=$av;; format) af=$av;; channels) ac=$av;; buffer_size) bs=$av;; esac;
      done 2>/dev/null < "$ad/hw_params";
    done;
    cf=-; read -r cf 2>/dev/null < /sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq;
    # three ping RTTs (laptop / gateway / internet) gated to every 3rd @@s (pgc) so a
    # dead target can't stall every tick; skipped ticks emit "-" (parser folds as gap).
    # The internet target self-pins to its resolved IP after the first success (pg's
    # $oip), so every later tick skips DNS entirely. The pin is gated on the RTT
    # parse succeeding, not just resolution: BusyBox ping prints the "PING host
    # (ip):" header even at 100% loss, and pinning a captive-portal / ICMP-dead
    # answer would hold a dead IP for the whole connection while DNS recovers.
    # The ALSA softvol "Master" (the real output level on this box: the app keeps it
    # at vol−1) rides the same every-3rd gate, so the laptop can flag the level
    # drifting out of step with the volume the device reports — the one failure
    # mode where the room goes quiet while every display says it didn't.
    pgc=$((pgc-1));
    if [ $pgc -le 0 ]; then pg "$cip"; pcl=$o; pg "$gw"; pgw=$o; pg "$ph"; pnt=$o; [ "$o" != - ] && [ -n "$oip" ] && ph=$oip; pgc=3; sv=-; mv=$(amixer -c0 cget name=Master 2>/dev/null); case "$mv" in *": values="*) sv=${mv##*: values=}; sv=${sv%%,*}; sv=${sv%%"$nl"*};; esac; else pcl=-; pgw=-; pnt=-; sv=-; fi;
    echo @@s;
    echo "$up $la $lb $lc $ma $mt $nc $fw.$fv $kt-$kr ${tp:--} ${rxb:--} ${txb:--} $sg $lq $pcl $pgw $pnt ${as:--} ${ab:--} ${ar:--} ${af:--} ${ac:--} ${bs:--} ${cf:--} ${r1:--} ${ns:--} ${rxe:--} ${txe:--} ${rxd:--} ${txd:--} ${sv:--}";
  fi;
  # A service toggle re-reads capabilities twice: once immediately (the env write
  # is visible at once) and once a few ticks later, because a daemon needs a
  # moment to appear in pidof and the pane must never settle on a stale "off".
  [ $cq -gt 0 ] && { cq=$((cq-1)); [ $cq = 0 ] && ct; };
  echo @@E;

  # tick bookkeeping: countdown, and force an immediate metadata re-read (i=0) on a
  # detected backward position jump (track skip), a play-state change, or a burst.
  i=$((i-1));
  if [ $rd -eq 1 ]; then
    case "$pn" in
      ''|*[!0-9]*) lpn=;;
      *) [ -n "$lpn" ] && [ "$pn" -lt "$lpn" ] && { i=0; pc49=0; }; lpn=$pn;;
    esac;
  fi;
  [ "$tv" != "$ltv" ] && { i=0; pc49=0; }; ltv=$tv;
  [ $bw -gt 0 ] && { bw=$((bw-1)); i=0; };
  # reg 51 Data:0 = playing -> reset the idle counter; anything else accrues
  # idle ticks, stretching the read timeout from 1s to 3s after 5 of them
  case "$tv" in *"Data:0 "*) idl=0;; *) idl=$((idl+1));; esac;
  w=1; [ $idl -ge 5 ] && w=3;
  read -r u0 ux < /proc/uptime;

  # blocking read of one command (timeout $w). On a command: run it (whitelist only),
  # then two-step burst-drain any queued commands. On timeout: time the read to tell a
  # real idle gap from an EOF storm (3 sub-50ms empty reads in a row -> peer gone).
  if read -r -t $w mid data; then
    ef=0; pc=0;
    while :; do
      case "$mid" in
        __MIDS__) LUCI_local "$mid" "$data" >/dev/null 2>&1; pc=1;;
        90) case "$data" in 1) dg=1;; *) dg=0;; esac;;
        91) case "$data" in 1) nv=on;; *) nv=off;; esac; amixer -c0 cset name='AED Multi-band DRC enable' $nv >/dev/null 2>&1; nm;;
        92) tg "$data";;
        93) echo @@l; lg; echo @@E;;
      esac;
      read -r -t 0 || break;
      read -r -t 1 mid data || break;
    done;
    [ $pc = 1 ] && { i=0; bw=4; idl=0; pc49=0; };
  else
    read -r u1 ux < /proc/uptime;
    el=$(( (${u1%%.*} - ${u0%%.*}) * 100 + 1${u1#*.} - 1${u0#*.} ));
    if [ $el -lt 50 ]; then ef=$((ef+1)); [ $ef -ge 3 ] && exit 0; else ef=0; fi;
  fi;
done
