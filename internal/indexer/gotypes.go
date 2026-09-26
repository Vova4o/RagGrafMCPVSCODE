package indexer

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

// goTypeCheckBudget bounds the total wall-clock time indexGoTypes spends
// type-checking every Go module discovered in a single Index call, so a
// repository with many modules cannot block a query-triggered refresh for
// N times a per-module timeout. Modules that cannot be reached, or whose
// packages.Load call does not finish, within the budget fall back to the
// AST-based heuristic.
//
// Test hook: tests may lower this package-level var to force degradation
// deterministically. It must be restored (and the mutating test must not
// call t.Parallel) since it is shared, unsynchronized state.
var goTypeCheckBudget = 60 * time.Second

// externalRef names a call target that resolves outside the set of nodes the
// indexer already created for the repository.
type externalRef struct {
	qualified string
	name      string
}

// indexGoTypes type-checks every Go module found under root and emits
// type-proven CALLS and IMPLEMENTS edges. It returns the set of repository
// files whose calls were fully resolved from type information (so the
// name-based fallback in Index skips them) and the list of files that could
// not be type-checked, with a reason for each.
func indexGoTypes(
	ctx context.Context,
	root, modulePath string,
	parsed []parsedFile,
	g *graph.Graph,
	edges map[string]graph.Edge,
	nodesByQualified map[string]graph.Node,
	funcPositionIndex map[string]string,
	typePositionIndex map[string]string,
	functionsByPackage map[string]map[string]string,
	methodsByPackage map[string]map[string][]string,
	methodsByName map[string][]string,
) (map[string]bool, []graph.SkippedFile, error) {
	moduleDirs, err := discoverGoModuleDirs(ctx, root)
	if err != nil {
		return nil, nil, err
	}

	typedFiles := make(map[string]bool)
	var degraded []graph.SkippedFile

	if len(moduleDirs) == 0 {
		for _, file := range parsed {
			degraded = append(degraded, graph.SkippedFile{File: file.rel, Reason: "file is outside any go module"})
		}
		return typedFiles, degraded, nil
	}

	filesByModule := make(map[string][]parsedFile)
	fileInfo := make(map[string]parsedFile, len(parsed))
	for _, file := range parsed {
		fileInfo[file.rel] = file
		dir := moduleDirFor(moduleDirs, filepath.Dir(file.absPath))
		if dir == "" {
			degraded = append(degraded, graph.SkippedFile{File: file.rel, Reason: "file is outside any go module"})
			continue
		}
		filesByModule[dir] = append(filesByModule[dir], file)
	}

	namedTypes := make(map[string]*types.Named)
	namedInterfaces := make(map[string]*types.Named)
	variantNamedTypes := make(map[string]map[string]*types.Named)
	variantNamedInterfaces := make(map[string]map[string]*types.Named)
	variantLocalTypes := make(map[string]map[string]*types.Named)
	variantLocalInterfaces := make(map[string]map[string]*types.Named)

	budgetDeadline := time.Now().Add(goTypeCheckBudget)

	for _, moduleDir := range moduleDirs {
		files := filesByModule[moduleDir]
		if len(files) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("index repository: %w", err)
		}

		remaining := time.Until(budgetDeadline)
		if remaining <= 0 {
			for _, file := range files {
				degraded = append(degraded, graph.SkippedFile{File: file.rel, Reason: "type check budget exceeded"})
			}
			continue
		}

		loadCtx, cancel := context.WithTimeout(ctx, remaining)
		pkgs, loadErr := loadGoPackages(loadCtx, moduleDir)
		budgetExceeded := loadCtx.Err() == context.DeadlineExceeded
		cancel()
		if loadErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, nil, fmt.Errorf("index repository: %w", ctxErr)
			}
			reason := loadErr.Error()
			if budgetExceeded {
				reason = "type check budget exceeded"
			}
			for _, file := range files {
				degraded = append(degraded, graph.SkippedFile{File: file.rel, Reason: reason})
			}
			continue
		}

		covered := make(map[string]bool)
		succeeded := make(map[string]bool)
		var candidateDegraded []graph.SkippedFile
		for _, pkg := range pkgs {
			if err := ctx.Err(); err != nil {
				return nil, nil, fmt.Errorf("index repository: %w", err)
			}
			var coveredRel []string
			for _, absFile := range pkg.CompiledGoFiles {
				rel, ok := relUnderRoot(root, absFile)
				if !ok {
					continue
				}
				covered[rel] = true
				coveredRel = append(coveredRel, rel)
			}
			if pkg.Types == nil || pkg.TypesInfo == nil {
				// The package could not be type-checked at all (for example a
				// parse failure severe enough that go/packages never ran
				// types.Check), so no per-call type information exists to
				// salvage: every file in it falls back to the name heuristic.
				reason := firstPackageErrorReason(pkg)
				for _, rel := range coveredRel {
					candidateDegraded = append(candidateDegraded, graph.SkippedFile{File: rel, Reason: reason})
				}
				continue
			}

			// A package with errors still resolves some identifiers: Uses,
			// Selections, and Defs contain entries only for what go/types
			// proved sound. extractTypedCalls emits a typed edge for every
			// call it can prove and falls back to the AST-only heuristic,
			// call by call, only where type information is absent.
			heuristicFiles := extractTypedCalls(
				pkg, root, funcPositionIndex, g, edges, nodesByQualified,
				fileInfo, functionsByPackage, methodsByPackage, methodsByName,
			)
			for _, rel := range coveredRel {
				// Every file covered here had extractTypedCalls run over its
				// calls (typed where provable, heuristic per call otherwise),
				// so it must never also be reprocessed whole-file by the
				// AST-only heuristic loop in Index.
				typedFiles[rel] = true
				if heuristicFiles[rel] {
					reason := "partial type information: " + firstPackageErrorReason(pkg)
					if len(pkg.Errors) == 0 && !pkg.IllTyped {
						// A clean package (no pkg.Errors, not IllTyped) still
						// reached the AST-only heuristic for one of its
						// calls: an unforeseen call shape that
						// resolveTypedCallee does not yet resolve from type
						// information. Report it under a distinct reason
						// instead of a package error description, and never
						// fail the whole index over it: one unresolved call
						// shape must degrade only this file, not every graph
						// query for the repository.
						reason = "unresolved call in type-checked package"
					}
					candidateDegraded = append(candidateDegraded, graph.SkippedFile{File: rel, Reason: reason})
					continue
				}
				succeeded[rel] = true
			}

			if isTestVariantPackage(pkg.ID) {
				variantTypes := make(map[string]*types.Named)
				variantInterfaces := make(map[string]*types.Named)
				localTypes := make(map[string]*types.Named)
				localInterfaces := make(map[string]*types.Named)
				collectNamedDeclarationsWithLocal(pkg, root, typePositionIndex, variantTypes, variantInterfaces, localTypes, localInterfaces)
				variantNamedTypes[pkg.ID] = variantTypes
				variantNamedInterfaces[pkg.ID] = variantInterfaces
				variantLocalTypes[pkg.ID] = localTypes
				variantLocalInterfaces[pkg.ID] = localInterfaces
			} else {
				collectNamedDeclarations(pkg, root, typePositionIndex, namedTypes, namedInterfaces)
			}
		}
		// A production file compiled cleanly under its own package but is
		// reported as errored only inside a broken test-variant package (for
		// example "p [p.test]" when an internal _test.go file fails to
		// type-check) must not be marked degraded: it was fully resolved.
		for _, entry := range candidateDegraded {
			if succeeded[entry.File] {
				continue
			}
			degraded = append(degraded, entry)
		}

		for _, file := range files {
			if covered[file.rel] {
				continue
			}
			degraded = append(degraded, graph.SkippedFile{
				File: file.rel, Reason: "excluded from build (build constraints or unsupported by go/packages)",
			})
		}
	}

	emitImplementsEdges(edges, namedTypes, namedInterfaces)
	for variantID, variantTypes := range variantNamedTypes {
		universeTypes := mergeNamed(namedTypes, variantTypes)
		universeInterfaces := mergeNamed(namedInterfaces, variantNamedInterfaces[variantID])
		emitVariantImplementsEdges(edges, variantLocalTypes[variantID], variantLocalInterfaces[variantID], universeTypes, universeInterfaces)
	}

	return typedFiles, dedupeSkippedFiles(degraded), nil
}

