// Package configtext holds the text helpers of stored configuration
// versions (config_snapshot.content): decompression and the count of
// changed lines. It imports nothing of the backend, so a consumer such as
// notify can use it without the import of configsnap (whose ingest needs
// alarm and, through probe and discovery, notify itself).
package configtext

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
)

// MaxSize is the largest configuration a version takes, uncompressed
// (configsnap.MaxSize).
const MaxSize = 2 << 20

// ErrTooLarge: the configuration exceeds MaxSize (configsnap.ErrTooLarge).
var ErrTooLarge = errors.New("configuration larger than 2 MiB")

// Decompress returns the text of a stored version (the content column of
// config_snapshot), at most MaxSize bytes.
func Decompress(content []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("configsnap: decompress: %w", err)
	}
	defer func() { _ = zr.Close() }()
	out, err := io.ReadAll(io.LimitReader(zr, MaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("configsnap: decompress: %w", err)
	}
	if len(out) > MaxSize {
		return nil, ErrTooLarge
	}
	return out, nil
}

// ChangedLines counts the lines of a configuration version that its
// predecessor lacks plus the lines of the predecessor it lacks, each line
// as often as it occurs (WP-1147: the number of changed lines of
// ci.config_changed). It counts as a unified diff does for every change
// that does not reorder lines; a moved line counts as unchanged. A
// trailing line end opens no further line.
func ChangedLines(previous, current []byte) int {
	counts := map[string]int{}
	for _, l := range splitLines(previous) {
		counts[l]++
	}
	added := 0
	for _, l := range splitLines(current) {
		if counts[l] > 0 {
			counts[l]--
			continue
		}
		added++
	}
	removed := 0
	for _, n := range counts {
		removed += n
	}
	return added + removed
}

func splitLines(text []byte) []string {
	text = bytes.ReplaceAll(text, []byte("\r\n"), []byte("\n"))
	text = bytes.TrimSuffix(text, []byte("\n"))
	if len(text) == 0 {
		return nil
	}
	parts := bytes.Split(text, []byte("\n"))
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = string(p)
	}
	return out
}
