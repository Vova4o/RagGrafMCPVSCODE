package indexer

import (
	"strings"
	"testing"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

func TestParseSyntaxDynamicTSTypedParameter(t *testing.T) {
	t.Parallel()
	source := "function f(a: Foo) { return a; }\n"
	facts := parseSyntaxTest(t, "typescript", "f.ts", source)
	if !hasLocalBinding(facts, "a", "Foo") {
		t.Fatalf("bindings = %#v, want local binding a:Foo", facts.Bindings)
	}
}

func TestParseSyntaxDynamicTSConstNewExpression(t *testing.T) {
	t.Parallel()
	source := "const x = new Foo();\n"
	facts := parseSyntaxTest(t, "typescript", "x.ts", source)
	if !hasLocalBinding(facts, "x", "Foo") {
		t.Fatalf("bindings = %#v, want local binding x:Foo", facts.Bindings)
	}
}

func TestParseSyntaxDynamicTSArrowParamShadowsOuterBinding(t *testing.T) {
	t.Parallel()
	source := "const x: Foo = null;\nconst fn = (x) => { return x; };\n"
	facts := parseSyntaxTest(t, "typescript", "fn.ts", source)
	openParen := strings.Index(source, "(x)")
	if openParen < 0 {
		t.Fatalf("fixture missing arrow parameter: %q", source)
	}
	paramStart := uint32(openParen + 1)
	closeBrace := strings.Index(source, "};")
	if closeBrace < 0 {
		t.Fatalf("fixture missing arrow body close: %q", source)
	}
	arrowEnd := uint32(closeBrace + 1)
	found := false
	for _, b := range facts.Bindings {
		if b.Local && b.Field == "x" && b.Type == "" && b.StartByte == paramStart && b.EndByte == arrowEnd {
			found = true
		}
	}
	if !found {
		t.Fatalf("bindings = %#v, want shadowing arrow param x with Type \"\" spanning [%d,%d)", facts.Bindings, paramStart, arrowEnd)
	}
}

func TestParseSyntaxDynamicTSForOfVariable(t *testing.T) {
	t.Parallel()
	source := "function f(items: Foo[]) {\n  for (const item of items) {\n    item;\n  }\n}\n"
	facts := parseSyntaxTest(t, "typescript", "f.ts", source)
	if !hasLocalBinding(facts, "item", "") {
		t.Fatalf("bindings = %#v, want local binding item:\"\"", facts.Bindings)
	}
}

func TestParseSyntaxDynamicTSCatchParameter(t *testing.T) {
	t.Parallel()
	source := "function f() {\n  try {} catch (e) {}\n}\n"
	facts := parseSyntaxTest(t, "typescript", "f.ts", source)
	if !hasLocalBinding(facts, "e", "") {
		t.Fatalf("bindings = %#v, want local binding e:\"\"", facts.Bindings)
	}
}

func TestParseSyntaxDynamicTSClassFieldContainerMatchesNestedMethodContainer(t *testing.T) {
	t.Parallel()
	source := "function outer() {\n  class Inner {\n    x: Foo;\n    save() { return this.x; }\n  }\n  return Inner;\n}\n"
	facts := parseSyntaxTest(t, "typescript", "outer.ts", source)
	saveContainer := declarationContainer(t, facts, "save", graph.KindMethod)
	if !hasFieldBinding(facts, saveContainer, "x", "Foo") {
		t.Fatalf("bindings = %#v, want field x:Foo with container %q (save's own container)", facts.Bindings, saveContainer)
	}
}

func TestParseSyntaxDynamicTSInterfaceMethodSignature(t *testing.T) {
	t.Parallel()
	source := "interface Store {\n  save(): void;\n}\n"
	facts := parseSyntaxTest(t, "typescript", "store.ts", source)
	assertSyntaxDeclaration(t, source, facts, "save", graph.KindMethod, "Store", 2, 2)
}

func TestParseSyntaxDynamicTSAbstractMethodSignature(t *testing.T) {
	t.Parallel()
	source := "abstract class Store {\n  abstract save(): void;\n}\n"
	facts := parseSyntaxTest(t, "typescript", "store.ts", source)
	assertSyntaxDeclaration(t, source, facts, "Store", graph.KindType, "", 1, 3)
	assertSyntaxDeclaration(t, source, facts, "save", graph.KindMethod, "Store", 2, 2)
}

func TestParseSyntaxDynamicTSInterfaceDeclarationSetsInterfaceFlag(t *testing.T) {
	t.Parallel()
	source := "interface Store {\n  save(): void;\n}\n"
	facts := parseSyntaxTest(t, "typescript", "store.ts", source)
	if !declarationInterfaceFlag(t, facts, "Store", graph.KindType) {
		t.Fatalf("declarations = %#v, want Store interface_declaration to set Interface", facts.Declarations)
	}
}

func TestParseSyntaxDynamicTSClassHeritageExtendsAndImplements(t *testing.T) {
	t.Parallel()
	source := "class A extends B implements C, D {\n}\n"
	facts := parseSyntaxTest(t, "typescript", "a.ts", source)
	if !hasHeritage(facts, "A", "B", syntaxHeritageExtends) {
		t.Errorf("heritage = %#v, missing A extends B", facts.Heritage)
	}
	if !hasHeritage(facts, "A", "C", syntaxHeritageImplements) {
		t.Errorf("heritage = %#v, missing A implements C", facts.Heritage)
	}
	if !hasHeritage(facts, "A", "D", syntaxHeritageImplements) {
		t.Errorf("heritage = %#v, missing A implements D", facts.Heritage)
	}
}

func TestParseSyntaxDynamicTSInterfaceHeritageExtends(t *testing.T) {
	t.Parallel()
	source := "interface I extends J, K {\n}\n"
	facts := parseSyntaxTest(t, "typescript", "i.ts", source)
	if !hasHeritage(facts, "I", "J", syntaxHeritageExtends) {
		t.Errorf("heritage = %#v, missing I extends J", facts.Heritage)
	}
	if !hasHeritage(facts, "I", "K", syntaxHeritageExtends) {
		t.Errorf("heritage = %#v, missing I extends K", facts.Heritage)
	}
}

func TestParseSyntaxDynamicPythonSelfFieldInNonInitMethod(t *testing.T) {
	t.Parallel()
	source := "class Store:\n    def run(self):\n        self.x = Foo()\n"
	facts := parseSyntaxTest(t, "python", "store.py", source)
	if !hasFieldBinding(facts, "Store", "x", "Foo") {
		t.Fatalf("bindings = %#v, want field x:Foo on Store from self.x = Foo() in run()", facts.Bindings)
	}
}

func TestParseSyntaxDynamicPythonClassLevelAnnotation(t *testing.T) {
	t.Parallel()
	source := "class Store:\n    x: Foo\n"
	facts := parseSyntaxTest(t, "python", "store.py", source)
	if !hasFieldBinding(facts, "Store", "x", "Foo") {
		t.Fatalf("bindings = %#v, want field x:Foo from class-level annotation", facts.Bindings)
	}
}

func TestParseSyntaxDynamicPythonTypedParameter(t *testing.T) {
	t.Parallel()
	source := "def run(a: Bar):\n    return a\n"
	facts := parseSyntaxTest(t, "python", "run.py", source)
	if !hasLocalBinding(facts, "a", "Bar") {
		t.Fatalf("bindings = %#v, want local binding a:Bar", facts.Bindings)
	}
}

func TestParseSyntaxDynamicPythonLocalConstructorAssignment(t *testing.T) {
	t.Parallel()
	source := "def run():\n    x = Foo()\n    return x\n"
	facts := parseSyntaxTest(t, "python", "run.py", source)
	if !hasLocalBinding(facts, "x", "Foo") {
		t.Fatalf("bindings = %#v, want local binding x:Foo", facts.Bindings)
	}
}

func TestParseSyntaxDynamicPythonReassignmentIsSeparateEmptyTypeBinding(t *testing.T) {
	t.Parallel()
	source := "def run():\n    x = Foo()\n    x = get()\n    return x\n"
	facts := parseSyntaxTest(t, "python", "run.py", source)
	count := 0
	for _, b := range facts.Bindings {
		if b.Local && b.Field == "x" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("bindings = %#v, want two separate x introductions, got %d", facts.Bindings, count)
	}
	if !hasLocalBinding(facts, "x", "Foo") {
		t.Errorf("bindings = %#v, missing first introduction x:Foo", facts.Bindings)
	}
	if !hasLocalBinding(facts, "x", "") {
		t.Errorf("bindings = %#v, missing reassignment x:\"\" from x = get()", facts.Bindings)
	}
}

func TestParseSyntaxDynamicPythonLambdaAndComprehensionBindingsWithSpans(t *testing.T) {
	t.Parallel()
	source := "def run():\n    fn = lambda p: p + 1\n    squares = [y * y for y in range(10)]\n"
	facts := parseSyntaxTest(t, "python", "run.py", source)

	lambdaKeyword := strings.Index(source, "lambda p")
	if lambdaKeyword < 0 {
		t.Fatalf("fixture missing lambda: %q", source)
	}
	paramStart := uint32(lambdaKeyword + len("lambda "))
	bodyMarker := "p + 1"
	bodyIdx := strings.Index(source, bodyMarker)
	if bodyIdx < 0 {
		t.Fatalf("fixture missing lambda body: %q", source)
	}
	lambdaEnd := uint32(bodyIdx + len(bodyMarker))
	foundLambdaParam := false
	for _, b := range facts.Bindings {
		if b.Local && b.Field == "p" && b.StartByte == paramStart && b.EndByte == lambdaEnd {
			foundLambdaParam = true
		}
	}
	if !foundLambdaParam {
		t.Fatalf("bindings = %#v, want lambda param p spanning [%d,%d)", facts.Bindings, paramStart, lambdaEnd)
	}

	compMarker := "[y * y for y in range(10)]"
	compIdx := strings.Index(source, compMarker)
	if compIdx < 0 {
		t.Fatalf("fixture missing comprehension: %q", source)
	}
	compEnd := uint32(compIdx + len(compMarker))
	forY := strings.Index(source, "for y in")
	if forY < 0 {
		t.Fatalf("fixture missing comprehension for-clause: %q", source)
	}
	yStart := uint32(forY + len("for "))
	foundCompVar := false
	for _, b := range facts.Bindings {
		if b.Local && b.Field == "y" && b.StartByte == yStart && b.EndByte == compEnd {
			foundCompVar = true
		}
	}
	if !foundCompVar {
		t.Fatalf("bindings = %#v, want comprehension var y spanning [%d,%d)", facts.Bindings, yStart, compEnd)
	}
}

func TestParseSyntaxDynamicPythonProtocolBaseSetsInterfaceFlag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		source string
	}{
		{"bare", "class P(Protocol):\n    pass\n"},
		{"qualified", "class Q(typing.Protocol):\n    pass\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			facts := parseSyntaxTest(t, "python", "protocol.py", tc.source)
			className := "P"
			if tc.name == "qualified" {
				className = "Q"
			}
			if !declarationInterfaceFlag(t, facts, className, graph.KindType) {
				t.Fatalf("declarations = %#v, want %s to set Interface via Protocol base", facts.Declarations, className)
			}
		})
	}
}

