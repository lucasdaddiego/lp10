package loopgen

import "testing"

func TestMinify(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"joins with single space", "a;\nb;\nc", "a; b; c"},
		{"drops blank lines", "a;\n\n\n b", "a; b"},
		{"drops whole-line comments", "# header\na;\n  # indented note\nb", "a; b"},
		{"trims indentation", "  while :; do\n    echo hi;\n  done", "while :; do echo hi; done"},
		{"keeps a leading comment from adding a space", "# c\n# c2\nfirst", "first"},
		{
			// the safety property: '#' as parameter-expansion (mid-line) is NOT a
			// comment and must survive verbatim.
			"param-expansion hash is not a comment",
			"fw=${fw#*Data:};\nfw=${fw%% *}", "fw=${fw#*Data:}; fw=${fw%% *}",
		},
		{
			// '#' inside a quoted string (not at line start) must survive verbatim.
			"quoted hash survives", `msg="a # b";`, `msg="a # b";`,
		},
		{"empty source", "", ""},
		{"only comments and blanks", "# a\n\n   # b\n", ""},
		{"crlf line endings tolerated", "a;\r\nb", "a; b"},
	}
	for _, c := range cases {
		if got := Minify(c.src); got != c.want {
			t.Errorf("%s: Minify(%q) = %q, want %q", c.name, c.src, got, c.want)
		}
	}
}

// TestMinifyIdempotent: minifying an already-minified line is a no-op (no leading
// comments/blanks to drop, nothing to trim).
func TestMinifyIdempotent(t *testing.T) {
	one := "ph='x'; while :; do echo @@p; done"
	if got := Minify(one); got != one {
		t.Errorf("Minify(minified) = %q, want unchanged %q", got, one)
	}
}
