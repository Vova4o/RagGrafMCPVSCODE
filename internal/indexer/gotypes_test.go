package indexer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

func mustIndexGo(t *testing.T, root string) *graph.Graph {
	t.Helper()
	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	return value
}

func nodeQualifiedNames(g *graph.Graph) map[string]string {
	names := make(map[string]string, len(g.Nodes))
	for _, node := range g.Nodes {
		names[node.ID] = node.QualifiedName
	}
	return names
}

func hasTypedCallEdge(t *testing.T, g *graph.Graph, fromQualified, toQualified string) bool {
	t.Helper()
	names := nodeQualifiedNames(g)
	for _, edge := range g.Edges {
		if edge.Kind != graph.EdgeCalls {
			continue
		}
		if names[edge.From] == fromQualified && names[edge.To] == toQualified {
			if edge.Resolution != graph.ResolutionTyped {
				t.Fatalf("edge %s->%s found but resolution = %q, want typed", fromQualified, toQualified, edge.Resolution)
			}
			return true
		}
	}
	return false
}

func hasCallEdge(g *graph.Graph, fromQualified, toQualified string) bool {
	names := nodeQualifiedNames(g)
	for _, edge := range g.Edges {
		if edge.Kind != graph.EdgeCalls {
			continue
		}
		if names[edge.From] == fromQualified && names[edge.To] == toQualified {
			return true
		}
	}
	return false
}

func hasImplementsEdge(g *graph.Graph, fromQualified, toQualified string) bool {
	names := nodeQualifiedNames(g)
	for _, edge := range g.Edges {
		if edge.Kind != graph.EdgeImplements {
			continue
		}
		if names[edge.From] == fromQualified && names[edge.To] == toQualified {
			return edge.Resolution == graph.ResolutionTyped
		}
	}
	return false
}

func isDegraded(g *graph.Graph, rel string) bool {
	for _, file := range g.Coverage.DegradedFiles {
		if file.File == rel {
			return true
		}
	}
	return false
}

// degradedReason returns the reported reason for rel's degradation, or ""
// when rel is not in Coverage.DegradedFiles.
func degradedReason(g *graph.Graph, rel string) string {
	for _, file := range g.Coverage.DegradedFiles {
		if file.File == rel {
			return file.Reason
		}
	}
	return ""
}

func TestGoTypesFieldCallResolvesConcreteMethod(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/field\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package field

type Dep struct{}

func (Dep) Index() {}

type Service struct {
	dep *Dep
}

func (s *Service) Index() {
	s.dep.Index()
}
`)
	value := mustIndexGo(t, root)
	if !hasTypedCallEdge(t, value, "example.com/field.Service.Index", "example.com/field.Dep.Index") {
		t.Fatalf("missing typed edge Service.Index -> Dep.Index; edges=%#v", value.Edges)
	}
	if hasCallEdge(value, "example.com/field.Service.Index", "example.com/field.Service.Index") {
		t.Fatalf("unexpected self-edge on Service.Index")
	}
}

func TestGoTypesDisambiguatesSameNamedMethod(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/disamb\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package disamb

type A struct{}

func (A) Load() {}

type B struct{}

func (B) Load() {}

func UseA(a A) {
	a.Load()
}
`)
	value := mustIndexGo(t, root)
	if !hasTypedCallEdge(t, value, "example.com/disamb.UseA", "example.com/disamb.A.Load") {
		t.Fatalf("missing typed edge UseA -> A.Load; edges=%#v", value.Edges)
	}
	if hasCallEdge(value, "example.com/disamb.UseA", "example.com/disamb.B.Load") {
		t.Fatalf("unexpected edge UseA -> B.Load")
	}
}

