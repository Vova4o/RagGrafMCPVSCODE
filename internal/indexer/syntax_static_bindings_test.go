package indexer

import (
	"testing"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

func hasSyntaxBinding(facts syntaxFacts, container, field, typ string, local bool) bool {
	for _, binding := range facts.Bindings {
		if binding.Container == container && binding.Field == field && binding.Type == typ && binding.Local == local {
			return true
		}
	}
	return false
}

// syntaxBindingScope returns the EndByte and ScopeStart of the local binding
// named field, failing the test if no such binding exists.
func syntaxBindingScope(t *testing.T, facts syntaxFacts, field string) (end, scopeStart uint32) {
	t.Helper()
	for _, binding := range facts.Bindings {
		if binding.Local && binding.Field == field {
			return binding.EndByte, binding.ScopeStart
		}
	}
	t.Fatalf("no local binding named %q in %#v", field, facts.Bindings)
	return 0, 0
}

func hasSyntaxHeritage(facts syntaxFacts, typeName, super, kind string) bool {
	for _, heritage := range facts.Heritage {
		if heritage.Type == typeName && heritage.Super == super && heritage.Kind == kind {
			return true
		}
	}
	return false
}

func syntaxDeclarationInterface(t *testing.T, facts syntaxFacts, name, container string) bool {
	t.Helper()
	for _, decl := range facts.Declarations {
		if decl.Name == name && decl.Container == container {
			return decl.Interface
		}
	}
	t.Fatalf("missing declaration name=%q container=%q in %#v", name, container, facts.Declarations)
	return false
}

func TestParseSyntaxJavaThisFieldChainCallTargetKeepsThis(t *testing.T) {
	t.Parallel()
	source := "class Runner {\n  void run() { this.indexer.index(); }\n}\n"
	facts := parseSyntaxTest(t, "java", "Runner.java", source)
	if !hasSyntaxCall(facts, "this.indexer.index") {
		t.Fatalf("calls = %#v, want this.indexer.index target", facts.Calls)
	}
}

func TestParseSyntaxJavaMultiSegmentCallTargetNotFlattened(t *testing.T) {
	t.Parallel()
	source := "class Runner {\n  void run() { a.b.c(); }\n}\n"
	facts := parseSyntaxTest(t, "java", "Runner.java", source)
	if !hasSyntaxCall(facts, "a.b.c") {
		t.Fatalf("calls = %#v, want a.b.c target", facts.Calls)
	}
}

func TestParseSyntaxJavaCallOnCallResultGetsUnresolvedMarker(t *testing.T) {
	t.Parallel()
	source := "class Runner {\n  void run() { foo().bar(); }\n}\n"
	facts := parseSyntaxTest(t, "java", "Runner.java", source)
	if !hasSyntaxCall(facts, "<expr>.bar") {
		t.Fatalf("calls = %#v, want <expr>.bar target", facts.Calls)
	}
}

func TestParseSyntaxJavaFieldDeclarationBinding(t *testing.T) {
	t.Parallel()
	source := "class Store {\n  private Indexer indexer;\n}\n"
	facts := parseSyntaxTest(t, "java", "Store.java", source)
	if !hasSyntaxBinding(facts, "Store", "indexer", "Indexer", false) {
		t.Fatalf("bindings = %#v, want Store.indexer field binding typed Indexer", facts.Bindings)
	}
}

func TestParseSyntaxJavaTypedLocalAndParamBindings(t *testing.T) {
	t.Parallel()
	source := "class Store {\n  void run(Foo x) {\n    Foo local = new Foo();\n  }\n}\n"
	facts := parseSyntaxTest(t, "java", "Store.java", source)
	if !hasSyntaxBinding(facts, "Store", "x", "Foo", true) {
		t.Fatalf("bindings = %#v, want param x typed Foo", facts.Bindings)
	}
	if !hasSyntaxBinding(facts, "Store", "local", "Foo", true) {
		t.Fatalf("bindings = %#v, want local typed Foo", facts.Bindings)
	}
}

func TestParseSyntaxJavaLambdaParamShadowBinding(t *testing.T) {
	t.Parallel()
	source := "class Store {\n  void run() {\n    Runnable r = (Foo x) -> { x.save(); };\n  }\n}\n"
	facts := parseSyntaxTest(t, "java", "Store.java", source)
	if !hasSyntaxBinding(facts, "Store", "x", "Foo", true) {
		t.Fatalf("bindings = %#v, want lambda param x typed Foo", facts.Bindings)
	}
}

func TestParseSyntaxJavaForEachBinding(t *testing.T) {
	t.Parallel()
	source := "class Store {\n  void run() {\n    for (Foo f : items) { f.save(); }\n  }\n}\n"
	facts := parseSyntaxTest(t, "java", "Store.java", source)
	if !hasSyntaxBinding(facts, "Store", "f", "Foo", true) {
		t.Fatalf("bindings = %#v, want for-each var f typed Foo", facts.Bindings)
	}
}

func TestParseSyntaxJavaCatchBinding(t *testing.T) {
	t.Parallel()
	source := "class Store {\n  void run() {\n    try {} catch (Exception e) { e.printStackTrace(); }\n  }\n}\n"
	facts := parseSyntaxTest(t, "java", "Store.java", source)
	if !hasSyntaxBinding(facts, "Store", "e", "Exception", true) {
		t.Fatalf("bindings = %#v, want catch param e typed Exception", facts.Bindings)
	}
}

func TestParseSyntaxJavaExtendsAndImplementsHeritage(t *testing.T) {
	t.Parallel()
	source := "class Derived extends Base implements Foo, Bar {\n}\n"
	facts := parseSyntaxTest(t, "java", "Derived.java", source)
	if !hasSyntaxHeritage(facts, "Derived", "Base", syntaxHeritageExtends) {
		t.Fatalf("heritage = %#v, want Derived extends Base", facts.Heritage)
	}
	if !hasSyntaxHeritage(facts, "Derived", "Foo", syntaxHeritageImplements) {
		t.Fatalf("heritage = %#v, want Derived implements Foo", facts.Heritage)
	}
	if !hasSyntaxHeritage(facts, "Derived", "Bar", syntaxHeritageImplements) {
		t.Fatalf("heritage = %#v, want Derived implements Bar", facts.Heritage)
	}
}

func TestParseSyntaxInterfaceFlagPerLanguage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		language string
		filename string
		source   string
		typeName string
	}{
		{"java", "java", "Foo.java", "interface Foo {\n  void run();\n}\n", "Foo"},
		{"csharp", "csharp", "Foo.cs", "interface Foo {\n  void Run();\n}\n", "Foo"},
		{"kotlin", "kotlin", "Foo.kt", "interface Foo {\n  fun run()\n}\n", "Foo"},
		{"kotlin fun interface", "kotlin", "Foo.kt", "fun interface Foo {\n  fun run()\n}\n", "Foo"},
		{"rust", "rust", "lib.rs", "trait Foo {\n  fn run(&self);\n}\n", "Foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			facts := parseSyntaxTest(t, tc.language, tc.filename, tc.source)
			if !syntaxDeclarationInterface(t, facts, tc.typeName, "") {
				t.Fatalf("declaration %s.Interface = false, want true", tc.typeName)
			}
		})
	}
}