func TestParseSyntaxDynamicPythonHeritageGenericAndMetaclass(t *testing.T) {
	t.Parallel()
	source := "class A(B, Generic[T], metaclass=Foo, object):\n    pass\n"
	facts := parseSyntaxTest(t, "python", "a.py", source)
	if !hasHeritage(facts, "A", "B", "") {
		t.Errorf("heritage = %#v, missing A -> B", facts.Heritage)
	}
	if !hasHeritage(facts, "A", "Generic", "") {
		t.Errorf("heritage = %#v, missing A -> Generic (subscript stripped)", facts.Heritage)
	}
	if hasHeritage(facts, "A", "Foo", "") {
		t.Errorf("heritage = %#v, metaclass keyword argument leaked into heritage", facts.Heritage)
	}
	if hasHeritage(facts, "A", "object", "") {
		t.Errorf("heritage = %#v, object base leaked into heritage", facts.Heritage)
	}
}

func TestParseSyntaxDynamicJSPlainReassignmentSharesDeclaringEndByte(t *testing.T) {
	t.Parallel()
	source := "let x = new A();\nif (c) {\n  x = new B();\n}\nx.run();\n"
	facts := parseSyntaxTest(t, "javascript", "x.js", source)

	var declEnd, reassignEnd uint32
	var foundDecl, foundReassign bool
	for _, b := range facts.Bindings {
		if !b.Local || b.Field != "x" {
			continue
		}
		switch b.Type {
		case "A":
			declEnd = b.EndByte
			foundDecl = true
		case "B":
			reassignEnd = b.EndByte
			foundReassign = true
		}
	}
	if !foundDecl {
		t.Fatalf("bindings = %#v, want local binding x:A from declaration", facts.Bindings)
	}
	if !foundReassign {
		t.Fatalf("bindings = %#v, want local binding x:B from reassignment", facts.Bindings)
	}
	if declEnd != reassignEnd {
		t.Fatalf("declaration EndByte=%d, reassignment EndByte=%d, want equal so the resolver can pair them", declEnd, reassignEnd)
	}
}