func TestGoTypesInterfaceCallAndImplements(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/iface\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package iface

type Store interface {
	Save()
}

type ValueImpl struct{}

func (ValueImpl) Save() {}

type PointerImpl struct{}

func (*PointerImpl) Save() {}

func Use(st Store) {
	st.Save()
}
`)
	value := mustIndexGo(t, root)
	if !hasTypedCallEdge(t, value, "example.com/iface.Use", "example.com/iface.Store.Save") {
		t.Fatalf("missing typed edge Use -> Store.Save; edges=%#v", value.Edges)
	}
	if !hasImplementsEdge(value, "example.com/iface.ValueImpl", "example.com/iface.Store") {
		t.Fatalf("missing IMPLEMENTS edge ValueImpl -> Store; edges=%#v", value.Edges)
	}
	if !hasImplementsEdge(value, "example.com/iface.PointerImpl", "example.com/iface.Store") {
		t.Fatalf("missing IMPLEMENTS edge PointerImpl -> Store; edges=%#v", value.Edges)
	}
}

func TestGoTypesStdlibCallIsExternal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/stdlib\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package stdlib

import "fmt"

func Run() {
	fmt.Println("hi")
}
`)
	value := mustIndexGo(t, root)
	if !hasTypedCallEdge(t, value, "example.com/stdlib.Run", "fmt.Println") {
		t.Fatalf("missing typed edge Run -> fmt.Println; edges=%#v", value.Edges)
	}
	found := false
	for _, node := range value.Nodes {
		if node.Kind == graph.KindExternal && node.QualifiedName == "fmt.Println" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing External node fmt.Println")
	}
}

func TestGoTypesFuncValueDoesNotLinkToUnrelatedSymbol(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/funcvalue\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package funcvalue

func helper() {}

func Run() {
	f := helper
	f()
}
`)
	value := mustIndexGo(t, root)
	if hasCallEdge(value, "example.com/funcvalue.Run", "example.com/funcvalue.helper") {
		t.Fatal("unexpected edge Run -> helper through a func value")
	}
}

func TestGoTypesRecursionKeepsSelfEdge(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/recur\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package recur

func fact(n int) int {
	if n <= 1 {
		return 1
	}
	return n * fact(n-1)
}
`)
	value := mustIndexGo(t, root)
	if !hasTypedCallEdge(t, value, "example.com/recur.fact", "example.com/recur.fact") {
		t.Fatalf("missing typed self-edge for fact; edges=%#v", value.Edges)
	}
}

func TestGoTypesGenericInstantiationResolvesOrigin(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/generic\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package generic

func Map[T any](values []T, f func(T) T) []T {
	out := make([]T, len(values))
	for i, v := range values {
		out[i] = f(v)
	}
	return out
}

func Run() []int {
	return Map[int]([]int{1, 2, 3}, func(v int) int { return v })
}
`)
	value := mustIndexGo(t, root)
	if !hasTypedCallEdge(t, value, "example.com/generic.Run", "example.com/generic.Map") {
		t.Fatalf("missing typed edge Run -> Map; edges=%#v", value.Edges)
	}
}

func TestGoTypesTypeErrorFileDegradesButKeepsHeuristicEdges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/typeerr\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package typeerr

func helper() {}

func Run() {
	helper()
	undefinedSymbol()
}
`)
	value := mustIndexGo(t, root)
	if !isDegraded(value, "main.go") {
		t.Fatalf("expected main.go to be degraded; coverage=%#v", value.Coverage)
	}
	if !hasCallEdge(value, "example.com/typeerr.Run", "example.com/typeerr.helper") {
		t.Fatalf("missing heuristic edge Run -> helper despite type error; edges=%#v", value.Edges)
	}
}

func TestGoTypesBuildConstrainedFileIsDegradedButIndexed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/buildtag\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), "package buildtag\n\nfunc Run() {}\n")
	writeTestFile(t, filepath.Join(root, "ignored.go"), "//go:build ignore\n\npackage buildtag\n\nfunc Ignored() {}\n")

	value := mustIndexGo(t, root)
	if !isDegraded(value, "ignored.go") {
		t.Fatalf("expected ignored.go to be degraded; coverage=%#v", value.Coverage)
	}
	found := false
	for _, node := range value.Nodes {
		if node.Name == "Ignored" && node.File == "ignored.go" {
			found = true
		}
	}
	if !found {
		t.Fatal("build-constrained file was not indexed")
	}
}

