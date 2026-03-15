// Package static — lock ordering analysis.
//
// AnalyzeLockOrder detects AB-BA (and N-way) lock ordering violations using
// interprocedural go/ssa analysis.  Two code paths have a violation when one
// acquires lock A then B while another acquires B then A (with both held
// simultaneously).
//
// Key algorithm:
//  1. For every function, compute the ordered sequence of mutex acquisitions
//     by walking the CFG while tracking a "currently held" lock set.
//  2. Whenever Lock(B) is called with lock A in the held set, add edge A→B to
//     the global lock-ordering graph.
//  3. Calls to non-primitive functions are expanded up to maxCallDepth levels
//     (interprocedural propagation), with the caller's held-set passed into
//     the callee so nested calls also contribute pairs.
//  4. Any directed cycle in the lock-ordering graph (detected with Tarjan's
//     SCC) represents a potential deadlock.
//
// Lock identity: locks are identified by a TYPE-LEVEL structural path of the
// form "(*pkg.Foo).field[3]".  This lets us match the same field accessed
// through different local variable names or different call sites, giving us
// cross-function-boundary stability without full pointer aliasing analysis.
package static

import (
	"fmt"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// LockOrderFinding reports a lock-ordering cycle.
type LockOrderFinding struct {
	Cycle    []string // lock type IDs in cycle order
	Location string   // representative source location (first lock in cycle)
	Function string   // function name where the representative edge was seen
	Message  string   // human-readable description
}

// maxCallDepth limits interprocedural expansion to avoid combinatorial blowup.
const maxCallDepth = 4

// AnalyzeLockOrder loads the given Go package patterns and finds functions
// that, together, acquire sync.Mutex / sync.RWMutex locks in an inconsistent
// order — a necessary condition for AB-BA deadlocks.
//
// It uses a type-level lock identity (struct type + field index) so that the
// same field accessed via different parameter names in different functions is
// treated as the same lock.
func AnalyzeLockOrder(pkgPatterns []string) ([]LockOrderFinding, error) {
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

	// Collect all functions to analyze (same traversal as lockrelease.go).
	var funcs []*ssa.Function
	seen := make(map[*ssa.Function]bool)

	var collectFuncs func(fn *ssa.Function)
	collectFuncs = func(fn *ssa.Function) {
		if fn == nil || seen[fn] {
			return
		}
		seen[fn] = true
		funcs = append(funcs, fn)
		for _, anon := range fn.AnonFuncs {
			collectFuncs(anon)
		}
	}

	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}
		for _, mem := range pkg.Members {
			switch m := mem.(type) {
			case *ssa.Function:
				collectFuncs(m)
			case *ssa.Type:
				named, ok := m.Type().(*types.Named)
				if !ok {
					continue
				}
				for _, t := range []types.Type{named, types.NewPointer(named)} {
					mset := prog.MethodSets.MethodSet(t)
					for i := 0; i < mset.Len(); i++ {
						collectFuncs(prog.MethodValue(mset.At(i)))
					}
				}
			}
		}
	}

	// --- Build global lock-ordering graph ---
	// Edge A→B: "while holding A, lock B is acquired"
	// value: list of (function, sourcePos) for the ordering edge
	type edgeInfo struct {
		function string
		location string
	}
	lockGraph := make(map[string]map[string]edgeInfo) // lockGraph[A][B] = edge info

	addEdge := func(from, to, function, location string) {
		if from == to || from == "" || to == "" {
			return
		}
		if lockGraph[from] == nil {
			lockGraph[from] = make(map[string]edgeInfo)
		}
		if _, exists := lockGraph[from][to]; !exists {
			lockGraph[from][to] = edgeInfo{function, location}
		}
	}

	// memo stores the set of lock acquisitions a function can perform,
	// keyed by function.  This avoids re-analyzing the same function.
	// Value: set of lock type IDs this function (directly or transitively) acquires.
	funcLockMemo := make(map[*ssa.Function]map[string]bool)

	fset := prog.Fset

	// analyzeFuncWithHeld performs intra-procedural CFG traversal for fn,
	// starting with the held-lock set passed in by the caller context.
	// It adds edges to lockGraph and returns the set of locks acquired.
	// The recursion depth is bounded to prevent stack overflow on large call graphs.
	var analyzeFuncWithHeld func(fn *ssa.Function, heldByCallerCopy map[string]bool, depth int) map[string]bool
	analyzeFuncWithHeld = func(fn *ssa.Function, heldByCaller map[string]bool, depth int) map[string]bool {
		if fn == nil || len(fn.Blocks) == 0 {
			return nil
		}

		// For memoized calls (depth == 0 with no caller context), use the cache.
		if depth == 0 && len(heldByCaller) == 0 {
			if cached, ok := funcLockMemo[fn]; ok {
				return cached
			}
		}

		// Guard against infinite recursion.
		if depth > maxCallDepth {
			return nil
		}

		// Prevent re-entrant recursion for the same function.
		if _, inProgress := funcLockMemo[fn]; inProgress && depth > 0 {
			return nil
		}
		if depth == 0 {
			funcLockMemo[fn] = nil // mark in-progress
		}

		// Track currently held locks (simulated at flow-insensitive level).
		// We propagate through blocks in order; for branches we take the
		// union (over-approximation is safe — more edges, potentially more
		// false positives, but never misses real cycles).
		held := make(map[string]bool)
		for k, v := range heldByCaller {
			held[k] = v
		}

		acquired := make(map[string]bool)

		for _, block := range fn.Blocks {
			for _, instr := range block.Instrs {
				switch v := instr.(type) {
				case *ssa.Call:
					callee := calleeFunc(v.Call)
					recv := receiverOf(v.Call)
					if isLockCall(callee) {
						lid := typeLockID(recv, fset)
						if lid != "" {
							// Add ordering edges from every currently-held lock.
							pos := fset.Position(v.Pos())
							loc := ""
							if pos.IsValid() {
								loc = fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
							}
							for h := range held {
								addEdge(h, lid, fn.RelString(nil), loc)
							}
							held[lid] = true
							acquired[lid] = true
						}
					} else if isUnlockCall(callee) {
						lid := typeLockID(recv, fset)
						delete(held, lid)
					} else if callee != nil && depth < maxCallDepth &&
						!isRuntimeSSAFunc(callee) && callee != fn {
						// Interprocedural: expand callee with current held set.
						sub := analyzeFuncWithHeld(callee, held, depth+1)
						for l := range sub {
							acquired[l] = true
							// Ordering: every lock we currently hold comes before
							// every lock the callee acquires.
							pos := fset.Position(v.Pos())
							loc := ""
							if pos.IsValid() {
								loc = fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
							}
							for h := range held {
								addEdge(h, l, fn.RelString(nil), loc)
							}
						}
					}
				case *ssa.Defer:
					// defer Unlock — simplification: we don't remove the lock from
					// 'held' here because deferred calls happen at function exit, not
					// at this instruction.  This is a conservative over-approximation.
					callee := calleeFunc(v.Call)
					if isLockCall(callee) {
						recv := receiverOf(v.Call)
						lid := typeLockID(recv, fset)
						if lid != "" {
							held[lid] = true
							acquired[lid] = true
						}
					}
				}
			}
		}

		if depth == 0 {
			funcLockMemo[fn] = acquired
		}
		return acquired
	}

	// First pass: analyze every collected function.
	for _, fn := range funcs {
		analyzeFuncWithHeld(fn, nil, 0)
	}

	// --- Tarjan's SCC on the lock-ordering graph ---
	nodes := make(map[string]bool)
	for from, tos := range lockGraph {
		nodes[from] = true
		for to := range tos {
			nodes[to] = true
		}
	}

	type tarjanState struct {
		index   int
		lowlink int
		onStack bool
		visited bool
	}
	states := make(map[string]*tarjanState, len(nodes))
	var sccStack []string
	var sccs [][]string
	nextIdx := 0

	var strongConnect func(v string)
	strongConnect = func(v string) {
		s := &tarjanState{index: nextIdx, lowlink: nextIdx, onStack: true, visited: true}
		states[v] = s
		nextIdx++
		sccStack = append(sccStack, v)

		for w := range lockGraph[v] {
			ws, visited := states[w]
			if !visited {
				strongConnect(w)
				if states[w].lowlink < s.lowlink {
					s.lowlink = states[w].lowlink
				}
			} else if ws.onStack {
				if ws.index < s.lowlink {
					s.lowlink = ws.index
				}
			}
		}

		if s.lowlink == s.index {
			var scc []string
			for {
				w := sccStack[len(sccStack)-1]
				sccStack = sccStack[:len(sccStack)-1]
				states[w].onStack = false
				scc = append(scc, w)
				if w == v {
					break
				}
			}
			if len(scc) >= 2 {
				sccs = append(sccs, scc)
			}
		}
	}

	for node := range nodes {
		if s, ok := states[node]; !ok || !s.visited {
			strongConnect(node)
		}
	}

	// --- Convert SCCs to findings ---
	var findings []LockOrderFinding
	seenSCC := make(map[string]bool)

	for _, scc := range sccs {
		sorted := make([]string, len(scc))
		copy(sorted, scc)
		sort.Strings(sorted)
		key := strings.Join(sorted, "|")
		if seenSCC[key] {
			continue
		}
		seenSCC[key] = true

		// Find a representative edge (best source location).
		repFunc := ""
		repLoc := ""
		for _, from := range scc {
			sccSet := make(map[string]bool, len(scc))
			for _, n := range scc {
				sccSet[n] = true
			}
			for to := range lockGraph[from] {
				if !sccSet[to] {
					continue
				}
				info := lockGraph[from][to]
				if repLoc == "" || info.location != "" {
					repFunc = info.function
					repLoc = info.location
				}
			}
		}

		desc := "AB-BA lock inversion"
		if len(scc) > 2 {
			desc = fmt.Sprintf("%d-way lock cycle", len(scc))
		}

		findings = append(findings, LockOrderFinding{
			Cycle:    sorted,
			Location: repLoc,
			Function: repFunc,
			Message:  fmt.Sprintf("lock ordering cycle (%s): %s", desc, strings.Join(sorted, " → ")),
		})
	}

	return findings, nil
}

