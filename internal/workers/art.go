package workers

import (
	"context"
	"errors"
	"time"

	"github.com/lucasdaddiego/lp10go/internal/artwork"
	"github.com/lucasdaddiego/lp10go/internal/config"
	"github.com/lucasdaddiego/lp10go/internal/protocol"
)

const (
	artPoll         = 700 * time.Millisecond // how often to check the playing track's cover
	artFetchTimeout = 6 * time.Second        // bound on one cover download
	artRetryDelay   = 5 * time.Second        // back-off before retrying a cover that failed
	artCacheKeep    = 256                    // most-recent covers to keep on disk (older pruned)
	artPruneEvery   = 64                     // re-prune the disk cache every N covers loaded (not only at startup)
)

// ArtWorker keeps the decoded album cover aligned with the now-playing track.
// It watches the current CoverArtUrl and, when it changes, loads the image from
// the on-disk cache or fetches it once, handing the decoded result to State for
// the UI to rasterize. A transient network failure is retried after a delay so
// an outage recovers without hammering, while a deterministic decode failure (an
// unsupported format) is given up on for that url — no point re-downloading it
// every few seconds. No-op when art is disabled or art_mode is "off"; the UI
// then keeps its procedural motif. Mirrors the other workers' Stop handling.
func ArtWorker(st *protocol.State, cfg config.Config) {
	if !cfg.Art || cfg.ArtMode == "off" {
		return
	}
	dir := config.ArtCacheDir()
	artwork.PruneCache(dir, artCacheKeep) // bound the on-disk cache at startup
	var loadedURL, failedURL string
	var retryAt time.Time
	loads := 0
	for !st.Stop.IsSet() {
		url := st.Snap().CoverURL
		switch {
		case url == "" || url == loadedURL:
			// nothing playing with art, or already loaded
		case url == failedURL && time.Now().Before(retryAt):
			// backing off this url after a recent transient failure
		default:
			ctx, cancel := context.WithTimeout(context.Background(), artFetchTimeout)
			img, err := artwork.Get(ctx, url, dir)
			cancel()
			switch {
			case err == nil && img != nil:
				// Derive the cover's dominant hue here, off the render goroutine, and
				// hand it to State with the image so View never scans pixels for it.
				dom, domOK := artwork.Dominant(img)
				st.SetArt(url, img, dom, domOK)
				loadedURL, failedURL = url, ""
				if loads++; loads%artPruneEvery == 0 {
					artwork.PruneCache(dir, artCacheKeep) // re-bound mid-session, not only at startup
				}
			case errors.Is(err, artwork.ErrUndecodable):
				loadedURL = url // permanent: an undecodable cover is never retried
			default:
				failedURL, retryAt = url, time.Now().Add(artRetryDelay) // transient
			}
		}
		st.Stop.Wait(artPoll)
	}
}