func TestGoTypesTestFileCallsProductionFuncWithoutDuplicates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/testcall\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package testcall

func Production() int { return 42 }
`)
	writeTestFile(t, filepath.Join(root, "main_test.go"), `package testcall

import "testing"

func TestProduction(t *testing.T) {
	if Production() != 42 {
		t.Fatal("bad")
	}
}
`)
	value := mustIndexGo(t, root)
	names := nodeQualifiedNames(value)
	count := 0
	for _, edge := range value.Edges {
		if edge.Kind != graph.EdgeCalls {
			continue
		}
		if names[edge.From] == "example.com/testcall.TestProduction" && names[edge.To] == "example.com/testcall.Production" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("edge count TestProduction -> Production = %d, want 1", count)
	}
	productionNodes := 0
	for _, node := range value.Nodes {
		if node.QualifiedName == "example.com/testcall.Production" {
			productionNodes++
		}
	}
	if productionNodes != 1 {
		t.Fatalf("Production node count = %d, want 1 (no duplicates)", productionNodes)
	}
}

// TestGoTypesLocalVarShadowingPackageFuncDoesNotLinkToRealFunc reproduces the
// qualified-name collision described in review defect #1: a local variable
// named exactly like a package-level function must never resolve a typed
// call to that unrelated function.
func TestGoTypesLocalVarShadowingPackageFuncDoesNotLinkToRealFunc(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/shadow\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package shadow

func helper() {}

func other() {}

func Run() {
	helper := other
	helper()
}
`)
	value := mustIndexGo(t, root)
	if hasCallEdge(value, "example.com/shadow.Run", "example.com/shadow.helper") {
		t.Fatal("unexpected edge Run -> helper: local var shadowing a package func must not link to it")
	}
}

// TestGoTypesFuncTypedFieldNamedLikePackageFuncDoesNotLinkToRealFunc covers
// the second collision scenario from defect #1: a func-typed struct field
// sharing a name with a package-level function.
func TestGoTypesFuncTypedFieldNamedLikePackageFuncDoesNotLinkToRealFunc(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/fieldshadow\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package fieldshadow

func helper() {}

type Holder struct {
	helper func()
}

func Run(h Holder) {
	h.helper()
}
`)
	value := mustIndexGo(t, root)
	if hasCallEdge(value, "example.com/fieldshadow.Run", "example.com/fieldshadow.helper") {
		t.Fatal("unexpected edge Run -> helper: func-typed field must not link to an unrelated package func")
	}
}

// TestGoTypesOneLineInterfaceMethodsResolveDistinctEdges reproduces defect
// #2: two declarations on the same line must not collide in
// funcPositionIndex, which was previously keyed by line number alone.
func TestGoTypesOneLineInterfaceMethodsResolveDistinctEdges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/oneline\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package oneline

type Store interface{ Get(); Put() }

type Impl struct{}

func (Impl) Get() {}
func (Impl) Put() {}

func UseGet(s Store) { s.Get() }
func UsePut(s Store) { s.Put() }
`)
	value := mustIndexGo(t, root)
	if !hasTypedCallEdge(t, value, "example.com/oneline.UseGet", "example.com/oneline.Store.Get") {
		t.Fatalf("missing typed edge UseGet -> Store.Get; edges=%#v", value.Edges)
	}
	if !hasTypedCallEdge(t, value, "example.com/oneline.UsePut", "example.com/oneline.Store.Put") {
		t.Fatalf("missing typed edge UsePut -> Store.Put; edges=%#v", value.Edges)
	}
	if hasCallEdge(value, "example.com/oneline.UseGet", "example.com/oneline.Store.Put") {
		t.Fatal("unexpected edge UseGet -> Store.Put: one-line interface methods collided by line number")
	}
	if hasCallEdge(value, "example.com/oneline.UsePut", "example.com/oneline.Store.Get") {
		t.Fatal("unexpected edge UsePut -> Store.Get: one-line interface methods collided by line number")
	}
}

