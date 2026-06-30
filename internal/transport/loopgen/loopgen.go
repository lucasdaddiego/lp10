// Package loopgen minifies the readable remote-loop shell source
// (remote_loop.src.sh) into the single line that gets embedded and shipped to the
// device (remote_loop.sh). It is shared by the `go:generate` tool (mkloop.go) and
// the test that checks the generated file is current, so both apply the exact same
// transform.
package loopgen

import "strings"

// Minify collapses a readable shell source into one line: it drops blank lines and
// whole-line comments, trims each remaining line, and joins the rest with a single
// space. The source must follow this contract (the generator test enforces the
// round-trip, so a violation fails CI rather than the device):
//
//   - Comments are WHOLE-LINE only — a line whose first non-blank character is '#'.
//     Inline trailing comments are NOT stripped: '#' also appears in ${x#...} and
//     inside quotes ("a # b"), so removing trailing comments safely would need a
//     real shell tokenizer. Keep comments on their own lines.
//   - Every code line ends with its own shell separator (';', ';;', or a keyword
//     that continues the next line such as 'do' / 'then' / 'in'). Lines are joined
//     with a single space; the minifier never inserts a separator.
//   - Break a command across lines only at a real, out-of-quote token-separating
//     space. The single space the join restores then reproduces the original byte
//     stream, so the minified output is byte-identical regardless of formatting.
func Minify(src string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(src, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(t)
	}
	return b.String()
}