// typeLockID returns a type-level structural identity for a mutex receiver value.
// Two FieldAddr values that access the same field on the same struct type produce
// the same ID even if they come from different function parameters or local variables.
//
// Examples:
//   c.mu (c *tableNameCache, mu is field 0) → "(*pkg.tableNameCache).field[0]"
//   l.mu (l *LeaseState, mu is field 0)     → "(*pkg.LeaseState).field[0]"
func typeLockID(v ssa.Value, _ *token.FileSet) string {
	if v == nil {
		return ""
	}
	return typeLockIDRec(v, 0)
}

func typeLockIDRec(v ssa.Value, depth int) string {
	if v == nil || depth > 6 {
		return ""
	}
	switch v := v.(type) {
	case *ssa.FieldAddr:
		// The base type of the struct containing this field.
		baseType := v.X.Type()
		// Try to resolve the actual field name from the struct type.
		fieldName := fieldNameAt(baseType, v.Field)
		if fieldName != "" {
			return fmt.Sprintf("(%s).%s", shortTypeName(baseType.String()), fieldName)
		}
		return fmt.Sprintf("(%s).field[%d]", shortTypeName(baseType.String()), v.Field)
	case *ssa.UnOp:
		if v.Op == token.MUL {
			inner := typeLockIDRec(v.X, depth+1)
			if inner != "" {
				return "*(" + inner + ")"
			}
		}
	case *ssa.Alloc:
		// Local struct alloc — use the type directly.
		t := v.Type()
		return fmt.Sprintf("alloc:(%s)", shortTypeName(t.String()))
	case *ssa.Parameter:
		// For receiver parameters, use the type + field[0] as identity
		// since receivers are typically passed as first arg.
		return fmt.Sprintf("param:(%s)", shortTypeName(v.Type().String()))
	case *ssa.Global:
		return "global:" + v.RelString(nil)
	}
	// Fallback: use the value's type as a coarse identity.
	return fmt.Sprintf("?(%s)", shortTypeName(v.Type().String()))
}