// TestGoTypesTwoFuncsOnOneLineResolveDistinctEdges covers the same
// line-collision defect for two function declarations sharing one line.
func TestGoTypesTwoFuncsOnOneLineResolveDistinctEdges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/oneliner\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), "package oneliner\n\nfunc a() {}; func b() { a() }\n")

	value := mustIndexGo(t, root)
	if !hasTypedCallEdge(t, value, "example.com/oneliner.b", "example.com/oneliner.a") {
		t.Fatalf("missing typed edge b -> a; edges=%#v", value.Edges)
	}
}

// TestGoTypesLineDirectiveFileStillYieldsTypedEdges reproduces the second
// half of defect #2: pkg.Fset.Position honours `//line` directives and
// reports a filename that is not under the repository root, which must not
// prevent a typed CALLS edge from being recorded for the real file.
func TestGoTypesLineDirectiveFileStillYieldsTypedEdges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/linedirective\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package linedirective

//line virtual.go:1
func helper() {}

func Run() {
	helper()
}
`)
	value := mustIndexGo(t, root)
	if !hasTypedCallEdge(t, value, "example.com/linedirective.Run", "example.com/linedirective.helper") {
		t.Fatalf("missing typed edge Run -> helper despite //line directive; edges=%#v", value.Edges)
	}
	if isDegraded(value, "main.go") {
		t.Fatalf("main.go should not be degraded; coverage=%#v", value.Coverage)
	}
}

// TestIndexGoTypesBudgetExceededDegradesRemainingModules reproduces defect
// #3: the type-check time budget is shared across the whole Index call, so
// once it is exhausted the remaining module's files degrade to the AST
// heuristic instead of blocking on another full per-module timeout.
//
// This test mutates the package-level goTypeCheckBudget test hook and must
// not run in parallel with other tests, so the mutation is not observed by
// tests that call t.Parallel() and run concurrently.
func TestIndexGoTypesBudgetExceededDegradesRemainingModules(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/budget\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), "package budget\n\nfunc Run() {}\n")

	original := goTypeCheckBudget
	goTypeCheckBudget = -1 * time.Second
	defer func() { goTypeCheckBudget = original }()

	value := mustIndexGo(t, root)
	found := false
	for _, file := range value.Coverage.DegradedFiles {
		if file.File == "main.go" && file.Reason == "type check budget exceeded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected main.go degraded with 'type check budget exceeded'; coverage=%#v", value.Coverage)
	}
}

// TestIndexReturnsErrorWhenContextAlreadyCancelled asserts that a cancelled
// parent context surfaces as an error from Index rather than degrading
// silently, per defect #3's requirement that cancellation stays fatal.
func TestIndexReturnsErrorWhenContextAlreadyCancelled(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/cancelled\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), "package cancelled\n\nfunc Run() {}\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Index(ctx, root); err == nil {
		t.Fatal("Index() with an already-cancelled context returned nil error, want an error")
	}
}

// TestGoTypesProductionFileNotDegradedByFailingInternalTestVariant
// reproduces defect #4: a production file that type-checks cleanly under
// its own package must not be reported degraded merely because the
// `[p.test]` variant (which recompiles it together with a broken internal
// test file) also failed to type-check.
func TestGoTypesProductionFileNotDegradedByFailingInternalTestVariant(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/mixedvariant\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package mixedvariant

func Production() int { return 42 }
`)
	writeTestFile(t, filepath.Join(root, "main_test.go"), `package mixedvariant

func TestBroken() {
	undefinedSymbol()
}
`)
	value := mustIndexGo(t, root)
	if isDegraded(value, "main.go") {
		t.Fatalf("main.go must not be degraded: it type-checked cleanly under the production package; coverage=%#v", value.Coverage)
	}
	if !isDegraded(value, "main_test.go") {
		t.Fatalf("main_test.go should be degraded: it fails to type-check; coverage=%#v", value.Coverage)
	}
}