func TestParseSyntaxDynamicJSTypedReassignmentInsideFunction(t *testing.T) {
	t.Parallel()
	source := "function f() {\n  let x = new A();\n  if (c) {\n    x = new B();\n  }\n  x.run();\n}\n"
	facts := parseSyntaxTest(t, "javascript", "f.js", source)
	if !hasLocalBinding(facts, "x", "A") {
		t.Fatalf("bindings = %#v, want local binding x:A", facts.Bindings)
	}
	if !hasLocalBinding(facts, "x", "B") {
		t.Fatalf("bindings = %#v, want local binding x:B from reassignment", facts.Bindings)
	}
}

func TestParseSyntaxDynamicJSCompoundAssignmentHasEmptyType(t *testing.T) {
	t.Parallel()
	source := "function f() {\n  let x = new A();\n  x ??= new B();\n}\n"
	facts := parseSyntaxTest(t, "javascript", "f.js", source)
	if !hasLocalBinding(facts, "x", "A") {
		t.Fatalf("bindings = %#v, want local binding x:A from declaration", facts.Bindings)
	}
	if !hasLocalBinding(facts, "x", "") {
		t.Fatalf("bindings = %#v, want compound reassignment x with Type \"\" even though right side is new B()", facts.Bindings)
	}
	if hasLocalBinding(facts, "x", "B") {
		t.Fatalf("bindings = %#v, compound assignment ??= must not infer the constructed type", facts.Bindings)
	}
}

func TestParseSyntaxDynamicJSReassignmentOfUndeclaredNameUsesFunctionEnd(t *testing.T) {
	t.Parallel()
	source := "function f() {\n  y = new A();\n  y.run();\n}\n"
	facts := parseSyntaxTest(t, "javascript", "f.js", source)
	fEnd := uint32(strings.LastIndex(source, "}") + 1)
	found := false
	for _, b := range facts.Bindings {
		if b.Local && b.Field == "y" && b.Type == "A" && b.EndByte == fEnd {
			found = true
		}
	}
	if !found {
		t.Fatalf("bindings = %#v, want implicit global y:A scoped to enclosing function end %d", facts.Bindings, fEnd)
	}
}