// isTestVariantPackage reports whether id names a go/packages test-variant
// package (for example "example.com/p [example.com/p.test]"), which
// type-checks its own copy of package p's declarations in a universe that
// is not comparable by pointer identity with the plain package's universe.
func isTestVariantPackage(id string) bool {
	return strings.Contains(id, " [")
}

// mergeNamed returns a map containing every entry of base, with override's
// entries taking precedence. It lets a test-variant package's own
// re-compiled declarations replace the production package's declarations
// for the purpose of computing IMPLEMENTS edges within that variant's own
// type-checking universe, while still comparing against every other
// package's production declarations.
func mergeNamed(base, override map[string]*types.Named) map[string]*types.Named {
	merged := make(map[string]*types.Named, len(base)+len(override))
	for id, named := range base {
		merged[id] = named
	}
	for id, named := range override {
		merged[id] = named
	}
	return merged
}

// implementsCheckHook is a test-only seam that checkImplementsPairs invokes
// once per (type, interface) pair it evaluates with types.Implements,
// letting a test assert the total check count stays proportional to one
// full pass over the primary universe plus each test variant's own
// newly-declared pairs, instead of being multiplied by the number of test
// variants in the module. Tests that set it must restore the previous value
// via defer and must not run in parallel with other tests, since it is
// shared, unsynchronized state.
var implementsCheckHook func()