func TestParseSyntaxCSharpBaseListHeritage(t *testing.T) {
	t.Parallel()
	source := "class App : Base, IFoo {\n}\n"
	facts := parseSyntaxTest(t, "csharp", "App.cs", source)
	if !hasSyntaxHeritage(facts, "App", "Base", "") {
		t.Fatalf("heritage = %#v, want App -> Base", facts.Heritage)
	}
	if !hasSyntaxHeritage(facts, "App", "IFoo", "") {
		t.Fatalf("heritage = %#v, want App -> IFoo", facts.Heritage)
	}
}

func TestParseSyntaxCSharpPropertyBinding(t *testing.T) {
	t.Parallel()
	source := "class Store {\n  Indexer Indexer { get; set; }\n}\n"
	facts := parseSyntaxTest(t, "csharp", "Store.cs", source)
	if !hasSyntaxBinding(facts, "Store", "Indexer", "Indexer", false) {
		t.Fatalf("bindings = %#v, want Store.Indexer property binding typed Indexer", facts.Bindings)
	}
}

func TestParseSyntaxKotlinPrimaryConstructorValBinding(t *testing.T) {
	t.Parallel()
	source := "class Store(val indexer: Indexer) {\n}\n"
	facts := parseSyntaxTest(t, "kotlin", "Store.kt", source)
	if !hasSyntaxBinding(facts, "Store", "indexer", "Indexer", false) {
		t.Fatalf("bindings = %#v, want Store.indexer field binding typed Indexer", facts.Bindings)
	}
}

