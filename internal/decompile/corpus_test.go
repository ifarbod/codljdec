package decompile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ifarbod/codljdec/internal/bytecode"
)

const dumpDir = "../../../Dump"

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

var errKind = regexp.MustCompile(`function f\d+: ([^;\n]*?)(?: at pc \d+| \(.*)?(?:;|$)`)

// TestDecompileCorpus decompiles every dumped file and summarizes failures.
func TestDecompileCorpus(t *testing.T) {
	files := corpus(t)
	var (
		mu       sync.Mutex
		failed   int
		kinds    = map[string]int{}
		examples = map[string]string{}
		wg       sync.WaitGroup
		sem      = make(chan struct{}, 16)
	)
	for _, path := range files {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Error(err)
				return
			}
			f, err := bytecode.Parse(data)
			if errors.Is(err, bytecode.ErrEmpty) {
				return
			}
			if err != nil {
				t.Errorf("%s: %v", path, err)
				return
			}
			fn, err := File(f)
			if perr := Print(io.Discard, fn); perr != nil {
				t.Errorf("%s: print: %v", path, perr)
			}
			if err == nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			failed++
			for _, m := range errKind.FindAllStringSubmatch(err.Error(), -1) {
				k := m[1]
				if len(k) > 70 {
					k = k[:70]
				}
				kinds[k]++
				if _, ok := examples[k]; !ok {
					examples[k] = path
				}
			}
		})
	}
	wg.Wait()
	keys := slices.SortedFunc(func(yield func(string) bool) {
		for k := range kinds {
			if !yield(k) {
				return
			}
		}
	}, func(a, b string) int { return kinds[b] - kinds[a] })
	for _, k := range keys {
		t.Logf("%6d  %s  (e.g. %s)", kinds[k], k, examples[k])
	}
	t.Logf("%d files, %d with failed functions", len(files), failed)
}