// TestGoTypesImplementsAcrossPackagesWithInternalTestVariant reproduces
// defect #5: an internal test file in the interface's own package must not
// break a cross-package IMPLEMENTS edge, because the `[a.test]` variant
// recompiles package a's declarations into a distinct, non-comparable
// type-checking universe.
func TestGoTypesImplementsAcrossPackagesWithInternalTestVariant(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/crosspkg\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "a", "a.go"), `package a

type Iface interface {
	Save()
}
`)
	writeTestFile(t, filepath.Join(root, "a", "a_internal_test.go"), `package a

func TestInternal() {}
`)
	writeTestFile(t, filepath.Join(root, "b", "b.go"), `package b

import "example.com/crosspkg/a"

type Impl struct{}

func (Impl) Save() {}

var _ a.Iface = Impl{}
`)
	value := mustIndexGo(t, root)
	if !hasImplementsEdge(value, "example.com/crosspkg/b.Impl", "example.com/crosspkg/a.Iface") {
		t.Fatalf("missing IMPLEMENTS edge Impl -> Iface with an internal test variant present; edges=%#v", value.Edges)
	}
}

// TestGoTypesMissingDependencyKeepsLocalCallsTypedAndDegradesOnlyThatFile
// reproduces the grpc-go-scale defect: a package that imports a module path
// that cannot be resolved offline must not lose type information for the
// rest of the package. Calls fully local to the package (a plain function
// call and the s.dep.Index() same-named-field pattern) must still get typed
// edges; only the call reaching into the missing dependency should fall back
// to the AST heuristic and become an External reference, and only then
// should the file be reported as degraded, with a reason describing partial
// type information rather than a full type-check failure.
func TestGoTypesMissingDependencyKeepsLocalCallsTypedAndDegradesOnlyThatFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/typederr\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package typederr

import "example.com/missing/dep"

func helper() {}

type Dep struct{}

func (Dep) Index() {}

type Service struct {
	dep *Dep
}

func (s *Service) Run() {
	helper()
	s.dep.Index()
	dep.DoSomething()
}
`)
	value := mustIndexGo(t, root)

	if !hasTypedCallEdge(t, value, "example.com/typederr.Service.Run", "example.com/typederr.helper") {
		t.Fatalf("missing typed edge Run -> helper despite the package's missing dependency; edges=%#v", value.Edges)
	}
	if !hasTypedCallEdge(t, value, "example.com/typederr.Service.Run", "example.com/typederr.Dep.Index") {
		t.Fatalf("missing typed edge Run -> Dep.Index despite the package's missing dependency; edges=%#v", value.Edges)
	}
	if !hasCallEdge(value, "example.com/typederr.Service.Run", "example.com/missing/dep.DoSomething") {
		t.Fatalf("missing heuristic edge Run -> example.com/missing/dep.DoSomething; edges=%#v", value.Edges)
	}
	for _, edge := range value.Edges {
		names := nodeQualifiedNames(value)
		if names[edge.From] == "example.com/typederr.Service.Run" && names[edge.To] == "example.com/missing/dep.DoSomething" {
			if edge.Resolution == graph.ResolutionTyped {
				t.Fatalf("edge Run -> example.com/missing/dep.DoSomething must not be typed: the dependency cannot be resolved")
			}
		}
	}
	found := false
	for _, node := range value.Nodes {
		if node.Kind == graph.KindExternal && node.QualifiedName == "example.com/missing/dep.DoSomething" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing External node example.com/missing/dep.DoSomething")
	}
	if !isDegraded(value, "main.go") {
		t.Fatalf("expected main.go to be degraded; coverage=%#v", value.Coverage)
	}
	if reason := degradedReason(value, "main.go"); !strings.HasPrefix(reason, "partial type information:") {
		t.Fatalf("main.go degraded reason = %q, want prefix %q", reason, "partial type information:")
	}
}

// TestGoTypesUnrelatedFileErrorDoesNotDegradeCleanSiblingFile reproduces the
// second half of the grpc-go-scale defect: a type error confined to one file
// in a package must not throw away type information for every other file in
// that package. A sibling file whose calls all resolve soundly must stay
// undegraded with typed edges, while the broken file is degraded only
// because of its own unresolved call, keeping its resolvable call typed.
func TestGoTypesUnrelatedFileErrorDoesNotDegradeCleanSiblingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/mixederr\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package mixederr

func Production() int { return 42 }

func UseProduction() int {
	return Production()
}
`)
	writeTestFile(t, filepath.Join(root, "other.go"), `package mixederr

func helper() {}

func Broken() {
	helper()
	undefinedSymbol()
}
`)
	value := mustIndexGo(t, root)

	if isDegraded(value, "main.go") {
		t.Fatalf("main.go must not be degraded by an unrelated error in other.go; coverage=%#v", value.Coverage)
	}
	if !hasTypedCallEdge(t, value, "example.com/mixederr.UseProduction", "example.com/mixederr.Production") {
		t.Fatalf("missing typed edge UseProduction -> Production; edges=%#v", value.Edges)
	}
	if !isDegraded(value, "other.go") {
		t.Fatalf("expected other.go to be degraded; coverage=%#v", value.Coverage)
	}
	if reason := degradedReason(value, "other.go"); !strings.HasPrefix(reason, "partial type information:") {
		t.Fatalf("other.go degraded reason = %q, want prefix %q", reason, "partial type information:")
	}
	if !hasTypedCallEdge(t, value, "example.com/mixederr.Broken", "example.com/mixederr.helper") {
		t.Fatalf("missing typed edge Broken -> helper despite the unrelated undefinedSymbol error; edges=%#v", value.Edges)
	}
}

