// Package config handles the config file, paths, and persistent-state IO
// (premute level, snapshot cache, atomic writes).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/lucasdaddiego/lp10/internal/atomicfile"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// homeDir resolves the user's home directory, falling back to the passwd
// database (like Python's os.path.expanduser) when $HOME is unset, rather than
// silently producing a cwd-relative path.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	return ""
}

// Defaults. Field order is irrelevant; types drive the strict TOML coercion
// below.
const (
	// defHost is only a fallback: discovery (on by default) resolves the device's
	// real address at startup, so it works out of the box even as the DHCP lease
	// moves. With discover=false (or no mDNS responder) this literal must itself
	// resolve — a device not reachable as "lp10.local" then needs host set.
	defHost = "lp10.local"
	defUser = "root"
	// DefaultName is the generic UI label. On a successful mDNS discovery the app
	// refines it to "LP10 · <device's advertised name>" (see main.go), so no room
	// name is hardcoded; a user-set `name` overrides it and also serves as the
	// discovery disambiguation hint among multiple LP10s.
	DefaultName = "LP10"
	defVolStep  = 2
	defPingHost = "spotify.com" // diagnostics: the device's internet-latency target
	defArtMode  = "auto"        // art rendering: auto|kitty|halfblock|off
	defPremute  = 30            // pre-mute level restored on any read problem
)

// artModes is the set of accepted art_mode values; anything else in the config
// is ignored (keeps the default) rather than silently mis-coerced.
var artModes = map[string]bool{"auto": true, "kitty": true, "halfblock": true, "off": true}

// HostEnv pins the device host for a single run, overriding config and skipping
// mDNS discovery.
const HostEnv = "LP10_HOST"

// Config is the resolved runtime configuration. Warn carries a config-load
// problem to surface in the UI (empty string == no warning).
type Config struct {
	Host       string
	StateKey   string // the host as configured (file or LP10_HOST): keys the state files; discovery may rewrite Host
	User       string
	Name       string
	VolStep    int
	PingHost   string // diagnostics overlay: device's internet-ping target
	Discover   bool   // attempt mDNS auto-discovery at startup (config input)
	Discovered bool   // set at runtime when discovery resolved the host
	Art        bool   // render real album art (from the track's CoverArtUrl)
	ArtMode    string // auto|kitty|halfblock|off — how album art is drawn
	Warn       string
}

// Load reads ~/.config/lp10/config.toml (honoring XDG_CONFIG_HOME), applies
// strict per-field typing, clamps vol_step, and lets LP10_HOST override the
// host for a single run.
func Load() Config {
	cfg := Config{Host: defHost, User: defUser, Name: DefaultName, VolStep: defVolStep, PingHost: defPingHost, Discover: true, Art: true, ArtMode: defArtMode}

	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		if h := homeDir(); h != "" {
			base = filepath.Join(h, ".config")
		}
	}
	// Only read a config file when we have a real base dir: a "" home must not
	// resolve to a cwd-relative <cwd>/.config path that varies run to run.
	if base != "" {
		path := filepath.Join(base, "lp10", "config.toml")
		var data map[string]any
		_, err := toml.DecodeFile(path, &data)
		switch {
		case err == nil:
			if c := applyTOML(&cfg, data); len(c) > 0 {
				cfg.Warn = "config.toml: " + strings.Join(c, "; ")
			}
		case errors.Is(err, fs.ErrNotExist):
			// missing file is identical to no config — no warning
		default:
			// a broken file must not be silently identical to a missing one
			cfg.Warn = fmt.Sprintf("config.toml ignored: %v", err)
		}
	}

	// Clamp to a sane [1,100]: 0/negative would freeze the volume keys, and an
	// absurd step (e.g. a hostile config, or a float that saturated to MaxInt)
	// would overflow AdjustVol before clamp100 rescued it.
	cfg.VolStep = clampVol(cfg.VolStep)
	if h := os.Getenv(HostEnv); h != "" {
		cfg.Host = h
	}
	cfg.StateKey = cfg.Host
	return cfg
}

// configKeys are the recognised config.toml keys and the type each takes.
var configKeys = map[string]string{
	"host": "string", "user": "string", "name": "string", "ping_host": "string",
	"discover": "bool", "art": "bool", "art_mode": "string", "vol_step": "number",
}

