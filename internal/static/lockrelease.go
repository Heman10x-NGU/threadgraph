// Package static provides go/ssa-based static analysis for concurrency bugs.
// Currently detects lock release issues: functions that acquire a mutex lock
// on some code paths but do not release it on all exit paths.
package static

import (
	"fmt"
	"go/token"
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
// every path from that point to a function exit passes through Unlock().
// Functions with defer Unlock() are skipped (defer covers all exits).
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

	// Check for load errors
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
	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}
		for _, mem := range pkg.Members {
			fn, ok := mem.(*ssa.Function)
			if !ok {
				continue
			}
			findings = append(findings, analyzeFn(fn)...)
			// Check anonymous functions (closures) within this function.
			for _, anon := range fn.AnonFuncs {
				findings = append(findings, analyzeFn(anon)...)
			}
		}
	}

	return findings, nil
}

// isLockCall returns true if fn is sync.Mutex.Lock, sync.RWMutex.Lock, or
// sync.RWMutex.RLock.
func isLockCall(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	s := fn.String()
	return s == "(*sync.Mutex).Lock" ||
		s == "(*sync.RWMutex).Lock" ||
		s == "(*sync.RWMutex).RLock"
}

// isUnlockCall returns true if fn is sync.Mutex.Unlock, sync.RWMutex.Unlock,
// or sync.RWMutex.RUnlock.
func isUnlockCall(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	s := fn.String()
	return s == "(*sync.Mutex).Unlock" ||
		s == "(*sync.RWMutex).Unlock" ||
		s == "(*sync.RWMutex).RUnlock"
}

// calleeFunc extracts the static callee from a CallCommon, or returns nil for
// interface/dynamic calls.
func calleeFunc(c ssa.CallCommon) *ssa.Function {
	if c.IsInvoke() {
		return nil // interface call
	}
	fn, _ := c.Value.(*ssa.Function)
	return fn
}

// lockSite records where a Lock() call appears.
type lockSite struct {
	block *ssa.BasicBlock
	idx   int       // instruction index within the block
	pos   token.Pos // source position
}

// analyzeFn checks whether fn has a lock acquired on some path without a
// corresponding release on all exit paths.
func analyzeFn(fn *ssa.Function) []Finding {
	if len(fn.Blocks) == 0 {
		return nil
	}

	// Phase 1: find all Lock() and Unlock() sites, and defer Unlock().
	var lockSites []lockSite
	// unlockIdx[blockIndex] = list of instruction indices with Unlock() calls.
	unlockIdx := make(map[int][]int)
	hasDeferUnlock := false

	for _, b := range fn.Blocks {
		for ii, instr := range b.Instrs {
			switch v := instr.(type) {
			case *ssa.Call:
				callee := calleeFunc(v.Call)
				if isLockCall(callee) {
					lockSites = append(lockSites, lockSite{b, ii, v.Pos()})
				} else if isUnlockCall(callee) {
					unlockIdx[b.Index] = append(unlockIdx[b.Index], ii)
				}
			case *ssa.Defer:
				callee := calleeFunc(v.Call)
				if isUnlockCall(callee) {
					// defer Unlock() covers all exit paths — function is safe.
					hasDeferUnlock = true
				}
			}
		}
	}

	// If no locks, or defer Unlock() covers all paths, nothing to report.
	if len(lockSites) == 0 || hasDeferUnlock {
		return nil
	}

	// Phase 2: for each Lock() site, check whether all CFG paths from that
	// point to a function exit pass through an Unlock().
	var findings []Finding
	fset := fn.Prog.Fset

	for _, ls := range lockSites {
		if pathExistsWithoutUnlock(fn, ls, unlockIdx) {
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

// pathExistsWithoutUnlock returns true if there is at least one path from the
// Lock() instruction (ls) to a function exit that does not pass through any
// Unlock() call.
//
// Algorithm:
//  1. Check the rest of ls.block after the Lock() instruction for an Unlock().
//     If found, the lock is released within the same block and we need not
//     propagate to successors from that point.
//  2. BFS over successor blocks. A block "consumes" the lock-held state if it
//     contains an Unlock() — we stop propagating through that block.
//  3. If BFS reaches a block with no successors (function exit) without having
//     passed through an Unlock(), return true.
func pathExistsWithoutUnlock(fn *ssa.Function, ls lockSite, unlockIdx map[int][]int) bool {
	// Check rest of the lock's block (instructions after the Lock() call).
	if hasUnlockAfterIdx(ls.block, ls.idx+1, unlockIdx) {
		// Unlocked in the same block — safe along paths that go through this block.
		// We still need to check: are there paths through the successors that
		// bypass this block's unlock? No — same-block unlock means the lock IS
		// released before control leaves the block.
		return false
	}

	// BFS from the lock block's successors.
	visited := make(map[int]bool)
	queue := make([]*ssa.BasicBlock, 0, len(ls.block.Succs))
	for _, succ := range ls.block.Succs {
		if !visited[succ.Index] {
			visited[succ.Index] = true
			queue = append(queue, succ)
		}
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		// If this block has an Unlock(), the lock is released here — don't
		// propagate further from this block.
		if len(unlockIdx[cur.Index]) > 0 {
			continue
		}

		// No unlock in this block. If it's an exit block (no successors),
		// we have found a path without unlock.
		if len(cur.Succs) == 0 {
			return true
		}

		// Continue BFS.
		for _, succ := range cur.Succs {
			if !visited[succ.Index] {
				visited[succ.Index] = true
				queue = append(queue, succ)
			}
		}
	}

	return false
}

// hasUnlockAfterIdx returns true if block b has an Unlock() call at an
// instruction index >= fromIdx.
func hasUnlockAfterIdx(b *ssa.BasicBlock, fromIdx int, unlockIdx map[int][]int) bool {
	for _, ui := range unlockIdx[b.Index] {
		if ui >= fromIdx {
			return true
		}
	}
	return false
}