// TestGoTypesCleanPackageWithBuiltinsHasNoDegradedFiles reproduces defect A:
// go/types reports *types.Builtin objects (len, make, append, panic, new,
// ...) with Typ[Invalid] by design, so checking isInvalidType before
// dispatching on the object's kind misreported nearly every file of a clean
// package as only partially type-checked.
func TestGoTypesCleanPackageWithBuiltinsHasNoDegradedFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/builtins\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package builtins

func Run(values []int) []int {
	out := make([]int, len(values))
	copy(out, values)
	out = append(out, values...)
	if len(out) == 0 {
		panic("empty")
	}
	extra := new(int)
	*extra = cap(out)
	return out
}
`)
	value := mustIndexGo(t, root)
	if len(value.Coverage.DegradedFiles) != 0 {
		t.Fatalf("expected zero degraded files for a clean package using builtins, got %#v", value.Coverage.DegradedFiles)
	}
}

// TestGoTypesInvalidTypedVarDoesNotLinkToPackageFuncOfSameName reproduces
// review defect LOW #3: a local variable initialized from an undefined
// call has an invalid type, but that must not make resolveTypedObject fall
// back to the AST-only heuristic, which could then wrongly match the
// variable's name against an unrelated package-level function of the same
// name.
func TestGoTypesInvalidTypedVarDoesNotLinkToPackageFuncOfSameName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/invalidvar\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package invalidvar

func h() {}

func Run() {
	h := broken()
	h()
}
`)
	value := mustIndexGo(t, root)
	if hasCallEdge(value, "example.com/invalidvar.Run", "example.com/invalidvar.h") {
		t.Fatal("unexpected edge Run -> h: an invalid-typed local var must not link to an unrelated package func of the same name")
	}
}