func TestParseSyntaxDynamicJSGeneratorFunctionDeclarationGetsOwnParamBinding(t *testing.T) {
	t.Parallel()
	source := "const api = new Api();\nfunction* g(api) {\n  api.run();\n}\n"
	facts := parseSyntaxTest(t, "javascript", "g.js", source)
	if !hasLocalBinding(facts, "api", "") {
		t.Fatalf("bindings = %#v, want generator param api with Type \"\" shadowing the outer const api = new Api()", facts.Bindings)
	}
}

func TestParseSyntaxDynamicJSGeneratorMethodGetsOwnParamBinding(t *testing.T) {
	t.Parallel()
	source := "const api = new Api();\nclass X {\n  *m(api) {\n    api.run();\n  }\n}\n"
	facts := parseSyntaxTest(t, "javascript", "x.js", source)
	if !hasLocalBinding(facts, "api", "") {
		t.Fatalf("bindings = %#v, want generator method param api with Type \"\" shadowing the outer const api = new Api()", facts.Bindings)
	}
}

func TestParseSyntaxDynamicJSAsyncGeneratorFunctionExpressionGetsOwnParamBinding(t *testing.T) {
	t.Parallel()
	source := "const api = new Api();\nconst h = async function*(api) {\n  api.run();\n};\n"
	facts := parseSyntaxTest(t, "typescript", "h.ts", source)
	if !hasLocalBinding(facts, "api", "") {
		t.Fatalf("bindings = %#v, want async generator expression param api with Type \"\" shadowing the outer const api = new Api()", facts.Bindings)
	}
}

func TestParseSyntaxDynamicTSClassFieldNullPlaceholderNotBound(t *testing.T) {
	t.Parallel()
	source := "class X {\n  b = undefined;\n  c = null;\n}\n"
	facts := parseSyntaxTest(t, "typescript", "x.ts", source)
	if hasFieldBinding(facts, "X", "b", "") {
		t.Fatalf("bindings = %#v, x = undefined must not emit a Type \"\" field binding", facts.Bindings)
	}
	if hasFieldBinding(facts, "X", "c", "") {
		t.Fatalf("bindings = %#v, x = null must not emit a Type \"\" field binding", facts.Bindings)
	}
}

func TestParseSyntaxDynamicTSClassFieldNullableUnionKeepsAnnotationType(t *testing.T) {
	t.Parallel()
	source := "class X {\n  a: Foo | null = null;\n}\n"
	facts := parseSyntaxTest(t, "typescript", "x.ts", source)
	if !hasFieldBinding(facts, "X", "a", "Foo") {
		t.Fatalf("bindings = %#v, want field a:Foo from annotation despite null initializer", facts.Bindings)
	}
}

func TestParseSyntaxDynamicJSThisFieldNullAssignmentNotBound(t *testing.T) {
	t.Parallel()
	source := "class X {\n  run() {\n    this.x = null;\n  }\n}\n"
	facts := parseSyntaxTest(t, "javascript", "x.js", source)
	for _, b := range facts.Bindings {
		if !b.Local && b.Container == "X" && b.Field == "x" {
			t.Fatalf("bindings = %#v, this.x = null must not emit any field binding", facts.Bindings)
		}
	}
}

func TestParseSyntaxDynamicPythonSelfFieldNonePlaceholderNotBound(t *testing.T) {
	t.Parallel()
	source := "class Store:\n    def __init__(self):\n        self.x = None\n"
	facts := parseSyntaxTest(t, "python", "store.py", source)
	if hasFieldBinding(facts, "Store", "x", "") {
		t.Fatalf("bindings = %#v, self.x = None must not emit a Type \"\" field binding", facts.Bindings)
	}
}

func TestParseSyntaxDynamicPythonReassignmentEndByteMatchesFunctionEnd(t *testing.T) {
	t.Parallel()
	source := "def run():\n    x = Foo()\n    x = get()\n    return x\n"
	facts := parseSyntaxTest(t, "python", "run.py", source)
	var ends []uint32
	for _, b := range facts.Bindings {
		if b.Local && b.Field == "x" {
			ends = append(ends, b.EndByte)
		}
	}
	if len(ends) != 2 {
		t.Fatalf("bindings = %#v, want two local x introductions, got %d", facts.Bindings, len(ends))
	}
	if ends[0] != ends[1] {
		t.Fatalf("bindings = %#v, want both x introductions scoped to the same enclosing function end, got %d and %d", facts.Bindings, ends[0], ends[1])
	}
}

func TestParseSyntaxDynamicJSPlainClassExtendsProducesHeritage(t *testing.T) {
	t.Parallel()
	source := "class Base {\n  helper() {}\n}\n\nclass Derived extends Base {\n}\n"
	facts := parseSyntaxTest(t, "javascript", "app.js", source)
	if !hasHeritage(facts, "Derived", "Base", syntaxHeritageExtends) {
		t.Fatalf("heritage = %#v, want plain JavaScript class_heritage to produce Derived extends Base", facts.Heritage)
	}
}

