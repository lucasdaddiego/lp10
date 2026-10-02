// The idle screen: when the box is connected and nothing is playing, the
// player shows a large clock and names the sources that can wake the box, so
// an idle terminal reads from across the room and says how to wake it.

package tui

import (
	"time"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// blockDigits is a 5-row block font for the clock: the ten digits and the
// colon, drawn in full blocks so it reads at a distance.
var blockDigits = map[rune][5]string{
	'0': {"█████", "█   █", "█   █", "█   █", "█████"},
	'1': {"   ██", "    █", "    █", "    █", "    █"},
	'2': {"█████", "    █", "█████", "█    ", "█████"},
	'3': {"█████", "    █", "█████", "    █", "█████"},
	'4': {"█   █", "█   █", "█████", "    █", "    █"},
	'5': {"█████", "█    ", "█████", "    █", "█████"},
	'6': {"█████", "█    ", "█████", "█   █", "█████"},
	'7': {"█████", "    █", "    █", "    █", "    █"},
	'8': {"█████", "█   █", "█████", "█   █", "█████"},
	'9': {"█████", "█   █", "█████", "    █", "█████"},
	':': {" ", "█", " ", "█", " "},
}

// bigClock renders "HH:MM" in the block font: five rows, digits two columns
// apart, the colon one column wide.
func bigClock(now time.Time) []string {
	rows := make([]string, 5)
	for i, r := range now.Format("15:04") {
		glyph, ok := blockDigits[r]
		if !ok {
			continue
		}
		for l := range rows {
			if i > 0 {
				rows[l] += "  "
			}
			rows[l] += glyph[l]
		}
	}
	return rows
}

// wakeHint is the line under "nothing playing": what would wake the box. The
// tunnel cannot say which services are switched on, so it names the three the
// box always runs.
const wakeHint = "start something on Spotify · AirPlay · Bluetooth"

// renderIdle is the full player's body when the box is connected and nothing
// plays: the clock, then what would wake it. Returned already sized to h rows
// (the footer stays pinned below by the caller).
func (m *model) renderIdle(s protocol.Snapshot, now time.Time, W, h int) []string {
	ps := m.sty.pens()
	var mid []string
	for _, row := range bigClock(now) {
		mid = append(mid, ccell(ps.bri.render(row), W))
	}
	mid = append(mid, "",
		ccell(ps.dim.render("nothing playing"), W),
		ccell(ps.dmr.render(Clip(wakeHint, W)), W))
	if lbl, _ := m.sleepLabel(now); lbl != "" {
		mid = append(mid, ccell(ps.dmr.render(lbl), W))
	}
	return frameBody(mid, nil, h, true)
}