// fieldNameAt returns the name of the struct field at the given index,
// dereferencing pointer types as needed.  Returns "" if not resolvable.
func fieldNameAt(t types.Type, idx int) string {
	// Dereference pointer.
	ptr, ok := t.Underlying().(*types.Pointer)
	if !ok {
		return ""
	}
	s, ok := ptr.Elem().Underlying().(*types.Struct)
	if !ok {
		return ""
	}
	if idx < 0 || idx >= s.NumFields() {
		return ""
	}
	return s.Field(idx).Name()
}

// shortTypeName strips the import path from a qualified type name for display,
// keeping only the unqualified type name (no package prefix).
//
// Examples:
//   "*github.com/foo/bar/baz.MyStruct" → "*MyStruct"
//   "*sync.RWMutex"                    → "*sync.RWMutex"  (stdlib kept)
//   "(*github.com/foo.T).field[2]"     → "(*T).field[2]"
func shortTypeName(s string) string {
	// We process the string token by token, replacing "pkg/path.Type" with "Type".
	// For stdlib packages (no "/"), we keep "pkg.Type" for clarity.
	var result strings.Builder
	i := 0
	for i < len(s) {
		// Find the start of a qualified identifier (after * ( , space).
		if s[i] == '*' || s[i] == '(' || s[i] == ')' || s[i] == ' ' || s[i] == ',' || s[i] == '[' || s[i] == ']' {
			result.WriteByte(s[i])
			i++
			continue
		}
		// Read a token until a non-identifier boundary.
		j := i
		for j < len(s) && s[j] != ' ' && s[j] != ',' && s[j] != ')' && s[j] != '(' && s[j] != '[' && s[j] != ']' {
			j++
		}
		token := s[i:j]
		i = j
		// If the token contains a "/", it's an import path — strip to last ".".
		if strings.Contains(token, "/") {
			if dot := strings.LastIndex(token, "."); dot >= 0 {
				result.WriteString(token[dot+1:]) // just the type name
			} else {
				result.WriteString(token) // no dot, keep as-is
			}
		} else {
			result.WriteString(token)
		}
	}
	return result.String()
}