func TestParseSyntaxDynamicJSClassExpressionExtendsProducesHeritage(t *testing.T) {
	t.Parallel()
	source := "class Base {\n  helper() {}\n}\n\nconst D = class extends Base {\n};\n"
	facts := parseSyntaxTest(t, "javascript", "app.js", source)
	if !hasHeritage(facts, "D", "Base", syntaxHeritageExtends) {
		t.Fatalf("heritage = %#v, want anonymous class expression assigned to D to produce D extends Base", facts.Heritage)
	}
}

func TestParseSyntaxDynamicJSVarIsFunctionScopedAcrossNestedBlock(t *testing.T) {
	t.Parallel()
	source := "function f() {\n  var x = new A();\n  if (c) {\n    var x = new B();\n  }\n  x.run();\n}\n"
	facts := parseSyntaxTest(t, "javascript", "f.js", source)

	fEnd := uint32(strings.LastIndex(source, "}") + 1)
	var declEnd, redeclEnd uint32
	var foundDecl, foundRedecl bool
	for _, b := range facts.Bindings {
		if !b.Local || b.Field != "x" {
			continue
		}
		switch b.Type {
		case "A":
			declEnd = b.EndByte
			foundDecl = true
		case "B":
			redeclEnd = b.EndByte
			foundRedecl = true
		}
	}
	if !foundDecl {
		t.Fatalf("bindings = %#v, want local binding x:A from outer var declaration", facts.Bindings)
	}
	if !foundRedecl {
		t.Fatalf("bindings = %#v, want local binding x:B from nested var declaration", facts.Bindings)
	}
	if declEnd != fEnd {
		t.Fatalf("outer var x:A EndByte=%d, want function end %d (var is function-scoped)", declEnd, fEnd)
	}
	if redeclEnd != fEnd {
		t.Fatalf("nested var x:B EndByte=%d, want function end %d (var hoists past the if-block)", redeclEnd, fEnd)
	}
}

func TestParseSyntaxDynamicJSVarTopLevelIsProgramScoped(t *testing.T) {
	t.Parallel()
	source := "var x = new A();\nif (c) {\n  var x = new B();\n}\nx.run();\n"
	facts := parseSyntaxTest(t, "javascript", "x.js", source)

	programEnd := uint32(len(source))
	var declEnd, redeclEnd uint32
	var foundDecl, foundRedecl bool
	for _, b := range facts.Bindings {
		if !b.Local || b.Field != "x" {
			continue
		}
		switch b.Type {
		case "A":
			declEnd = b.EndByte
			foundDecl = true
		case "B":
			redeclEnd = b.EndByte
			foundRedecl = true
		}
	}
	if !foundDecl {
		t.Fatalf("bindings = %#v, want local binding x:A from top-level var declaration", facts.Bindings)
	}
	if !foundRedecl {
		t.Fatalf("bindings = %#v, want local binding x:B from nested var declaration", facts.Bindings)
	}
	if declEnd != programEnd {
		t.Fatalf("top-level var x:A EndByte=%d, want program end %d", declEnd, programEnd)
	}
	if redeclEnd != programEnd {
		t.Fatalf("nested var x:B EndByte=%d, want program end %d (var hoists past the if-block)", redeclEnd, programEnd)
	}
}

func TestParseSyntaxDynamicJSVarReassignmentInNestedBlockSharesFunctionScopeEndByte(t *testing.T) {
	t.Parallel()
	source := "function f() {\n  if (c) {\n    var x = new A();\n  }\n  x = new B();\n  x.run();\n}\n"
	facts := parseSyntaxTest(t, "javascript", "f.js", source)

	fEnd := uint32(strings.LastIndex(source, "}") + 1)
	var declEnd, reassignEnd uint32
	var foundDecl, foundReassign bool
	for _, b := range facts.Bindings {
		if !b.Local || b.Field != "x" {
			continue
		}
		switch b.Type {
		case "A":
			declEnd = b.EndByte
			foundDecl = true
		case "B":
			reassignEnd = b.EndByte
			foundReassign = true
		}
	}
	if !foundDecl {
		t.Fatalf("bindings = %#v, want local binding x:A from nested var declaration", facts.Bindings)
	}
	if !foundReassign {
		t.Fatalf("bindings = %#v, want local binding x:B from reassignment", facts.Bindings)
	}
	if declEnd != fEnd {
		t.Fatalf("nested var x:A EndByte=%d, want function end %d", declEnd, fEnd)
	}
	if reassignEnd != fEnd {
		t.Fatalf("reassignment x:B EndByte=%d, want function end %d, matching the var's function scope", reassignEnd, fEnd)
	}
}

