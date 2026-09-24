package workers

import (
	"context"
	"errors"
	"image"
	"image/draw"
	"time"

	"github.com/lucasdaddiego/lp10/internal/artwork"
	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

const (
	artPoll         = 700 * time.Millisecond // how often to check the playing track's cover
	artFetchTimeout = 6 * time.Second        // bound on one cover download
	artRetryDelay   = 5 * time.Second        // back-off before retrying a cover that failed
	artCacheKeep    = 256                    // most-recent covers to keep on disk (older pruned)
	artPruneEvery   = 64                     // re-prune the disk cache every N covers loaded (not only at startup)
	// artCacheBytes bounds the cover cache's total size next to the count. One
	// cover may be as large as the fetch cap (8 MB), so artCacheKeep of them
	// alone could reach 2 GB; real covers average ~133 KB (256 in 34 MB), so
	// with those the count still binds first.
	artCacheBytes = 64 << 20
	// artMaxEdge bounds the longest edge of the cover handed to State. It is the
	// artwork package's Kitty payload cap (kittyMaxPx): no placement is ever
	// drawn larger, so pixels past it only cost the render goroutine.
	artMaxEdge = 2048
)

// artWorker keeps the decoded album cover aligned with the now-playing track.
// It watches the current CoverArtUrl and, when it changes, loads the image from
// the on-disk cache or fetches it once, handing the decoded result to State for
// the UI to rasterize. A transient network failure is retried after a delay so
// an outage recovers without hammering, while a deterministic decode failure (an
// unsupported format) is given up on for that url — no point re-downloading it
// every few seconds. No-op when art is disabled or art_mode is "off"; the UI
// then keeps its procedural motif. Mirrors the other workers' stop handling.
func artWorker(ctx context.Context, control *runControl, st *protocol.State, cfg config.Config) {
	if !cfg.Art || cfg.ArtMode == "off" {
		return
	}
	dir := config.ArtCacheDir()
	artwork.PruneCache(dir, artCacheKeep, artCacheBytes) // bound the on-disk cache at startup
	var loadedURL, failedURL string
	var retryAt time.Time
	loads := 0
	for !control.stop.IsSet() && ctx.Err() == nil {
		url := st.Snap().CoverURL
		switch {
		case url == "" || url == loadedURL:
			// nothing playing with art, or already loaded
		case url == failedURL && time.Now().Before(retryAt):
			// backing off this url after a recent transient failure
		default:
			// The load runs under the fence: its bytes come from whatever url the
			// device reports, so a decoder panic on a hostile image costs a noted
			// error, not the program. loadedURL moves to url first, so a panic
			// mid-load retires this cover like an undecodable one instead of
			// re-downloading it every poll; only a transient failure moves it back.
			prev := loadedURL
			loadedURL = url
			fence(st, control, "art worker", func() {
				fetchCtx, cancel := context.WithTimeout(ctx, artFetchTimeout)
				img, err := artwork.Get(fetchCtx, url, dir, cfg.Host)
				cancel()
				switch {
				case err == nil && img != nil:
					// Bound the cover and derive its dominant hue here, off the
					// render goroutine, so View never rebuilds a raster from a
					// full-size source nor scans pixels for the hue.
					img = boundArt(img)
					dom, domOK := artwork.Dominant(img)
					st.SetArt(url, img, dom, domOK)
					failedURL = ""
					if loads++; loads%artPruneEvery == 0 {
						artwork.PruneCache(dir, artCacheKeep, artCacheBytes) // re-bound mid-session, not only at startup
					}
				case errors.Is(err, artwork.ErrUndecodable):
					// permanent: an undecodable cover is never retried
				default:
					loadedURL = prev
					failedURL, retryAt = url, time.Now().Add(artRetryDelay) // transient
				}
			})
		}
		control.stop.Wait(artPoll)
	}
}

// boundArt shrinks a cover whose longest edge exceeds artMaxEdge by the
// smallest whole factor k that brings it under, averaging each k×k block, and
// returns any other cover as is. The decode guard admits covers up to 16 MP,
// and the render goroutine rebuilds its raster from this image on every
// art-footprint change: a 4096² cover cost ~209 ms and 67 MB per ghost render
// there, against a 66 ms frame. Bounded once here, the largest source any
// render sees is artMaxEdge on its long side.
func boundArt(img image.Image) image.Image {
	b := img.Bounds()
	k := (max(b.Dx(), b.Dy()) + artMaxEdge - 1) / artMaxEdge
	if k <= 1 {
		return img
	}
	src, ok := img.(*image.RGBA)
	if !ok {
		src = image.NewRGBA(b)
		draw.Draw(src, b, img, b.Min, draw.Src)
	}
	w, h := (b.Dx()+k-1)/k, (b.Dy()+k-1)/k
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		y0, y1 := b.Min.Y+y*k, min(b.Min.Y+(y+1)*k, b.Max.Y)
		for x := range w {
			x0, x1 := b.Min.X+x*k, min(b.Min.X+(x+1)*k, b.Max.X)
			var sum [4]int
			for sy := y0; sy < y1; sy++ {
				row := src.Pix[src.PixOffset(x0, sy):src.PixOffset(x1, sy)]
				for i := 0; i < len(row); i += 4 {
					sum[0] += int(row[i])
					sum[1] += int(row[i+1])
					sum[2] += int(row[i+2])
					sum[3] += int(row[i+3])
				}
			}
			n := (y1 - y0) * (x1 - x0)
			o := dst.PixOffset(x, y)
			for c := range sum {
				dst.Pix[o+c] = uint8(sum[c] / n)
			}
		}
	}
	return dst
}