// ChanLockFinding reports a potential chan-lock deadlock: a function that
// holds a mutex while performing a channel operation on the same struct.
type ChanLockFinding struct {
	Function     string
	Location     string
	LockTypeID   string // type-level ID of the held mutex
	ChanTypeID   string // type-level ID of the channel being operated on
	Operation    string // "chan send" or "chan receive"
	Message      string
}

// AnalyzeChanLockHolding finds functions that hold a sync.Mutex/RWMutex while
// performing a channel send or receive, where BOTH the mutex AND the channel
// are fields of the same struct type.
//
// This pattern can cause a deadlock if:
//   - Goroutine G1 holds the mutex and blocks on the channel operation (e.g.
//     because the channel is full or has no sender)
//   - Goroutine G2 tries to acquire the same mutex — G2 is blocked waiting
//     for G1's mutex, while G1 is waiting for the channel (which G2 or some
//     other goroutine dependent on G2 would unblock).
//
// The check "mutex and channel are fields of the same struct" is a strong
// heuristic for this pattern.  False-positive risk is low because most
// designs either do not share lock and channel in the same struct, or they
// do so intentionally to protect the channel (in which case no other goroutine
// blocks on the mutex during the channel operation).
func AnalyzeChanLockHolding(pkgPatterns []string) ([]ChanLockFinding, error) {
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

	fset := prog.Fset

	// collect all user functions (same method-set traversal as other analyses).
	var funcs []*ssa.Function
	seenFn := make(map[*ssa.Function]bool)
	var collectFn func(fn *ssa.Function)
	collectFn = func(fn *ssa.Function) {
		if fn == nil || seenFn[fn] {
			return
		}
		seenFn[fn] = true
		funcs = append(funcs, fn)
		for _, anon := range fn.AnonFuncs {
			collectFn(anon)
		}
	}
	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}
		for _, mem := range pkg.Members {
			switch m := mem.(type) {
			case *ssa.Function:
				collectFn(m)
			case *ssa.Type:
				named, ok := m.Type().(*types.Named)
				if !ok {
					continue
				}
				for _, t := range []types.Type{named, types.NewPointer(named)} {
					mset := prog.MethodSets.MethodSet(t)
					for i := 0; i < mset.Len(); i++ {
						collectFn(prog.MethodValue(mset.At(i)))
					}
				}
			}
		}
	}

	var findings []ChanLockFinding
	seenKey := make(map[string]bool)

	for _, fn := range funcs {
		if len(fn.Blocks) == 0 {
			continue
		}

		// Track currently held locks by type-level ID.
		// Flow-insensitive: walk instructions in block order, union across branches.
		held := make(map[string]bool)

		for _, block := range fn.Blocks {
			for _, instr := range block.Instrs {
				switch v := instr.(type) {
				case *ssa.Call:
					callee := calleeFunc(v.Call)
					recv := receiverOf(v.Call)
					if isLockCall(callee) {
						lid := typeLockID(recv, fset)
						if lid != "" {
							held[lid] = true
						}
					} else if isUnlockCall(callee) {
						lid := typeLockID(recv, fset)
						delete(held, lid)
					}

				case *ssa.Defer:
					// defer Lock() — held for the function's duration.
					callee := calleeFunc(v.Call)
					recv := receiverOf(v.Call)
					if isLockCall(callee) {
						lid := typeLockID(recv, fset)
						if lid != "" {
							held[lid] = true
						}
					}

				case *ssa.Send:
					// Channel send while holding a lock.
					if len(held) == 0 {
						continue
					}
					chanID := chanFieldTypeID(v.Chan)
					if chanID == "" {
						continue
					}
					// Check if any held lock shares the same struct base type.
					for lockID := range held {
						if sameBaseStructType(lockID, chanID) {
							key := fn.RelString(nil) + "|" + lockID + "|" + chanID + "|send"
							if seenKey[key] {
								continue
							}
							seenKey[key] = true
							pos := fset.Position(v.Pos())
							loc := ""
							if pos.IsValid() {
								loc = fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
							}
							findings = append(findings, ChanLockFinding{
								Function:   fn.RelString(nil),
								Location:   loc,
								LockTypeID: lockID,
								ChanTypeID: chanID,
								Operation:  "chan send",
								Message: fmt.Sprintf(
									"mutex %s held during channel send on %s — "+
										"deadlock if channel blocks and another goroutine waits for this mutex",
									shortTypeID(lockID), shortTypeID(chanID)),
							})
						}
					}

				default:
					// Check for channel receive: UnOp with ARROW operator.
					if un, ok := instr.(*ssa.UnOp); ok && un.Op == token.ARROW {
						if len(held) == 0 {
							continue
						}
						chanID := chanFieldTypeID(un.X)
						if chanID == "" {
							continue
						}
						for lockID := range held {
							if sameBaseStructType(lockID, chanID) {
								key := fn.RelString(nil) + "|" + lockID + "|" + chanID + "|recv"
								if seenKey[key] {
									continue
								}
								seenKey[key] = true
								pos := fset.Position(un.Pos())
								loc := ""
								if pos.IsValid() {
									loc = fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
								}
								findings = append(findings, ChanLockFinding{
									Function:   fn.RelString(nil),
									Location:   loc,
									LockTypeID: lockID,
									ChanTypeID: chanID,
									Operation:  "chan receive",
									Message: fmt.Sprintf(
										"mutex %s held during channel receive on %s — "+
											"deadlock if channel blocks and another goroutine waits for this mutex",
										shortTypeID(lockID), shortTypeID(chanID)),
								})
							}
						}
					}
				}
			}
		}
	}

	return findings, nil
}