func TestParseSyntaxKotlinObjectDeclarationActsAsContainer(t *testing.T) {
	t.Parallel()
	source := "object Singleton {\n  fun save() {}\n}\n"
	facts := parseSyntaxTest(t, "kotlin", "Singleton.kt", source)
	assertSyntaxDeclaration(t, source, facts, "save", graph.KindMethod, "Singleton", 2, 2)
}

func TestParseSyntaxKotlinImplicitItBinding(t *testing.T) {
	t.Parallel()
	source := "class Store {\n  fun run() {\n    val cb = { it.save() }\n  }\n}\n"
	facts := parseSyntaxTest(t, "kotlin", "Store.kt", source)
	if !hasSyntaxBinding(facts, "Store", "it", "", true) {
		t.Fatalf("bindings = %#v, want implicit it binding", facts.Bindings)
	}
}

func TestParseSyntaxRustStructFieldAndGenericImplContainerMatch(t *testing.T) {
	t.Parallel()
	source := "struct Foo<T> { x: Bar }\nimpl<T> Foo<T> {\n    fn save(&self) {}\n}\n"
	facts := parseSyntaxTest(t, "rust", "lib.rs", source)
	if !hasSyntaxBinding(facts, "Foo", "x", "Bar", false) {
		t.Fatalf("bindings = %#v, want Foo.x field binding typed Bar", facts.Bindings)
	}
	assertSyntaxDeclaration(t, source, facts, "save", graph.KindMethod, "Foo", 3, 3)
}

func TestParseSyntaxRustLetBindingTypes(t *testing.T) {
	t.Parallel()
	source := "fn run() {\n" +
		"    let a: Foo = get();\n" +
		"    let b = Foo { };\n" +
		"    let c = Foo::new();\n" +
		"}\n"
	facts := parseSyntaxTest(t, "rust", "lib.rs", source)
	if !hasSyntaxBinding(facts, "", "a", "Foo", true) {
		t.Fatalf("bindings = %#v, want a typed Foo from explicit type", facts.Bindings)
	}
	if !hasSyntaxBinding(facts, "", "b", "Foo", true) {
		t.Fatalf("bindings = %#v, want b typed Foo from struct literal", facts.Bindings)
	}
	if !hasSyntaxBinding(facts, "", "c", "", true) {
		t.Fatalf("bindings = %#v, want c untyped (Foo::new() is not inferred)", facts.Bindings)
	}
}

func TestParseSyntaxRustClosureParamBinding(t *testing.T) {
	t.Parallel()
	source := "fn run() {\n    let cb = |x: Foo| { x.save(); };\n}\n"
	facts := parseSyntaxTest(t, "rust", "lib.rs", source)
	if !hasSyntaxBinding(facts, "", "x", "Foo", true) {
		t.Fatalf("bindings = %#v, want closure param x typed Foo", facts.Bindings)
	}
}

func TestParseSyntaxRustClosureUntypedParamBinding(t *testing.T) {
	t.Parallel()
	source := "fn run() {\n    let cb = |x| x.save();\n}\n"
	facts := parseSyntaxTest(t, "rust", "lib.rs", source)
	if !hasSyntaxBinding(facts, "", "x", "", true) {
		t.Fatalf("bindings = %#v, want untyped closure param x", facts.Bindings)
	}
}

func TestParseSyntaxRustTraitFunctionSignatureIsMethod(t *testing.T) {
	t.Parallel()
	source := "trait Shape {\n    fn area(&self) -> f64;\n}\n"
	facts := parseSyntaxTest(t, "rust", "lib.rs", source)
	assertSyntaxDeclaration(t, source, facts, "area", graph.KindMethod, "Shape", 2, 2)
}