func TestParseSyntaxDynamicJSLetStillBlockScopedAlongsideVar(t *testing.T) {
	t.Parallel()
	source := "function f() {\n  let x = new A();\n  if (c) {\n    let x = new B();\n    x.run();\n  }\n}\n"
	facts := parseSyntaxTest(t, "javascript", "f.js", source)

	ifBlockEnd := uint32(strings.LastIndex(source, "}\n}") + len("}"))
	fEnd := uint32(strings.LastIndex(source, "}") + 1)
	var outerEnd, innerEnd uint32
	var foundOuter, foundInner bool
	for _, b := range facts.Bindings {
		if !b.Local || b.Field != "x" {
			continue
		}
		switch b.Type {
		case "A":
			outerEnd = b.EndByte
			foundOuter = true
		case "B":
			innerEnd = b.EndByte
			foundInner = true
		}
	}
	if !foundOuter || !foundInner {
		t.Fatalf("bindings = %#v, want both let x:A and let x:B", facts.Bindings)
	}
	if outerEnd != fEnd {
		t.Fatalf("outer let x:A EndByte=%d, want function end %d", outerEnd, fEnd)
	}
	if innerEnd != ifBlockEnd {
		t.Fatalf("inner let x:B EndByte=%d, want if-block end %d (let stays block-scoped)", innerEnd, ifBlockEnd)
	}
}

func TestParseSyntaxDynamicJSNestedFunctionReassignsModuleLevelVariable(t *testing.T) {
	t.Parallel()
	source := "let x = new A();\nfunction f() {\n  x = new B();\n}\nfunction g() {\n  x.run();\n}\n"
	facts := parseSyntaxTest(t, "javascript", "app.js", source)

	programEnd := uint32(len(source))
	var declEnd, reassignEnd uint32
	var foundDecl, foundReassign bool
	for _, b := range facts.Bindings {
		if !b.Local || b.Field != "x" {
			continue
		}
		switch b.Type {
		case "A":
			declEnd = b.EndByte
			foundDecl = true
		case "B":
			reassignEnd = b.EndByte
			foundReassign = true
		}
	}
	if !foundDecl {
		t.Fatalf("bindings = %#v, want module-level local binding x:A", facts.Bindings)
	}
	if !foundReassign {
		t.Fatalf("bindings = %#v, want reassignment x:B from inside nested function f", facts.Bindings)
	}
	if declEnd != programEnd {
		t.Fatalf("module-level x:A EndByte=%d, want program end %d", declEnd, programEnd)
	}
	if reassignEnd != programEnd {
		t.Fatalf("reassignment inside nested function f x:B EndByte=%d, want the declaring module scope end %d, not f's own end", reassignEnd, programEnd)
	}
}

func TestParseSyntaxDynamicJSNestedFunctionReassignsOuterFunctionVariable(t *testing.T) {
	t.Parallel()
	source := "function outer() {\n  let x = new A();\n  function f() {\n    x = new B();\n  }\n  function g() {\n    x.run();\n  }\n}\n"
	facts := parseSyntaxTest(t, "javascript", "app.js", source)

	outerBodyEnd := uint32(strings.LastIndex(source, "}") + 1)
	var declEnd, reassignEnd uint32
	var foundDecl, foundReassign bool
	for _, b := range facts.Bindings {
		if !b.Local || b.Field != "x" {
			continue
		}
		switch b.Type {
		case "A":
			declEnd = b.EndByte
			foundDecl = true
		case "B":
			reassignEnd = b.EndByte
			foundReassign = true
		}
	}
	if !foundDecl {
		t.Fatalf("bindings = %#v, want outer() local binding x:A", facts.Bindings)
	}
	if !foundReassign {
		t.Fatalf("bindings = %#v, want reassignment x:B from inside nested function f", facts.Bindings)
	}
	if declEnd != outerBodyEnd {
		t.Fatalf("outer() x:A EndByte=%d, want outer() body end %d", declEnd, outerBodyEnd)
	}
	if reassignEnd != outerBodyEnd {
		t.Fatalf("reassignment inside nested function f x:B EndByte=%d, want outer()'s declaring scope end %d, not f's own end", reassignEnd, outerBodyEnd)
	}
}

func TestParseSyntaxDynamicPythonNestedFunctionSameEndByteDifferentScopeStart(t *testing.T) {
	t.Parallel()
	source := "def outer():\n    c = Client()\n    c.send()\n    def inner():\n        c = other()\n"
	facts := parseSyntaxTest(t, "python", "outer.py", source)

	var outerScopeStart, outerEnd, innerScopeStart, innerEnd uint32
	var foundOuter, foundInner bool
	for _, b := range facts.Bindings {
		if !b.Local || b.Field != "c" {
			continue
		}
		switch b.Type {
		case "Client":
			outerScopeStart, outerEnd = b.ScopeStart, b.EndByte
			foundOuter = true
		case "":
			innerScopeStart, innerEnd = b.ScopeStart, b.EndByte
			foundInner = true
		}
	}
	if !foundOuter {
		t.Fatalf("bindings = %#v, want outer's local binding c:Client", facts.Bindings)
	}
	if !foundInner {
		t.Fatalf("bindings = %#v, want inner's local binding c:\"\" from c = other()", facts.Bindings)
	}
	if outerEnd != innerEnd {
		t.Fatalf("outer c EndByte=%d, inner c EndByte=%d, want equal (Python has no closing token so both functions end on the same byte)", outerEnd, innerEnd)
	}
	if outerScopeStart == innerScopeStart {
		t.Fatalf("outer c ScopeStart=%d, inner c ScopeStart=%d, want different scopes despite the same EndByte", outerScopeStart, innerScopeStart)
	}
}

