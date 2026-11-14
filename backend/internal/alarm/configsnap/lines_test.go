package configsnap

import "testing"

func TestChangedLines(t *testing.T) {
	for _, c := range []struct {
		name          string
		previous, cur string
		want          int
	}{
		{"same", "a\nb\n", "a\nb\n", 0},
		{"one changed", "hostname a\nntp 1\n", "hostname a\nntp 2\n", 2},
		{"added", "a\n", "a\nb\nc\n", 2},
		{"removed", "a\nb\nc", "a", 2},
		{"crlf", "a\r\nb\r\n", "a\nb\n", 0},
		{"duplicate", "!\n!\n", "!\n", 1},
		{"from empty", "", "a\nb", 2},
	} {
		if got := ChangedLines([]byte(c.previous), []byte(c.cur)); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}
