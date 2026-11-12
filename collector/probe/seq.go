package probe

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Streams of the sequence counters (WP-952 AK4); the names match
// probe_ingest_cursor.stream on the server (WP-944).
const (
	StreamState   = "state"
	StreamResults = "results"
)

// SeqFile is the file of the sequence counters in the spool directory.
const SeqFile = "probe-seq"

// SeqBlock is the number of sequence numbers reserved on disk at once
// (WP-952 AK5).
const SeqBlock = 1000

// Sequence hands out the sequence numbers of a collector per stream
// (PRB-09, E-175). Before a number of a new block is handed out, the end of
// the block is written atomically to <dir>/probe-seq (temporary file,
// fsync, rename, directory fsync). After a restart or crash the counter
// continues behind the last reserved block, so numbers rise strictly
// across restarts; gaps are allowed. The first number of a stream is 1.
//
// Sequence is safe for concurrent use.
type Sequence struct {
	dir   string
	block uint64

	mu       sync.Mutex
	next     map[string]uint64
	reserved map[string]uint64 // exclusive upper bound persisted on disk
}

// seqDisk is the file format: the exclusive upper bound of the reserved
// block per stream.
type seqDisk struct {
	Reserved map[string]uint64 `json:"reserved"`
}

// OpenSequence reads the counters from dir; a missing file starts every
// stream at 1. A corrupt file is an error: continuing at 1 would break the
// monotony the server relies on.
func OpenSequence(dir string) (*Sequence, error) {
	s := &Sequence{dir: dir, block: SeqBlock, next: map[string]uint64{}, reserved: map[string]uint64{}}
	data, err := os.ReadFile(filepath.Join(dir, SeqFile))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("probe: read sequence: %w", err)
	}
	var d seqDisk
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("probe: decode sequence: %w", err)
	}
	for stream, r := range d.Reserved {
		// Continue behind the reserved block: numbers below it may have
		// been handed out before the restart.
		s.next[stream] = r
		s.reserved[stream] = r
	}
	return s, nil
}

// Next returns the next sequence number of stream. It writes a new block
// reservation first when the current block is used up; if that write fails,
// no number is handed out.
func (s *Sequence) Next(stream string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.next[stream]
	if n == 0 {
		n = 1
	}
	if n >= s.reserved[stream] {
		reserved := make(map[string]uint64, len(s.reserved)+1)
		for k, v := range s.reserved {
			reserved[k] = v
		}
		reserved[stream] = n + s.block
		data, err := json.Marshal(seqDisk{Reserved: reserved})
		if err != nil {
			return 0, err
		}
		if err := writeFileAtomic(s.dir, SeqFile, data); err != nil {
			return 0, fmt.Errorf("probe: reserve sequence block: %w", err)
		}
		s.reserved = reserved
	}
	s.next[stream] = n + 1
	return n, nil
}

// renameFile is os.Rename; tests replace it to simulate a crash between
// writing the temporary file and the rename.
var renameFile = os.Rename

// writeFileAtomic writes data to a temporary file in dir, syncs it and
// renames it to dir/name, then syncs the directory (like
// edgecore/buffer). Readers see the old or the complete new file, never a
// partial one.
func writeFileAtomic(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = renameFile(tmpName, filepath.Join(dir, name))
	}
	if err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	d, err := os.Open(dir) //nolint:gosec // the spool directory of the collector
	if err != nil {
		return err
	}
	err = d.Sync()
	if closeErr := d.Close(); err == nil {
		err = closeErr
	}
	return err
}