// chanFieldTypeID extracts a type-level ID for the struct field that a channel
// value is loaded from.  Returns "" if the channel is not a struct field load.
//
// In go/ssa, a field channel load looks like:
//   t1 = &recv.notifyCh        ← *ssa.FieldAddr
//   t2 = *t1                   ← *ssa.UnOp(MUL)  [= "load t1"]
//   send t2 ...                ← *ssa.Send
//
// We look through the UnOp load to the FieldAddr.
func chanFieldTypeID(v ssa.Value) string {
	if v == nil {
		return ""
	}
	var fa *ssa.FieldAddr
	if f, ok := v.(*ssa.FieldAddr); ok {
		fa = f
	} else if un, ok := v.(*ssa.UnOp); ok && un.Op == token.MUL {
		fa, _ = un.X.(*ssa.FieldAddr)
	}
	if fa == nil {
		return ""
	}
	baseType := fa.X.Type()
	fieldName := fieldNameAt(baseType, fa.Field)
	if fieldName != "" {
		return fmt.Sprintf("(%s).%s", shortTypeName(baseType.String()), fieldName)
	}
	return fmt.Sprintf("(%s).chanfield[%d]", shortTypeName(baseType.String()), fa.Field)
}

// sameBaseStructType returns true if both a lock type ID and a channel type ID
// refer to fields of the same struct type.
//
// Lock IDs have the form:    "(*pkg.T).field[N]"
// Channel IDs have the form: "(*pkg.T).chanfield[M]"
// Both share the same base "(base_type)" prefix.
func sameBaseStructType(lockID, chanID string) bool {
	lockBase := extractBaseType(lockID)
	chanBase := extractBaseType(chanID)
	return lockBase != "" && chanBase != "" && lockBase == chanBase
}