func TestParseSyntaxDynamicJSNestedArrowsSameEndByteDifferentScopeStart(t *testing.T) {
	t.Parallel()
	source := "(s: A) => () => { let s = new B(); s.run() };\n"
	facts := parseSyntaxTest(t, "typescript", "f.ts", source)

	var paramScopeStart, paramEnd, letScopeStart, letEnd uint32
	var foundParam, foundLet bool
	for _, b := range facts.Bindings {
		if !b.Local || b.Field != "s" {
			continue
		}
		switch b.Type {
		case "A":
			paramScopeStart, paramEnd = b.ScopeStart, b.EndByte
			foundParam = true
		case "B":
			letScopeStart, letEnd = b.ScopeStart, b.EndByte
			foundLet = true
		}
	}
	if !foundParam {
		t.Fatalf("bindings = %#v, want outer arrow param binding s:A", facts.Bindings)
	}
	if !foundLet {
		t.Fatalf("bindings = %#v, want inner let binding s:B", facts.Bindings)
	}
	if paramEnd != letEnd {
		t.Fatalf("outer param s:A EndByte=%d, inner let s:B EndByte=%d, want equal (both close on the same brace)", paramEnd, letEnd)
	}
	if paramScopeStart == letScopeStart {
		t.Fatalf("outer param s:A ScopeStart=%d, inner let s:B ScopeStart=%d, want different scopes despite the same EndByte", paramScopeStart, letScopeStart)
	}
}

func TestParseSyntaxDynamicJSLetReassignmentSharesScopeStartAndEndByte(t *testing.T) {
	t.Parallel()
	source := "let x = new A();\nif (c) {\n  x = new B();\n}\nx.run();\n"
	facts := parseSyntaxTest(t, "javascript", "x.js", source)

	var declScope, declEnd, reassignScope, reassignEnd uint32
	var foundDecl, foundReassign bool
	for _, b := range facts.Bindings {
		if !b.Local || b.Field != "x" {
			continue
		}
		switch b.Type {
		case "A":
			declScope, declEnd = b.ScopeStart, b.EndByte
			foundDecl = true
		case "B":
			reassignScope, reassignEnd = b.ScopeStart, b.EndByte
			foundReassign = true
		}
	}
	if !foundDecl || !foundReassign {
		t.Fatalf("bindings = %#v, want both declaration x:A and reassignment x:B", facts.Bindings)
	}
	if declEnd != reassignEnd {
		t.Fatalf("declaration EndByte=%d, reassignment EndByte=%d, want equal", declEnd, reassignEnd)
	}
	if declScope != reassignScope {
		t.Fatalf("declaration ScopeStart=%d, reassignment ScopeStart=%d, want equal so the resolver treats them as the same variable's scope", declScope, reassignScope)
	}
}

func TestParseSyntaxDynamicJSVarReassignmentSharesScopeStartAndEndByte(t *testing.T) {
	t.Parallel()
	source := "function f() {\n  if (c) {\n    var x = new A();\n  }\n  x = new B();\n  x.run();\n}\n"
	facts := parseSyntaxTest(t, "javascript", "f.js", source)

	var declScope, declEnd, reassignScope, reassignEnd uint32
	var foundDecl, foundReassign bool
	for _, b := range facts.Bindings {
		if !b.Local || b.Field != "x" {
			continue
		}
		switch b.Type {
		case "A":
			declScope, declEnd = b.ScopeStart, b.EndByte
			foundDecl = true
		case "B":
			reassignScope, reassignEnd = b.ScopeStart, b.EndByte
			foundReassign = true
		}
	}
	if !foundDecl || !foundReassign {
		t.Fatalf("bindings = %#v, want both var declaration x:A and reassignment x:B", facts.Bindings)
	}
	if declEnd != reassignEnd {
		t.Fatalf("var declaration EndByte=%d, reassignment EndByte=%d, want equal (function-scoped)", declEnd, reassignEnd)
	}
	if declScope != reassignScope {
		t.Fatalf("var declaration ScopeStart=%d, reassignment ScopeStart=%d, want equal", declScope, reassignScope)
	}
}

func TestParseSyntaxDynamicPythonReassignmentSharesScopeStartAndEndByte(t *testing.T) {
	t.Parallel()
	source := "def run():\n    x = Foo()\n    x = get()\n    return x\n"
	facts := parseSyntaxTest(t, "python", "run.py", source)

	var scopeStarts, ends []uint32
	for _, b := range facts.Bindings {
		if b.Local && b.Field == "x" {
			scopeStarts = append(scopeStarts, b.ScopeStart)
			ends = append(ends, b.EndByte)
		}
	}
	if len(scopeStarts) != 2 {
		t.Fatalf("bindings = %#v, want two local x introductions, got %d", facts.Bindings, len(scopeStarts))
	}
	if ends[0] != ends[1] {
		t.Fatalf("bindings = %#v, want both x introductions to share EndByte, got %d and %d", facts.Bindings, ends[0], ends[1])
	}
	if scopeStarts[0] != scopeStarts[1] {
		t.Fatalf("bindings = %#v, want both x introductions to share ScopeStart, got %d and %d", facts.Bindings, scopeStarts[0], scopeStarts[1])
	}
}