// emitImplementsEdges records a typed IMPLEMENTS edge for every (type,
// interface) pair in namedTypes/namedInterfaces where the type satisfies
// the interface. Both maps must come from the same type-checking universe:
// mixing *types.Named values from different go/packages.Load results (or
// different test-variant packages) can never report a satisfied interface,
// even when the two declarations are textually identical.
func emitImplementsEdges(edges map[string]graph.Edge, namedTypes, namedInterfaces map[string]*types.Named) {
	checkImplementsPairs(edges, namedTypes, namedInterfaces)
}

// emitVariantImplementsEdges records the IMPLEMENTS edges a test-variant
// package's own universe can introduce, without repeating the full
// O(len(types)×len(interfaces)) sweep emitImplementsEdges already performed
// once for the primary, non-variant universe. A test-variant package
// recompiles nearly the entire production package alongside its own test
// files, so checking every pair in its near-full-size universe against
// itself, for every variant in the module, would multiply the cost of the
// primary pass by the number of variants. Instead this only checks pairs
// that involve at least one type or interface declared in that variant's
// own test files (localTypes/localInterfaces), each checked against the
// variant's full universe (universeTypes/universeInterfaces), which is the
// only set of pairs a test file could actually have changed.
func emitVariantImplementsEdges(
	edges map[string]graph.Edge,
	localTypes, localInterfaces map[string]*types.Named,
	universeTypes, universeInterfaces map[string]*types.Named,
) {
	checkImplementsPairs(edges, localTypes, universeInterfaces)
	checkImplementsPairs(edges, universeTypes, localInterfaces)
}

// checkImplementsPairs records a typed IMPLEMENTS edge for every (type,
// interface) pair in namedTypes/namedInterfaces where the type satisfies
// the interface.
func checkImplementsPairs(edges map[string]graph.Edge, namedTypes, namedInterfaces map[string]*types.Named) {
	for typeID, named := range namedTypes {
		for ifaceID, iface := range namedInterfaces {
			if typeID == ifaceID {
				continue
			}
			underlying, ok := iface.Underlying().(*types.Interface)
			if !ok {
				continue
			}
			if implementsCheckHook != nil {
				implementsCheckHook()
			}
			if types.Implements(named, underlying) || types.Implements(types.NewPointer(named), underlying) {
				addTypedEdge(edges, typeID, ifaceID, graph.EdgeImplements)
			}
		}
	}
}