func TestParseSyntaxRustImplTraitForHeritage(t *testing.T) {
	t.Parallel()
	source := "trait Shape {\n    fn area(&self) -> f64;\n}\nimpl Shape for Store {\n    fn area(&self) -> f64 { 1.0 }\n}\n"
	facts := parseSyntaxTest(t, "rust", "lib.rs", source)
	if !hasSyntaxHeritage(facts, "Store", "Shape", syntaxHeritageImplements) {
		t.Fatalf("heritage = %#v, want Store implements Shape", facts.Heritage)
	}
}

func TestParseSyntaxRustTraitSupertraitsHeritage(t *testing.T) {
	t.Parallel()
	source := "trait A: B + C {\n}\n"
	facts := parseSyntaxTest(t, "rust", "lib.rs", source)
	if !hasSyntaxHeritage(facts, "A", "B", syntaxHeritageExtends) {
		t.Fatalf("heritage = %#v, want A extends B", facts.Heritage)
	}
	if !hasSyntaxHeritage(facts, "A", "C", syntaxHeritageExtends) {
		t.Fatalf("heritage = %#v, want A extends C", facts.Heritage)
	}
}

func TestParseSyntaxJavaParamAndLocalOfSameFunctionShareScopeStart(t *testing.T) {
	t.Parallel()
	source := "class Store {\n  void run(Foo x) {\n    Foo local = new Foo();\n  }\n}\n"
	facts := parseSyntaxTest(t, "java", "Store.java", source)
	_, paramScope := syntaxBindingScope(t, facts, "x")
	_, localScope := syntaxBindingScope(t, facts, "local")
	if paramScope != localScope {
		t.Fatalf("param x ScopeStart = %d, local ScopeStart = %d, want equal (same function scope)", paramScope, localScope)
	}
}

// TestParseSyntaxCSharpNestedExpressionLambdaParamsDistinctScopeStart covers
// expression-bodied lambdas nested at the tail of an outer lambda: the outer
// lambda's body IS the inner lambda, and the inner lambda's body IS its own
// tail expression, so both scopes end on the exact same byte. ScopeStart
// must still tell them apart.
func TestParseSyntaxCSharpNestedExpressionLambdaParamsDistinctScopeStart(t *testing.T) {
	t.Parallel()
	source := "class Store {\n  void Run() {\n    Func<int, Func<int, int>> f = x => y => y + x;\n  }\n}\n"
	facts := parseSyntaxTest(t, "csharp", "Store.cs", source)
	xEnd, xScope := syntaxBindingScope(t, facts, "x")
	yEnd, yScope := syntaxBindingScope(t, facts, "y")
	if xEnd != yEnd {
		t.Fatalf("expected x and y bindings to share EndByte (nested expression lambdas), got xEnd=%d yEnd=%d", xEnd, yEnd)
	}
	if xScope == yScope {
		t.Fatalf("expected distinct ScopeStart for nested lambda params ending on the same byte, got %d for both", xScope)
	}
}

// TestParseSyntaxRustNestedClosureParamsDistinctScopeStart covers a closure
// returning another closure as its tail expression (`|a| move |b| b.run()`):
// both closures' bodies end on the same byte, so only ScopeStart can
// distinguish the scope of "a" from the scope of "b".
func TestParseSyntaxRustNestedClosureParamsDistinctScopeStart(t *testing.T) {
	t.Parallel()
	source := "fn run() {\n    let cb = |a| move |b| b.run();\n}\n"
	facts := parseSyntaxTest(t, "rust", "lib.rs", source)
	aEnd, aScope := syntaxBindingScope(t, facts, "a")
	bEnd, bScope := syntaxBindingScope(t, facts, "b")
	if aEnd != bEnd {
		t.Fatalf("expected a and b bindings to share EndByte (nested closures), got aEnd=%d bEnd=%d", aEnd, bEnd)
	}
	if aScope == bScope {
		t.Fatalf("expected distinct ScopeStart for nested closure params ending on the same byte, got %d for both", aScope)
	}
}
