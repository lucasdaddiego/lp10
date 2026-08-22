package protocol

import (
	"iter"
	"strings"
)

// tags is the set of section letters a record may carry. 'i' is the one-shot
// static device/network info block and 'c' the one-shot capability/config block
// (both key=value lines); 'd' and 'g' are the one-shot raw register reads for
// the device-details (reg 92) and multiroom-group (reg 39) JSON. All four are
// sent once per connection. 'n' is the night-mode (multi-band DRC enable)
// readback: once at connect and again after every MID-91 set.
var tags = map[byte]bool{'B': true, 'p': true, 't': true, 'v': true, 's': true, 'i': true, 'c': true, 'd': true, 'g': true, 'n': true}

const maxRecLines = 200 // a legitimate record is ~30 lines

// Record is one framed snapshot: section letter -> its lines.
type Record map[string][]string

// IterRecords turns a line source into a sequence of framed records. The 'B'
// key is present only when an @@B section appeared. Bad lines never panic. A
// single section that grows past maxRecLines (malformed flood) is shed on its
// own — the record's other, well-formed sections are kept; framing is kept.
// nextLine returns (line, true) per line and ("", false) at EOF.
func IterRecords(nextLine func() (string, bool)) iter.Seq[Record] {
	return func(yield func(Record) bool) {
		rec := Record{}
		key := byte(0)
		for {
			line, ok := nextLine()
			if !ok {
				return // EOF: drop any partial record (no @@E)
			}
			line = strings.TrimRight(line, "\n")
			if strings.HasPrefix(line, "@@") {
				var tag byte
				if len(line) >= 3 {
					tag = line[2]
				}
				switch {
				case tag == 'E':
					if !yield(rec) {
						return
					}
					rec, key = Record{}, 0
				case tags[tag]:
					key = tag
					if _, exists := rec[string(key)]; !exists {
						rec[string(key)] = []string{}
					}
				default:
					key = 0
				}
			} else if key != 0 && line != "" {
				// Measured against the lines actually accumulated, not a per-header
				// counter: a repeated @@B re-opens the section without clearing it,
				// so resetting a counter there would hand the flood a fresh budget
				// on every header and the section could grow without bound.
				if len(rec[string(key)]) >= maxRecLines {
					// this section is flooding: drop what it accumulated and stop
					// appending, but keep the record's other (well-formed) sections
					delete(rec, string(key))
					key = 0
				} else {
					rec[string(key)] = append(rec[string(key)], line)
				}
			}
		}
	}
}