// TestGoTypesInvalidSignatureDoesNotProduceFalseImplementsEdge reproduces
// review defect MEDIUM #1: in an ill-typed package go/types treats every
// Invalid type as identical to every other Invalid type, so an interface
// method and a concrete method that both reference an unresolvable missing
// import can compare equal even though their real parameter types differ.
func TestGoTypesInvalidSignatureDoesNotProduceFalseImplementsEdge(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/invalidsig\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package invalidsig

import "example.com/missing/dep"

type I interface {
	Handle(dep.Req)
}

type T struct{}

func (T) Handle(dep.Resp) {}
`)
	value := mustIndexGo(t, root)
	if hasImplementsEdge(value, "example.com/invalidsig.T", "example.com/invalidsig.I") {
		t.Fatal("unexpected IMPLEMENTS edge T -> I: both method signatures only compare equal because they both reduce to Invalid via a missing import")
	}
}

// TestGoTypesEmbeddedInvalidFieldSelectionFallsBackToHeuristic reproduces
// review defect LOW #4: go/types silently skips an embedded field with an
// invalid type while resolving a selection instead of reporting the
// ambiguity a sound program would have, so it can select a deeper,
// differently-named method than the real program would resolve to. The
// selection must fall back to the AST-only heuristic instead of producing a
// typed edge.
func TestGoTypesEmbeddedInvalidFieldSelectionFallsBackToHeuristic(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/embedinvalid\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package embedinvalid

import "example.com/missing/dep"

type Deep struct{}

func (Deep) Foo() {}

type Mid struct {
	Deep
}

type T struct {
	dep.Bad
	Mid
}

func Run(t T) {
	t.Foo()
}
`)
	value := mustIndexGo(t, root)
	names := nodeQualifiedNames(value)
	for _, edge := range value.Edges {
		if edge.Kind != graph.EdgeCalls {
			continue
		}
		if names[edge.From] != "example.com/embedinvalid.Run" {
			continue
		}
		if names[edge.To] == "example.com/embedinvalid.Deep.Foo" && edge.Resolution == graph.ResolutionTyped {
			t.Fatal("edge Run -> Deep.Foo must not be typed: T embeds an invalid field, so the selection is not soundly resolved")
		}
	}
}

// TestGoTypesTestFileOnlyTypeImplementsProductionInterface covers the
// variant-local case emitVariantImplementsEdges must still handle: a type
// declared only in a _test.go file, which the production universe never
// saw, must still produce an IMPLEMENTS edge to a production interface.
func TestGoTypesTestFileOnlyTypeImplementsProductionInterface(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/variantimpl\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package variantimpl

type Store interface {
	Save()
}
`)
	writeTestFile(t, filepath.Join(root, "main_test.go"), `package variantimpl

type fakeStore struct{}

func (fakeStore) Save() {}
`)
	value := mustIndexGo(t, root)
	if !hasImplementsEdge(value, "example.com/variantimpl.fakeStore", "example.com/variantimpl.Store") {
		t.Fatalf("missing IMPLEMENTS edge fakeStore -> Store for a type declared only in a _test.go file; edges=%#v", value.Edges)
	}
}

// TestGoTypesCleanPackageUnusualCallShapesNeverDegrade enumerates call
// shapes that resolveTypedCallee has no direct case for (an immediately
// invoked function literal, a call on a call result, a method expression, a
// method value, a generic instantiation, a conversion, a builtin call, a
// call through an interface variable, and a call through a promoted
// embedded method) inside one otherwise clean, type-checked package. None of
// them must ever reach indexGoTypes' "unresolved call in type-checked
// package" degradation: a clean package (no pkg.Errors, not IllTyped) has no
// legitimate reason for any of its calls to need the AST-only heuristic.
func TestGoTypesCleanPackageUnusualCallShapesNeverDegrade(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/shapes\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package shapes

type T struct{}

func (T) M() {}

type MyInt int

func Map[V any](values []V, f func(V) V) []V {
	out := make([]V, len(values))
	for i, v := range values {
		out[i] = f(v)
	}
	return out
}

func twice() func() {
	return func() {}
}

type Base struct{}

func (Base) Hello() {}

type Derived struct {
	Base
}

type Greeter interface {
	Hello()
}

func Run() {
	func() {}()

	twice()()

	t := T{}
	me := (*T).M
	me(&t)

	m := t.M
	m()

	xs := Map[int]([]int{1, 2, 3}, func(v int) int { return v })
	_ = xs

	_ = MyInt(5)
	_ = len(xs)

	d := Derived{}
	d.Hello()

	var g Greeter = d
	g.Hello()
}
`)
	value := mustIndexGo(t, root)
	if len(value.Coverage.DegradedFiles) != 0 {
		t.Fatalf("expected zero degraded files for a clean package with unusual call shapes, got %#v", value.Coverage.DegradedFiles)
	}
	if isDegraded(value, "main.go") {
		t.Fatalf("main.go must not be degraded; coverage=%#v", value.Coverage)
	}
}

