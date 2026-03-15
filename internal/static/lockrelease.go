// Package static provides go/ssa-based static analysis for concurrency bugs.
// Currently detects lock release issues: functions that acquire a mutex lock
// on some code paths but do not release it on all exit paths.
package static

import (
	"fmt"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// Finding is a static analysis finding (no goroutine ID — this is compile-time).
type Finding struct {
	Function string // fully qualified function name
	Location string // file:line
	Message  string
}

// AnalyzeLockRelease loads the given Go package patterns and finds functions
// that acquire a sync.Mutex / sync.RWMutex lock on some code paths without
// releasing it on all exit paths.
//
// Uses go/ssa control-flow analysis: for each Lock() call, it checks whether
// every path from that point to a function exit passes through a matching
// Unlock() on the same mutex receiver. Functions with defer Unlock() are
// skipped (defer covers all exits).
//
// Two fixes over the original implementation:
//  1. Method iteration: methods are NOT in pkg.Members; accessed via MethodSets.
//  2. Receiver-aware matching: "same mutex" is determined by structural value ID
//     so that r.mu.Unlock() does not cancel a pending r.client.mu.RLock().
func AnalyzeLockRelease(pkgPatterns []string) ([]Finding, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedDeps |
			packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedTypesInfo,
		Tests: true,
	}

	loaded, err := packages.Load(cfg, pkgPatterns...)
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}

	var loadErrs []string
	for _, pkg := range loaded {
		for _, e := range pkg.Errors {
			loadErrs = append(loadErrs, e.Msg)
		}
	}
	if len(loadErrs) > 0 {
		return nil, fmt.Errorf("package load errors: %s", strings.Join(loadErrs, "; "))
	}

	prog, pkgs := ssautil.AllPackages(loaded, ssa.SanityCheckFunctions)
	prog.Build()

	var findings []Finding
	seen := make(map[*ssa.Function]bool)

	var analyzeWithAnon func(fn *ssa.Function)
	analyzeWithAnon = func(fn *ssa.Function) {
		if fn == nil || seen[fn] {
			return
		}
		seen[fn] = true
		findings = append(findings, analyzeFn(fn)...)
		for _, anon := range fn.AnonFuncs {
			analyzeWithAnon(anon)
		}
	}

	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}
		for _, mem := range pkg.Members {
			switch m := mem.(type) {
			case *ssa.Function:
				// Top-level package function.
				analyzeWithAnon(m)

			case *ssa.Type:
				// Named type — iterate both T and *T method sets.
				// Methods are NOT in pkg.Members; they require prog.MethodValue().
				named, ok := m.Type().(*types.Named)
				if !ok {
					continue
				}
				for _, t := range []types.Type{named, types.NewPointer(named)} {
					mset := prog.MethodSets.MethodSet(t)
					for i := 0; i < mset.Len(); i++ {
						analyzeWithAnon(prog.MethodValue(mset.At(i)))
					}
				}
			}
		}
	}

	return findings, nil
}

// isLockCall returns true if fn is a mutex Lock or RLock method.
func isLockCall(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	s := fn.String()
	return s == "(*sync.Mutex).Lock" ||
		s == "(*sync.RWMutex).Lock" ||
		s == "(*sync.RWMutex).RLock"
}

// isUnlockCall returns true if fn is a mutex Unlock or RUnlock method.
func isUnlockCall(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	s := fn.String()
	return s == "(*sync.Mutex).Unlock" ||
		s == "(*sync.RWMutex).Unlock" ||
		s == "(*sync.RWMutex).RUnlock"
}

// calleeFunc extracts the static callee from a CallCommon.
func calleeFunc(c ssa.CallCommon) *ssa.Function {
	if c.IsInvoke() {
		return nil
	}
	fn, _ := c.Value.(*ssa.Function)
	return fn
}

// receiverOf returns the receiver argument of a sync.Mutex/RWMutex call.
// In go/ssa, method calls are represented as c.Args[0] = the receiver pointer.
func receiverOf(c ssa.CallCommon) ssa.Value {
	if len(c.Args) > 0 {
		return c.Args[0]
	}
	return nil
}

// valueID computes a structural identity string for an SSA value by walking
// its definition chain. Two distinct SSA values that access the same struct
// field path from the same base will produce the same valueID, making it
// suitable for "same mutex" comparisons across multiple accesses in a function.
//
// Example: both occurrences of "r.client.mu" in a function produce
// the same valueID even though they are different *ssa.Value objects.
func valueID(v ssa.Value) string {
	if v == nil {
		return "nil"
	}
	switch v := v.(type) {
	case *ssa.Parameter:
		return "param:" + v.Name()
	case *ssa.FreeVar:
		return "freevar:" + v.Name()
	case *ssa.Global:
		return "global:" + v.RelString(nil)
	case *ssa.FieldAddr:
		return fmt.Sprintf("(%s).f%d", valueID(v.X), v.Field)
	case *ssa.UnOp:
		if v.Op == token.MUL {
			return "*(" + valueID(v.X) + ")"
		}
	case *ssa.Alloc:
		// Local allocations are unique per instruction — use their name.
		return "alloc:" + v.Name()
	}
	// Fallback: use the SSA value name. This prevents false positives (we
	// conservatively treat unknown values as distinct) but may miss some bugs.
	return "?" + v.Name()
}

