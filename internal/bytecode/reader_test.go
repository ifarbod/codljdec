package bytecode

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// dumpDir is the repository's corpus of runtime-dumped bytecode.
const dumpDir = "../../../Dump"

// corpus returns every bytecode file under Dump/, skipping the test when absent.
func corpus(t testing.TB) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dumpDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".lua") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil || len(files) == 0 {
		t.Skipf("no corpus at %s: %v", dumpDir, err)
	}
	return files
}

func TestParseCorpus(t *testing.T) {
	files := corpus(t)
	var (
		mu     sync.Mutex
		failed int
		empty  int
		wg     sync.WaitGroup
		sem    = make(chan struct{}, 16)
	)
	for _, path := range files {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			data, err := os.ReadFile(path)
			if err == nil {
				_, err = Parse(data)
			}
			if err != nil {
				mu.Lock()
				defer mu.Unlock()
				if errors.Is(err, ErrEmpty) {
					empty++
					return
				}
				if failed++; failed <= 20 {
					t.Errorf("%s: %v", path, err)
				}
			}
		})
	}
	wg.Wait()
	t.Logf("parsed %d files: %d empty, %d failures", len(files), empty, failed)
}