// applyTOML copies recognized keys with strict typing: string fields accept
// only strings; vol_step accepts an integer or an integral float. Anything else
// (including a bool, or a string for a numeric field) is ignored, to avoid
// surprising coercions from typos — and reported, along with unknown keys, so
// a typo cannot silently keep the default (the returned complaints become the
// startup warning).
func applyTOML(cfg *Config, data map[string]any) (complaints []string) {
	keys := slices.Sorted(maps.Keys(data))
	for _, k := range keys {
		v := data[k]
		want, known := configKeys[k]
		if !known {
			complaints = append(complaints, fmt.Sprintf("unknown key %q", k))
			continue
		}
		ok := false
		switch k {
		case "host", "user", "name", "ping_host":
			var sv string
			if sv, ok = v.(string); ok {
				switch k {
				case "host":
					cfg.Host = sv
				case "user":
					cfg.User = sv
				case "name":
					cfg.Name = sv
				case "ping_host":
					cfg.PingHost = sv
				}
			}
		case "discover", "art":
			var bv bool
			if bv, ok = v.(bool); ok {
				if k == "discover" {
					cfg.Discover = bv
				} else {
					cfg.Art = bv
				}
			}
		case "art_mode":
			sv, isStr := v.(string)
			if isStr && !artModes[sv] {
				complaints = append(complaints, fmt.Sprintf("art_mode %q ignored (auto|kitty|halfblock|off)", sv))
				continue
			}
			if ok = isStr; ok {
				cfg.ArtMode = sv
			}
		case "vol_step":
			switch n := v.(type) {
			case int64:
				if int64(int(n)) == n {
					cfg.VolStep, ok = int(n), true
				}
			case float64:
				// allow an integral float like 2.0, but reject values outside int
				// range so the conversion can't overflow to a garbage/negative
				// step (float64(MaxInt) rounds UP to 2^63 on 64-bit systems, so
				// comparing against it inclusively still admitted exactly the
				// first invalid value).
				limit := float64(uint64(1) << (strconv.IntSize - 1))
				if n == math.Trunc(n) && n >= -limit && n < limit {
					cfg.VolStep, ok = int(n), true
				}
			}
		}
		if !ok {
			complaints = append(complaints, fmt.Sprintf("%s ignored (want %s)", k, want))
		}
	}
	return complaints
}

// StateDir is the persistent-state directory, or "" when it cannot be created —
// callers degrade to a session without persistence rather than crashing.
func StateDir() string {
	d := os.Getenv("LP10_STATE_DIR")
	if d == "" {
		h := homeDir()
		if h == "" {
			return "" // no home: degrade to no-persistence, not a cwd-relative dir
		}
		d = filepath.Join(h, ".local", "state", "lp10")
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return ""
	}
	return d
}

var slugRe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func slug(host string) string {
	return slugRe.ReplaceAllString(host, "_")
}

// PremutePath / SnapshotPath are per-device files under the state dir, or ""
// when there is no usable state dir. They key on StateKey — the host as
// configured — not on Host, which discovery rewrites to whatever address the
// box holds today: keyed on the address, a new DHCP lease lost the pre-mute
// level and the first-paint snapshot.
func PremutePath(cfg Config) string {
	if d := StateDir(); d != "" {
		return filepath.Join(d, "premute-"+slug(cfg.stateKey()))
	}
	return ""
}

func SnapshotPath(cfg Config) string {
	if d := StateDir(); d != "" {
		return filepath.Join(d, "snapshot-"+slug(cfg.stateKey())+".json")
	}
	return ""
}

// stateKey is StateKey, or Host for a Config built without Load (tests).
func (cfg Config) stateKey() string {
	if cfg.StateKey != "" {
		return cfg.StateKey
	}
	return cfg.Host
}

// SweepPath is `lp10 sweep`'s per-device baseline file under the state dir
// (the last sweep's findings, diffed against the next), or "" when there is no
// usable state dir.
func SweepPath(cfg Config) string {
	if d := StateDir(); d != "" {
		return filepath.Join(d, "sweep-"+slug(cfg.stateKey())+".json")
	}
	return ""
}

// ArtCacheDir is the album-art cache directory (state dir /art), created on
// demand, or "" when there's no usable state dir (art then works network-only).
// It is shared across hosts: covers are keyed by URL, which is already unique.
func ArtCacheDir() string {
	if d := StateDir(); d != "" {
		p := filepath.Join(d, "art")
		if err := os.MkdirAll(p, 0o700); err != nil {
			return ""
		}
		return p
	}
	return ""
}

func clampVol(v int) int {
	if v < 1 {
		return 1
	}
	if v > 100 {
		return 100
	}
	return v
}

// LoadPremute returns the persisted pre-mute level clamped to [1,100], or 30 on
// any problem (missing path, unreadable, or non-numeric content).
func LoadPremute(path string) int {
	if path == "" {
		return defPremute
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return defPremute
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return defPremute
	}
	return clampVol(n)
}

// SavePremute persists a clamped pre-mute level. Failures are swallowed.
func SavePremute(path string, v int) {
	if path == "" {
		return
	}
	_ = atomicfile.Write(path, []byte(strconv.Itoa(clampVol(v))))
}

// CachedSnapshot is the typed, versionless on-disk first-paint state. Its JSON
// tags preserve the existing cache contract so snapshots written by earlier
// releases remain readable.
type CachedSnapshot struct {
	Track   *protocol.Track `json:"track"`
	Pos     int             `json:"pos"`
	Playing int             `json:"playing"`
	Vol     int             `json:"vol"`
	EQ      map[string]int  `json:"eq"`
}

// LoadSnapshot reads the cached snapshot. A corrupt file, non-object root, or
// field with the wrong JSON type returns nil so it cannot become a crash loop.
func LoadSnapshot(path string) *CachedSnapshot {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(b, &root) != nil || root == nil {
		return nil
	}
	var snap CachedSnapshot
	if json.Unmarshal(b, &snap) != nil {
		return nil
	}
	return &snap
}

// SaveSnapshot persists the snapshot as JSON. Failures are swallowed.
func SaveSnapshot(path string, snap CachedSnapshot) {
	if path == "" {
		return
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return
	}
	_ = atomicfile.Write(path, b)
}