func TestParseSyntaxDynamicJSForOfVarIsFunctionScoped(t *testing.T) {
	t.Parallel()
	source := "function f(xs) {\n  var s = new A();\n  for (var s of xs) {\n    s;\n  }\n  s.run();\n}\n"
	facts := parseSyntaxTest(t, "javascript", "f.js", source)

	fEnd := uint32(strings.LastIndex(source, "}") + 1)
	var declScope, declEnd, loopScope, loopEnd uint32
	var foundDecl, foundLoop bool
	for _, b := range facts.Bindings {
		if !b.Local || b.Field != "s" {
			continue
		}
		switch b.Type {
		case "A":
			declScope, declEnd = b.ScopeStart, b.EndByte
			foundDecl = true
		case "":
			loopScope, loopEnd = b.ScopeStart, b.EndByte
			foundLoop = true
		}
	}
	if !foundDecl {
		t.Fatalf("bindings = %#v, want var declaration s:A", facts.Bindings)
	}
	if !foundLoop {
		t.Fatalf("bindings = %#v, want for-of var loop binding s with Type \"\"", facts.Bindings)
	}
	if declEnd != fEnd {
		t.Fatalf("var declaration s:A EndByte=%d, want function end %d", declEnd, fEnd)
	}
	if loopEnd != fEnd {
		t.Fatalf("for (var s of xs) EndByte=%d, want function end %d (var in for-of head is function-scoped, not loop-scoped)", loopEnd, fEnd)
	}
	if declScope != loopScope {
		t.Fatalf("var declaration ScopeStart=%d, for-of loop ScopeStart=%d, want equal so the resolver pairs them", declScope, loopScope)
	}
}

func TestParseSyntaxDynamicJSForInVarIsFunctionScoped(t *testing.T) {
	t.Parallel()
	source := "function f(o) {\n  var k = new A();\n  for (var k in o) {\n    k;\n  }\n  k.run();\n}\n"
	facts := parseSyntaxTest(t, "javascript", "f.js", source)

	fEnd := uint32(strings.LastIndex(source, "}") + 1)
	var declEnd, loopEnd uint32
	var foundDecl, foundLoop bool
	for _, b := range facts.Bindings {
		if !b.Local || b.Field != "k" {
			continue
		}
		switch b.Type {
		case "A":
			declEnd = b.EndByte
			foundDecl = true
		case "":
			loopEnd = b.EndByte
			foundLoop = true
		}
	}
	if !foundDecl || !foundLoop {
		t.Fatalf("bindings = %#v, want both var declaration k:A and for-in loop binding k", facts.Bindings)
	}
	if declEnd != fEnd || loopEnd != fEnd {
		t.Fatalf("bindings = %#v, want both k bindings scoped to function end %d, got decl=%d loop=%d", facts.Bindings, fEnd, declEnd, loopEnd)
	}
}

func TestParseSyntaxDynamicJSForOfLetStaysLoopScoped(t *testing.T) {
	t.Parallel()
	source := "function f(xs) {\n  for (let s of xs) {\n    s;\n  }\n}\n"
	facts := parseSyntaxTest(t, "javascript", "f.js", source)
	fEnd := uint32(strings.LastIndex(source, "}") + 1)
	found := false
	for _, b := range facts.Bindings {
		if b.Local && b.Field == "s" {
			found = true
			if b.EndByte == fEnd {
				t.Fatalf("let s in for-of EndByte=%d equals function end %d, want loop-scoped (let/const heads must not become function-scoped)", b.EndByte, fEnd)
			}
		}
	}
	if !found {
		t.Fatalf("bindings = %#v, want local binding s from for-of let head", facts.Bindings)
	}
}

func hasLocalBinding(facts syntaxFacts, field, typ string) bool {
	for _, b := range facts.Bindings {
		if b.Local && b.Field == field && b.Type == typ {
			return true
		}
	}
	return false
}

func hasFieldBinding(facts syntaxFacts, container, field, typ string) bool {
	for _, b := range facts.Bindings {
		if !b.Local && b.Container == container && b.Field == field && b.Type == typ {
			return true
		}
	}
	return false
}

func hasHeritage(facts syntaxFacts, typ, super, kind string) bool {
	for _, h := range facts.Heritage {
		if h.Type == typ && h.Super == super && h.Kind == kind {
			return true
		}
	}
	return false
}

func declarationContainer(t *testing.T, facts syntaxFacts, name, kind string) string {
	t.Helper()
	for _, d := range facts.Declarations {
		if d.Name == name && d.Kind == kind {
			return d.Container
		}
	}
	t.Fatalf("missing declaration name=%q kind=%q in %#v", name, kind, facts.Declarations)
	return ""
}

func declarationInterfaceFlag(t *testing.T, facts syntaxFacts, name, kind string) bool {
	t.Helper()
	for _, d := range facts.Declarations {
		if d.Name == name && d.Kind == kind {
			return d.Interface
		}
	}
	t.Fatalf("missing declaration name=%q kind=%q in %#v", name, kind, facts.Declarations)
	return false
}