// lockSite records where a Lock() call appears, including which mutex.
type lockSite struct {
	block       *ssa.BasicBlock
	idx         int       // instruction index within the block
	pos         token.Pos // source position
	receiverID  string    // structural identity of the mutex receiver
}

// unlockEntry records a single Unlock() call with its mutex receiver ID.
type unlockEntry struct {
	idx        int
	receiverID string
}

// analyzeFn checks whether fn has a lock acquired on some path without a
// corresponding release on all exit paths (receiver-aware).
func analyzeFn(fn *ssa.Function) []Finding {
	if len(fn.Blocks) == 0 {
		return nil
	}

	var lockSites []lockSite
	// unlocksByBlock[blockIndex] = list of (idx, receiverID) for Unlock() calls.
	unlocksByBlock := make(map[int][]unlockEntry)
	hasDeferUnlock := false

	for _, b := range fn.Blocks {
		for ii, instr := range b.Instrs {
			switch v := instr.(type) {
			case *ssa.Call:
				callee := calleeFunc(v.Call)
				recv := receiverOf(v.Call)
				if isLockCall(callee) {
					lockSites = append(lockSites, lockSite{b, ii, v.Pos(), valueID(recv)})
				} else if isUnlockCall(callee) {
					unlocksByBlock[b.Index] = append(unlocksByBlock[b.Index], unlockEntry{ii, valueID(recv)})
				}
			case *ssa.Defer:
				callee := calleeFunc(v.Call)
				if isUnlockCall(callee) {
					hasDeferUnlock = true
				}
			}
		}
	}

	if len(lockSites) == 0 || hasDeferUnlock {
		return nil
	}

	var findings []Finding
	fset := fn.Prog.Fset

	for _, ls := range lockSites {
		if pathExistsWithoutMatchingUnlock(fn, ls, unlocksByBlock) {
			pos := fset.Position(ls.pos)
			findings = append(findings, Finding{
				Function: fn.RelString(nil),
				Location: fmt.Sprintf("%s:%d", pos.Filename, pos.Line),
				Message:  "mutex Lock() acquired but not released on all exit paths",
			})
		}
	}

	return findings
}

// pathExistsWithoutMatchingUnlock returns true if there is at least one path
// from the Lock() instruction (ls) to a function exit that does not pass
// through a matching Unlock() on the same mutex receiver.
//
// "Same mutex" is determined by receiverID (structural value path).
func pathExistsWithoutMatchingUnlock(fn *ssa.Function, ls lockSite, unlocksByBlock map[int][]unlockEntry) bool {
	// Check rest of the lock's own block.
	if hasMatchingUnlockAfterIdx(ls.block, ls.idx+1, ls.receiverID, unlocksByBlock) {
		return false
	}

	// BFS over successor blocks.
	visited := make(map[int]bool)
	var queue []*ssa.BasicBlock
	for _, succ := range ls.block.Succs {
		if !visited[succ.Index] {
			visited[succ.Index] = true
			queue = append(queue, succ)
		}
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		// If this block has a matching Unlock(), lock is released here.
		if hasMatchingUnlockInBlock(cur, ls.receiverID, unlocksByBlock) {
			continue
		}

		// Exit block reached without matching unlock.
		if len(cur.Succs) == 0 {
			return true
		}

		for _, succ := range cur.Succs {
			if !visited[succ.Index] {
				visited[succ.Index] = true
				queue = append(queue, succ)
			}
		}
	}

	return false
}

func hasMatchingUnlockAfterIdx(b *ssa.BasicBlock, fromIdx int, receiverID string, unlocksByBlock map[int][]unlockEntry) bool {
	for _, u := range unlocksByBlock[b.Index] {
		if u.idx >= fromIdx && (receiverID == "" || u.receiverID == "" || u.receiverID == receiverID) {
			return true
		}
	}
	return false
}

func hasMatchingUnlockInBlock(b *ssa.BasicBlock, receiverID string, unlocksByBlock map[int][]unlockEntry) bool {
	for _, u := range unlocksByBlock[b.Index] {
		if receiverID == "" || u.receiverID == "" || u.receiverID == receiverID {
			return true
		}
	}
	return false
}
