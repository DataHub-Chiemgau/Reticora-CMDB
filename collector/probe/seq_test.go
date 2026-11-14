package probe

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestSequenceMonotonicAcrossRestart covers PRB-09 and WP-952 AK5: the
// sequence rises strictly across a restart; a restart continues behind the
// reserved block of 1,000 numbers.
func TestSequenceMonotonicAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSequence(dir)
	if err != nil {
		t.Fatal(err)
	}
	var last uint64
	for i := 0; i < 1500; i++ {
		n, nerr := s.Next(StreamState)
		if nerr != nil {
			t.Fatal(nerr)
		}
		if n <= last {
			t.Fatalf("sequence not rising: %d after %d", n, last)
		}
		last = n
	}
	if last != 1500 {
		t.Fatalf("1,500 numbers end at %d, want 1500 (first number is 1)", last)
	}
	// Restart (or crash: nothing is saved on shutdown).
	for restart := 0; restart < 3; restart++ {
		s, err = OpenSequence(dir)
		if err != nil {
			t.Fatal(err)
		}
		n, err := s.Next(StreamState)
		if err != nil {
			t.Fatal(err)
		}
		if n <= last {
			t.Fatalf("restart %d: %d not above %d", restart, n, last)
		}
		if n%SeqBlock != 1 {
			t.Fatalf("restart %d: %d does not continue behind a block", restart, n)
		}
		last = n
	}
	// Concurrent use stays unique.
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[uint64]bool{}
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				n, err := s.Next(StreamState)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				if seen[n] {
					t.Errorf("number %d handed out twice", n)
				}
				seen[n] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
}

// TestSequenceSurvivesCrashDuringWrite covers AK5: an interrupted write
// leaves the old file; no number of the failed block is handed out, and a
// restart continues behind the last durable block.
func TestSequenceSurvivesCrashDuringWrite(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSequence(dir)
	if err != nil {
		t.Fatal(err)
	}
	var last uint64
	for i := 0; i < SeqBlock; i++ { // uses up the first block exactly
		if last, err = s.Next(StreamResults); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(filepath.Join(dir, SeqFile))
	if err != nil {
		t.Fatal(err)
	}
	// The crash: the temporary file is written, the rename never happens.
	renameFile = func(string, string) error { return errors.New("power cut") }
	_, err = s.Next(StreamResults)
	renameFile = os.Rename
	if err == nil {
		t.Fatal("Next handed out a number of a block that was not persisted")
	}
	after, _ := os.ReadFile(filepath.Join(dir, SeqFile))
	if string(after) != string(before) {
		t.Fatalf("sequence file changed by the interrupted write: %s → %s", before, after)
	}
	// A leftover temporary file of a real crash is ignored.
	if err = os.WriteFile(filepath.Join(dir, "."+SeqFile+".tmp-crash"), []byte(`{"reserved":{"res`), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "."+SeqFile+".tmp-") && e.Name() != "."+SeqFile+".tmp-crash" {
			t.Fatalf("temporary file %s left behind", e.Name())
		}
	}
	s, err = OpenSequence(dir)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.Next(StreamResults)
	if err != nil {
		t.Fatal(err)
	}
	if n <= last {
		t.Fatalf("after crash %d not above %d", n, last)
	}
	// A corrupt sequence file is an error, never a restart at 1.
	if err := os.WriteFile(filepath.Join(dir, SeqFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSequence(dir); err == nil {
		t.Fatal("corrupt sequence file accepted")
	}
}

// TestSequencePerStream covers AK4: state and results have their own
// counters.
func TestSequencePerStream(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSequence(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 5; i++ {
		n, _ := s.Next(StreamResults)
		if n != i {
			t.Fatalf("results #%d = %d", i, n)
		}
	}
	if n, _ := s.Next(StreamState); n != 1 {
		t.Fatalf("first state number = %d, want 1 (own counter)", n)
	}
	if n, _ := s.Next(StreamResults); n != 6 {
		t.Fatalf("results after state = %d, want 6", n)
	}
	s, _ = OpenSequence(dir)
	st, _ := s.Next(StreamState)
	rs, _ := s.Next(StreamResults)
	if st != SeqBlock+1 || rs != SeqBlock+1 {
		t.Fatalf("after restart state=%d results=%d, want %d each", st, rs, SeqBlock+1)
	}
}