// discoverGoModuleDirs finds every directory containing a go.mod file under
// root, applying the same directory-skip rules as sourceFiles.
func discoverGoModuleDirs(ctx context.Context, root string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %s: %w", path, walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root {
				if _, skip := skippedDirectories[entry.Name()]; skip || strings.HasPrefix(entry.Name(), ".") {
					return filepath.SkipDir
				}
				if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Name() == "go.mod" {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover go module directories in %s: %w", root, err)
	}
	sort.Strings(dirs)
	return dirs, nil
}

// moduleDirFor returns the most specific module directory that contains
// fileDir, or empty when fileDir is not under any of them.
func moduleDirFor(moduleDirs []string, fileDir string) string {
	best := ""
	for _, dir := range moduleDirs {
		if dir == fileDir || strings.HasPrefix(fileDir, dir+string(filepath.Separator)) {
			if len(dir) > len(best) {
				best = dir
			}
		}
	}
	return best
}

// loadGoPackages type-checks every package in a Go module without ever
// mutating the module's own go.mod or go.sum.
func loadGoPackages(ctx context.Context, moduleDir string) ([]*packages.Package, error) {
	goFlags := "-mod=readonly"
	if _, err := os.Stat(filepath.Join(moduleDir, "vendor", "modules.txt")); err == nil {
		goFlags = "-mod=vendor"
	}
	env := filteredEnviron(os.Environ())
	env = append(env,
		"GOPROXY=off",
		"GOTOOLCHAIN=local",
		"CGO_ENABLED=0",
		"GOWORK=off",
		"GOFLAGS="+goFlags,
	)
	cfg := &packages.Config{
		Context: ctx,
		Dir:     moduleDir,
		Tests:   true,
		Env:     env,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedModule,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("load go packages in %s: %w", moduleDir, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("load go packages in %s: %w", moduleDir, err)
	}
	return pkgs, nil
}

// filteredEnviron strips variables that loadGoPackages sets explicitly so
// the process environment can never override the deterministic, offline
// configuration required for a reproducible index.
func filteredEnviron(base []string) []string {
	blocked := map[string]bool{
		"GOPROXY": true, "GOTOOLCHAIN": true, "CGO_ENABLED": true, "GOWORK": true, "GOFLAGS": true,
	}
	filtered := make([]string, 0, len(base))
	for _, entry := range base {
		key, _, found := strings.Cut(entry, "=")
		if found && blocked[key] {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// relUnderRoot converts an absolute path to a root-relative slash path, and
// reports false when the path is not under root.
func relUnderRoot(root, absPath string) (string, bool) {
	rel, err := filepath.Rel(root, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// extractTypedCalls walks every function and method body in pkg's syntax and
// records a CALLS edge for each callee. A callee go/types proved sound (even
// in a package with errors elsewhere) gets a typed edge; a callee with no
// usable type information falls back, call by call, to the same AST-only
// heuristic resolveCall applies to files the type checker never reached at
// all. It returns the set of repository-relative files that needed the
// heuristic for at least one call, so the caller can report them as only
// partially type-checked.
func extractTypedCalls(
	pkg *packages.Package,
	root string,
	funcPositionIndex map[string]string,
	g *graph.Graph,
	edges map[string]graph.Edge,
	nodesByQualified map[string]graph.Node,
	fileInfo map[string]parsedFile,
	functionsByPackage map[string]map[string]string,
	methodsByPackage map[string]map[string][]string,
	methodsByName map[string][]string,
) map[string]bool {
	heuristicFiles := make(map[string]bool)
	for _, astFile := range pkg.Syntax {
		relFile, ok := relUnderRoot(root, unadjustedPosition(pkg.Fset, astFile.Pos()).Filename)
		if !ok {
			continue
		}
		for _, decl := range astFile.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			sourceID := funcPositionIndex[positionKey(relFile, unadjustedPosition(pkg.Fset, fn.Name.Pos()).Offset)]
			if sourceID == "" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				targetID, external, resolved := resolveTypedCallee(pkg, call.Fun, root, funcPositionIndex)
				if !resolved {
					heuristicFiles[relFile] = true
					file, ok := fileInfo[relFile]
					if !ok {
						return true
					}
					heuristicTargetID, externalQualified, externalName := resolveCall(
						call.Fun, sourceID, file.packageQ, file.importPath,
						functionsByPackage, methodsByPackage, methodsByName,
					)
					if heuristicTargetID == "" && externalQualified != "" {
						heuristicTargetID = ensureExternal(g, nodesByQualified, externalQualified, externalName, "call target")
					}
					if heuristicTargetID != "" {
						addEdge(edges, sourceID, heuristicTargetID, graph.EdgeCalls)
					}
					return true
				}
				if targetID == "" && external != nil {
					targetID = ensureExternal(g, nodesByQualified, external.qualified, external.name, "call target")
				}
				if targetID != "" {
					addTypedEdge(edges, sourceID, targetID, graph.EdgeCalls)
				}
				return true
			})
		}
	}
	return heuristicFiles
}

// resolveTypedCallee resolves the callee of a call expression using type
// information, unwrapping parenthesization and generic instantiation syntax.
// The third return value is false when go/types recorded no usable
// information for this callee (an errored package left the identifier or
// selector unresolved, or resolved it to an invalid type), signalling that
// the caller must fall back to the AST-only heuristic for this call.
func resolveTypedCallee(pkg *packages.Package, funExpr ast.Expr, root string, funcPositionIndex map[string]string) (string, *externalRef, bool) {
	switch expr := funExpr.(type) {
	case *ast.ParenExpr:
		return resolveTypedCallee(pkg, expr.X, root, funcPositionIndex)
	case *ast.IndexExpr:
		return resolveTypedCallee(pkg, expr.X, root, funcPositionIndex)
	case *ast.IndexListExpr:
		return resolveTypedCallee(pkg, expr.X, root, funcPositionIndex)
	case *ast.Ident:
		return resolveTypedObject(pkg.TypesInfo.Uses[expr], root, pkg, funcPositionIndex)
	case *ast.SelectorExpr:
		if sel, ok := pkg.TypesInfo.Selections[expr]; ok {
			if selectionCrossesInvalidEmbedding(sel) {
				return "", nil, false
			}
			return resolveTypedObject(sel.Obj(), root, pkg, funcPositionIndex)
		}
		return resolveTypedObject(pkg.TypesInfo.Uses[expr.Sel], root, pkg, funcPositionIndex)
	default:
		// Not an identifier or selector (for example an immediately invoked
		// function literal): the AST-only heuristic has no rule for this
		// shape either, so there is nothing to gain by falling back to it.
		return "", nil, true
	}
}

// resolveTypedObject maps a resolved call-target object to either a node the
// indexer already created for the repository, or an external reference.
// Builtins and type conversions never produce an edge, and a call through a
// function-typed variable is reported by its own name rather than linked to
// an unrelated same-named symbol. The third return value is false only when
// obj is nil (go/types recorded no usable fact at all for this callee) or,
// for an object kind this function does not otherwise special-case, when its
// type is invalid; a *types.Builtin is reported by go/types with
// Typ[Invalid] by design (go/types/object.go: "Builtins don't have a valid
// type"), so isInvalidType must never be checked before dispatching on the
// object's concrete kind, or nearly every len/make/append/panic/new call in
// an otherwise clean package would be misreported as unresolved.
func resolveTypedObject(obj types.Object, root string, pkg *packages.Package, funcPositionIndex map[string]string) (string, *externalRef, bool) {
	if obj == nil {
		return "", nil, false
	}
	switch value := obj.(type) {
	case *types.Func:
		fn := value.Origin()
		pos := unadjustedPosition(pkg.Fset, fn.Pos())
		if rel, ok := relUnderRoot(root, pos.Filename); ok {
			if id := funcPositionIndex[positionKey(rel, pos.Offset)]; id != "" {
				return id, nil, true
			}
		}
		qualified, name := externalFuncName(fn)
		return "", &externalRef{qualified: qualified, name: name}, true
	case *types.Builtin, *types.TypeName, *types.Nil, *types.Label, *types.PkgName:
		return "", nil, true
	case *types.Var, *types.Const:
		// A func-value variable, package-level var, or struct field can share
		// a plain name with a real package-level function or method (local
		// shadowing, or a func-typed field named like a sibling function).
		// The "var:" namespace can never collide with a declaration's
		// qualified name, so ensureExternal always creates or reuses a
		// distinct External node instead of accidentally returning the real
		// symbol's node. This resolution is unconditional, regardless of
		// whether obj.Type() is valid: an ill-typed initializer (for example
		// `h := broken()` where broken is undefined) otherwise left the
		// invalid-type check to reject the object and fall back to the
		// AST-only heuristic, which can then wrongly match the local
		// variable's name against an unrelated package-level function or
		// method of the same name.
		qualified := "var:local." + obj.Name()
		if obj.Pkg() != nil {
			qualified = "var:" + obj.Pkg().Path() + "." + obj.Name()
		}
		return "", &externalRef{qualified: qualified, name: obj.Name()}, true
	default:
		if isInvalidType(obj.Type()) {
			return "", nil, false
		}
		return "", nil, true
	}
}

// isInvalidType reports whether t is nil or go/types' sentinel invalid type,
// which it assigns to an object it could not soundly resolve while
// type-checking a package with errors.
func isInvalidType(t types.Type) bool {
	if t == nil {
		return true
	}
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind() == types.Invalid
}

// selectionCrossesInvalidEmbedding reports whether sel's receiver, or any
// struct nested along sel's embedding path, declares an embedded field
// whose type is invalid. go/types silently treats an embedded field with an
// invalid type as contributing no methods or fields while resolving a
// selection, instead of reporting the ambiguity a sound program would have;
// it can therefore select a deeper, differently-named field or method than
// the real (uncorrupted) program would resolve to. Treating such a
// selection as unresolved lets the caller fall back to the AST-only
// heuristic instead of recording an unsound typed edge.
func selectionCrossesInvalidEmbedding(sel *types.Selection) bool {
	recv := sel.Recv()
	index := sel.Index()
	for depth, fieldIndex := range index {
		structType, ok := underlyingStruct(recv)
		if !ok {
			return false
		}
		if structHasInvalidEmbeddedField(structType) {
			return true
		}
		if depth == len(index)-1 {
			return false
		}
		if fieldIndex < 0 || fieldIndex >= structType.NumFields() {
			return false
		}
		recv = structType.Field(fieldIndex).Type()
	}
	return false
}

// underlyingStruct unwraps a single pointer indirection from t and reports
// the struct type beneath it, when there is one.
func underlyingStruct(t types.Type) (*types.Struct, bool) {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	structType, ok := t.Underlying().(*types.Struct)
	return structType, ok
}

// structHasInvalidEmbeddedField reports whether s declares an embedded
// field whose type is, or reduces to, go/types' invalid sentinel.
func structHasInvalidEmbeddedField(s *types.Struct) bool {
	for i := 0; i < s.NumFields(); i++ {
		field := s.Field(i)
		if !field.Embedded() {
			continue
		}
		if typeContainsInvalid(field.Type(), make(map[types.Type]bool)) {
			return true
		}
	}
	return false
}

// typeContainsInvalid recursively reports whether t contains go/types'
// invalid sentinel type, unwrapping pointers, slices, arrays, maps,
// channels, function signatures, and named types. visited guards against
// infinite recursion through a type's own recursive definition.
func typeContainsInvalid(t types.Type, visited map[types.Type]bool) bool {
	if t == nil {
		return true
	}
	if visited[t] {
		return false
	}
	visited[t] = true
	if isInvalidType(t) {
		return true
	}
	switch value := t.(type) {
	case *types.Pointer:
		return typeContainsInvalid(value.Elem(), visited)
	case *types.Slice:
		return typeContainsInvalid(value.Elem(), visited)
	case *types.Array:
		return typeContainsInvalid(value.Elem(), visited)
	case *types.Map:
		return typeContainsInvalid(value.Key(), visited) || typeContainsInvalid(value.Elem(), visited)
	case *types.Chan:
		return typeContainsInvalid(value.Elem(), visited)
	case *types.Signature:
		return signatureHasInvalidType(value)
	case *types.Named:
		return typeContainsInvalid(value.Underlying(), visited)
	default:
		return false
	}
}

// signatureHasInvalidType reports whether any parameter or result type of
// sig contains go/types' invalid sentinel, even nested inside a pointer,
// slice, array, map, channel, or function type. In an ill-typed package
// go/types treats every Invalid type as identical to every other Invalid
// type, so two methods whose signatures both reduce to Invalid (for example
// through a selector into an unresolvable missing import) would otherwise
// compare equal and can report an IMPLEMENTS edge that is not a sound fact
// about the program.
func signatureHasInvalidType(sig *types.Signature) bool {
	visited := make(map[types.Type]bool)
	for i := 0; i < sig.Params().Len(); i++ {
		if typeContainsInvalid(sig.Params().At(i).Type(), visited) {
			return true
		}
	}
	for i := 0; i < sig.Results().Len(); i++ {
		if typeContainsInvalid(sig.Results().At(i).Type(), visited) {
			return true
		}
	}
	return false
}

// namedHasInvalidMethodSignature reports whether any method in named's
// method set (using a pointer method set so both value- and
// pointer-receiver methods are included) has a parameter or result type
// that contains go/types' invalid sentinel.
func namedHasInvalidMethodSignature(named *types.Named) bool {
	methodSet := types.NewMethodSet(types.NewPointer(named))
	for i := 0; i < methodSet.Len(); i++ {
		fn, ok := methodSet.At(i).Obj().(*types.Func)
		if !ok {
			continue
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok {
			continue
		}
		if signatureHasInvalidType(sig) {
			return true
		}
	}
	return false
}

// interfaceHasInvalidMethodSignature reports whether any method declared by
// iface has a parameter or result type that contains go/types' invalid
// sentinel.
func interfaceHasInvalidMethodSignature(iface *types.Interface) bool {
	for i := 0; i < iface.NumMethods(); i++ {
		sig, ok := iface.Method(i).Type().(*types.Signature)
		if !ok {
			continue
		}
		if signatureHasInvalidType(sig) {
			return true
		}
	}
	return false
}

// firstPackageErrorReason returns a short, deterministic description of why
// pkg could not be fully type-checked.
func firstPackageErrorReason(pkg *packages.Package) string {
	if len(pkg.Errors) > 0 {
		return pkg.Errors[0].Error()
	}
	return "package failed to type-check"
}

// unadjustedPosition returns the position of pos ignoring any `//line`
// directive remapping, so the filename and offset always identify a
// location in the real on-disk file the indexer's own AST parse also sees.
func unadjustedPosition(fset *token.FileSet, pos token.Pos) token.Position {
	return fset.PositionFor(pos, false)
}

// externalFuncName builds the qualified name for a callee outside the
// repository: pkgpath.Name for a plain function, pkgpath.Type.Method for a
// method.
func externalFuncName(fn *types.Func) (qualified, name string) {
	pkgPath := ""
	if fn.Pkg() != nil {
		pkgPath = fn.Pkg().Path()
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		if recvName := recvTypeName(sig.Recv().Type()); recvName != "" {
			if pkgPath == "" {
				return recvName + "." + fn.Name(), fn.Name()
			}
			return pkgPath + "." + recvName + "." + fn.Name(), fn.Name()
		}
	}
	if pkgPath == "" {
		return fn.Name(), fn.Name()
	}
	return pkgPath + "." + fn.Name(), fn.Name()
}

func recvTypeName(recv types.Type) string {
	for {
		ptr, ok := recv.(*types.Pointer)
		if !ok {
			break
		}
		recv = ptr.Elem()
	}
	if named, ok := recv.(*types.Named); ok {
		return named.Obj().Name()
	}
	return ""
}

// collectNamedDeclarations records every named type and non-empty named
// interface declared in pkg that maps to a Type node the indexer created,
// for later IMPLEMENTS resolution.
func collectNamedDeclarations(
	pkg *packages.Package,
	root string,
	typePositionIndex map[string]string,
	namedTypes map[string]*types.Named,
	namedInterfaces map[string]*types.Named,
) {
	collectNamedDeclarationsWithLocal(pkg, root, typePositionIndex, namedTypes, namedInterfaces, nil, nil)
}

// collectNamedDeclarationsWithLocal behaves like collectNamedDeclarations,
// additionally recording, in localTypes/localInterfaces (when non-nil), the
// subset of declarations found in a _test.go file. A test-variant package
// (for example "example.com/p [example.com/p.test]") recompiles every
// production declaration of p alongside its own test-only ones into a
// single, near-full-size package scope; localTypes/localInterfaces let the
// caller limit the (type, interface) pairs it re-checks for that variant to
// only the pairs a test file could actually introduce, instead of repeating
// a full sweep over the variant's entire copy of the package.
func collectNamedDeclarationsWithLocal(
	pkg *packages.Package,
	root string,
	typePositionIndex map[string]string,
	namedTypes map[string]*types.Named,
	namedInterfaces map[string]*types.Named,
	localTypes map[string]*types.Named,
	localInterfaces map[string]*types.Named,
) {
	if pkg.Types == nil {
		return
	}
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		typeName, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || typeName.IsAlias() || typeName.Pkg() != pkg.Types {
			continue
		}
		named, ok := typeName.Type().(*types.Named)
		if !ok {
			continue
		}
		if isInvalidType(named.Underlying()) {
			// A type declaration inside an errored package can end up with
			// go/types' invalid sentinel as its underlying type; reporting
			// IMPLEMENTS for it could never be a sound fact.
			continue
		}
		pos := unadjustedPosition(pkg.Fset, typeName.Pos())
		rel, ok := relUnderRoot(root, pos.Filename)
		if !ok {
			continue
		}
		nodeID := typePositionIndex[positionKey(rel, pos.Offset)]
		if nodeID == "" {
			continue
		}
		isTestFile := strings.HasSuffix(rel, "_test.go")
		if iface, ok := named.Underlying().(*types.Interface); ok {
			if iface.NumMethods() > 0 {
				if interfaceHasInvalidMethodSignature(iface) {
					// In an ill-typed package a method whose parameter or
					// result reduces to Invalid (for example through an
					// unresolvable missing import) compares equal to any
					// other Invalid-typed method, so keeping this interface
					// could report a typed IMPLEMENTS edge for a coincidence
					// in error recovery rather than a sound fact.
					continue
				}
				namedInterfaces[nodeID] = named
				if isTestFile && localInterfaces != nil {
					localInterfaces[nodeID] = named
				}
			}
			continue
		}
		if namedHasInvalidMethodSignature(named) {
			continue
		}
		namedTypes[nodeID] = named
		if isTestFile && localTypes != nil {
			localTypes[nodeID] = named
		}
	}
}

// dedupeSkippedFiles keeps the first reported reason for each file.
func dedupeSkippedFiles(files []graph.SkippedFile) []graph.SkippedFile {
	seen := make(map[string]bool, len(files))
	result := make([]graph.SkippedFile, 0, len(files))
	for _, file := range files {
		if seen[file.File] {
			continue
		}
		seen[file.File] = true
		result = append(result, file)
	}
	return result
}
