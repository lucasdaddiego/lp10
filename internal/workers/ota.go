// The firmware-check worker: asks the vendor's OTA manifest whether the
// device's build is current. This is the one thing lp10 does that leaves the
// LAN on the device's behalf, so it is strictly on demand — the diagnostics
// overlay raises a request when it opens, and nothing else does — and it
// answers a repeat request from its last verdict for a while rather than
// asking the vendor again.
//
// The endpoint is the same public, unauthenticated POST the box itself uses
// (found in the teardown; firmware 8530 moved it to lp10.arylic.rakoit-ota.com
// via the device's fwdownload_xml env). The body names the brand, model and
// the current build; a synthetic deviceId is accepted. Reply: errorCode 1001
// "No update available", or 1000 with the offered version and package URL.

package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

const (
	// otaManifestURL is the vendor manifest the LP10 asks since firmware 8530
	// (the pre-8530 host, lp10-ota.rakoit.com, still answers identically).
	otaManifestURL = "https://lp10.arylic.rakoit-ota.com/v1"
	otaTimeout     = 6 * time.Second
	otaPoll        = 500 * time.Millisecond
	// otaFresh is how long a verdict answers repeat requests without another
	// round trip to the vendor: reopening the overlay a few times in a session
	// should not mean a few POSTs.
	otaFresh   = 30 * time.Minute
	otaMaxBody = 16 << 10
)

// reBuild is the shape of a firmware build the manifest is asked about
// ("AR241CE_8530"): the device string is LAN input and lands in a request body.
var reBuild = regexp.MustCompile(`^[A-Z0-9]{2,12}_[0-9]{1,8}$`)

// otaURL is the manifest endpoint: the default, or LP10_OTA_URL (tests point it
// at a local server; set-but-empty disables the worker, as the hermetic e2e
// runs do).
func otaURL() (string, bool) {
	if u, set := os.LookupEnv("LP10_OTA_URL"); set {
		return u, u != ""
	}
	return otaManifestURL, true
}

// otaCheck performs one manifest request for build and turns the reply into a
// verdict. Every failure is a verdict too (Err set), so the overlay never waits
// on a check that silently went nowhere.
func otaCheck(ctx context.Context, url, build string) protocol.OTAInfo {
	info := protocol.OTAInfo{At: time.Now(), Asked: build}
	if build == "" {
		info.Err = "firmware not read yet"
		return info
	}
	if !reBuild.MatchString(build) {
		info.Err = "unrecognised firmware string"
		return info
	}
	body, _ := json.Marshal(map[string]any{"device": map[string]string{
		"brand": "arylic", "deviceId": "lp10", "fwVersion": build, "model": "LP10",
	}})
	rctx, cancel := context.WithTimeout(ctx, otaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		info.Err = "bad manifest url"
		return info
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		info.Err = "vendor unreachable"
		return info
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, otaMaxBody))
	if err != nil {
		info.Err = "vendor unreachable"
		return info
	}
	var reply struct {
		ErrorCode   int    `json:"errorCode"`
		ErrorString string `json:"errorString"`
		Version     string `json:"version"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &reply) != nil {
		info.Err = "unexpected vendor reply"
		return info
	}
	switch reply.ErrorCode {
	case 1001:
		info.UpToDate = true
	case 1000:
		info.Offered = reply.Version
		if info.Offered == "" {
			info.Offered = "a newer build"
		}
	default:
		info.Err = "vendor said: " + reply.ErrorString
		if reply.ErrorString == "" {
			info.Err = "unexpected vendor reply"
		}
	}
	return info
}

// otaWorker waits for requests and serves each from the last verdict when it
// is fresh and was for the same build, else from one manifest round trip.
func otaWorker(ctx context.Context, control *runControl, st *protocol.State) {
	url, ok := otaURL()
	if !ok {
		return
	}
	var last *protocol.OTAInfo
	poll := time.NewTicker(otaPoll)
	defer poll.Stop()
	for !control.stop.IsSet() && ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
		build, pending := st.TakeOTARequest()
		if !pending {
			continue
		}
		if last != nil && last.Err == "" && last.Asked == build && time.Since(last.At) < otaFresh {
			st.SetOTA(*last) // fresh enough: no second trip to the vendor
			continue
		}
		info := otaCheck(ctx, url, build)
		last = &info
		st.SetOTA(info)
	}
}