// extractBaseType extracts the parenthesized base type prefix from a type ID.
// For "(*pkg.T).field[3]" → "(*pkg.T)"
// For "(*pkg.T).chanfield[1]" → "(*pkg.T)"
func extractBaseType(typeID string) string {
	if !strings.HasPrefix(typeID, "(") {
		return ""
	}
	end := strings.Index(typeID, ")")
	if end < 0 {
		return ""
	}
	return typeID[:end+1]
}

// shortTypeID extracts the unqualified type and field name for display.
// "(*github.com/foo/bar.MyStruct).field[2]" → "MyStruct.field[2]"
func shortTypeID(id string) string {
	// Remove package path, keep just the struct name and field index.
	if dot := strings.LastIndex(id, "."); dot >= 0 {
		// Find the closing paren or bracket after the last dot.
		suffix := id[dot+1:]
		return suffix
	}
	return id
}

// isRuntimeSSAFunc returns true if the function is a runtime/stdlib function
// that we should not expand interprocedurally (to keep analysis bounded).
func isRuntimeSSAFunc(fn *ssa.Function) bool {
	if fn == nil {
		return true
	}
	pkg := fn.Package()
	if pkg == nil {
		return true // built-in or synthetic
	}
	path := pkg.Pkg.Path()
	return strings.HasPrefix(path, "runtime") ||
		strings.HasPrefix(path, "sync") ||
		strings.HasPrefix(path, "testing") ||
		strings.HasPrefix(path, "reflect") ||
		strings.HasPrefix(path, "internal/")
}