// TestGoTypesImplementsCheckCountNotMultipliedByTestVariants reproduces the
// grpc-go-scale perf defect: emitImplementsEdges previously ran a full
// O(len(types)×len(interfaces)) sweep once for the primary universe and
// again, at nearly the same size, for every test-variant package in the
// module. This asserts the total number of types.Implements checks stays
// close to one full pass plus each variant's own small, newly-declared
// pairs, rather than being multiplied by the number of variants.
//
// This test mutates the package-level implementsCheckHook test hook and
// must not run in parallel with other tests, so the mutation is not
// observed by tests that call t.Parallel() and run concurrently.
func TestGoTypesImplementsCheckCountNotMultipliedByTestVariants(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/variantperf\n\ngo 1.24\n")

	// A module-wide universe of 15 interfaces and 15 concrete types in the
	// root package, so the primary pass alone performs a non-trivial number
	// of checks to compare a per-variant resweep's cost against.
	const universeSize = 15
	var mainSrc strings.Builder
	mainSrc.WriteString("package variantperf\n\n")
	for i := 0; i < universeSize; i++ {
		fmt.Fprintf(&mainSrc, "type Iface%d interface{ M%d() }\n", i, i)
		fmt.Fprintf(&mainSrc, "type Impl%d struct{}\n", i)
		fmt.Fprintf(&mainSrc, "func (Impl%d) M%d() {}\n", i, i)
	}
	writeTestFile(t, filepath.Join(root, "main.go"), mainSrc.String())

	// 5 separate packages, each with its own internal test file, so
	// go/packages produces 5 distinct "[subN.test]" type-checking universes
	// (each recompiling the whole module-wide universe of interfaces this
	// package can see) rather than a single shared variant.
	const numVariants = 5
	for v := 0; v < numVariants; v++ {
		pkgDir := filepath.Join(root, fmt.Sprintf("sub%d", v))
		writeTestFile(t, filepath.Join(pkgDir, "sub.go"), fmt.Sprintf("package sub%d\n\ntype T struct{}\n", v))
		writeTestFile(t, filepath.Join(pkgDir, "sub_internal_test.go"), fmt.Sprintf(`package sub%d

type fakeT struct{}

func (fakeT) M() {}
`, v))
	}

	var count int64
	original := implementsCheckHook
	implementsCheckHook = func() { atomic.AddInt64(&count, 1) }
	defer func() { implementsCheckHook = original }()

	value := mustIndexGo(t, root)
	if len(value.Nodes) == 0 {
		t.Fatal("expected a non-empty graph")
	}

	// Primary universe: 15 interfaces x (15 root types + 5 sub-package
	// types) = 300 checks. Each variant's own universe is nearly as large
	// (production types plus its one new fakeT), so a full O(T*I) resweep
	// repeated per variant, as the pre-fix code did, would cost roughly
	// (1 + numVariants) times the primary pass, well over 1500. Limiting
	// each variant to its own newly-declared pairs (1 new type x 15
	// interfaces per variant here) keeps the total well under that.
	got := atomic.LoadInt64(&count)
	const maxExpected = 700
	if got == 0 {
		t.Fatal("expected at least one types.Implements check")
	}
	if got > maxExpected {
		t.Fatalf("types.Implements check count = %d, want <= %d (checks must not be multiplied by test variants)", got, maxExpected)
	}
}
