package decompile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ifarbod/codljdec/internal/bytecode"
)

// TestRoundTrip recompiles decompiled output with a stock LuaJIT 2.1 (GC64)
// binary given by $LUAJIT. Every file must compile; IW8 files, whose format
// is stock LuaJIT, are additionally compared instruction by instruction.
// $ROUNDTRIP_FILTER restricts the corpus to paths containing that string.
func TestRoundTrip(t *testing.T) {
	luajit := os.Getenv("LUAJIT")
	if luajit == "" {
		t.Skip("set LUAJIT to a LuaJIT 2.1 GC64 binary")
	}
	filter := os.Getenv("ROUNDTRIP_FILTER")
	tmp := t.TempDir()
	var (
		mu                          sync.Mutex
		files, syntaxErr            int
		iw8Files, iw8Exact          int
		protos, protosExact         int
		syntaxExamples, diffSamples []string
		wg                          sync.WaitGroup
		sem                         = make(chan struct{}, 16)
	)
	for i, path := range corpus(t) {
		if filter != "" && !strings.Contains(filepath.ToSlash(path), filter) {
			continue
		}
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Error(err)
				return
			}
			orig, err := bytecode.Parse(data)
			if errors.Is(err, bytecode.ErrEmpty) {
				return
			}
			if err != nil {
				t.Errorf("%s: %v", path, err)
				return
			}
			fn, _ := File(orig)
			var src bytes.Buffer
			if err := Print(&src, fn); err != nil {
				t.Errorf("%s: %v", path, err)
				return
			}
			in := filepath.Join(tmp, fmt.Sprintf("%d.lua", i))
			out := filepath.Join(tmp, fmt.Sprintf("%d.ljbc", i))
			if err := os.WriteFile(in, src.Bytes(), 0o644); err != nil {
				t.Error(err)
				return
			}
			cmd := exec.Command(luajit, "-b", "-s", in, out)
			// "luajit -b" loads jit/bcsave.lua, which sits next to the binary in a source build.
			cmd.Env = append(os.Environ(), "LUA_PATH="+filepath.Join(filepath.Dir(luajit), "?.lua")+";;")
			msg, err := cmd.CombinedOutput()

			mu.Lock()
			defer mu.Unlock()
			files++
			if err != nil {
				syntaxErr++
				if len(syntaxExamples) < 15 {
					syntaxExamples = append(syntaxExamples, path+": "+strings.TrimSpace(string(msg)))
				}
				return
			}
			if orig.Variant != bytecode.VariantIW8 {
				return
			}
			iw8Files++
			recompiled, err := os.ReadFile(out)
			if err != nil {
				t.Error(err)
				return
			}
			rf, err := bytecode.Parse(recompiled)
			if err != nil {
				t.Errorf("%s: reparse: %v", path, err)
				return
			}
			n, exact, diff := compareProtos(orig.Main, rf.Main, "f0")
			protos += n
			protosExact += exact
			if n == exact {
				iw8Exact++
			} else if len(diffSamples) < 25 {
				diffSamples = append(diffSamples, path+": "+diff)
			}
		})
	}
	wg.Wait()
	for _, s := range syntaxExamples {
		t.Log("compile error:", s)
	}
	for _, s := range diffSamples {
		t.Log("mismatch:", s)
	}
	t.Logf("%d files: %d failed to compile", files, syntaxErr)
	t.Logf("IW8: %d/%d files identical; %d/%d functions identical", iw8Exact, iw8Files, protosExact, protos)
	if syntaxErr > 0 {
		t.Errorf("%d decompiled files do not compile", syntaxErr)
	}
}

// compareProtos counts functions in the tree and how many recompiled identically.
func compareProtos(a, b *bytecode.Proto, name string) (total, exact int, firstDiff string) {
	total = 1
	if d := protoDiff(a, b); d == "" {
		exact = 1
	} else {
		firstDiff = name + ": " + d
	}
	if len(a.Children) != len(b.Children) {
		if firstDiff == "" {
			firstDiff = fmt.Sprintf("%s: %d children, recompiled %d", name, len(a.Children), len(b.Children))
		}
		return total + countProtos(a) - 1, exact, firstDiff
	}
	for i := range a.Children {
		n, e, d := compareProtos(a.Children[i], b.Children[i], fmt.Sprintf("%s.%d", name, i))
		total += n
		exact += e
		if firstDiff == "" {
			firstDiff = d
		}
	}
	return total, exact, firstDiff
}

func countProtos(p *bytecode.Proto) int {
	n := 1
	for _, c := range p.Children {
		n += countProtos(c)
	}
	return n
}

// protoDiff describes the first difference between two prototypes' code.
func protoDiff(a, b *bytecode.Proto) string {
	if a.NumParams != b.NumParams || a.IsVararg() != b.IsVararg() {
		return "signature differs"
	}
	for pc := range min(len(a.Code), len(b.Code)) {
		if !sameIns(a, b, a.Code[pc], b.Code[pc]) {
			return fmt.Sprintf("pc %d: %s, recompiled %s", pc, insString(a, a.Code[pc]), insString(b, b.Code[pc]))
		}
	}
	if len(a.Code) != len(b.Code) {
		return fmt.Sprintf("%d instructions, recompiled %d", len(a.Code), len(b.Code))
	}
	return ""
}

func operand(p *bytecode.Proto, m bytecode.Mode, v int) string {
	switch m {
	case bytecode.MTab:
		// Template hash order depends on the table's internal layout; compare as sets.
		if v < len(p.KGC) && p.KGC[v].Table != nil {
			t := p.KGC[v].Table
			parts := []string{fmt.Sprint(len(t.Array))}
			for i, a := range t.Array {
				parts = append(parts, fmt.Sprintf("[%d]=%s", i, a))
			}
			var hash []string
			for _, e := range t.Hash {
				hash = append(hash, fmt.Sprintf("[%s]=%s", e.Key, e.Value))
			}
			slices.Sort(hash)
			return "{" + strings.Join(append(parts, hash...), ",") + "}"
		}
	case bytecode.MStr, bytecode.MCdata:
		if v < len(p.KGC) {
			return p.KGC[v].String()
		}
	case bytecode.MFunc:
		return "fn"
	case bytecode.MNum:
		if v < len(p.KN) {
			c := p.KN[v]
			if c.Kind == bytecode.KInt {
				return bytecode.FormatNumber(float64(c.Int))
			}
			return bytecode.FormatNumber(c.Num)
		}
	}
	return fmt.Sprint(v)
}

func insString(p *bytecode.Proto, i bytecode.Ins) string {
	ma, mb, md := i.Op.Modes()
	s := []string{i.Op.String()}
	if ma != bytecode.MNone {
		s = append(s, operand(p, ma, int(i.A)))
	}
	if mb != bytecode.MNone {
		s = append(s, operand(p, mb, int(i.B)), operand(p, md, int(i.C)))
	} else if md != bytecode.MNone {
		s = append(s, operand(p, md, int(i.D)))
	}
	return strings.Join(s, " ")
}

func sameIns(pa, pb *bytecode.Proto, a, b bytecode.Ins) bool {
	return a.Op == b.Op && insString(pa, a) == insString(pb, b)
}
