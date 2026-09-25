package indexer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

func TestParseSyntaxNestedJavaScriptArrowFunctionRanges(t *testing.T) {
	t.Parallel()
	source := "const runMcpTool = async () => {\n" +
		"  const finish = () => {\n" +
		"    return 1;\n" +
		"  };\n" +
		"  return finish();\n" +
		"};\n"
	facts := parseSyntaxTest(t, "javascript", "tool.js", source)
	assertSyntaxDeclaration(t, source, facts, "runMcpTool", graph.KindFunction, "", 1, 6)
	assertSyntaxDeclaration(t, source, facts, "finish", graph.KindFunction, "runMcpTool", 2, 4)
}

func TestIndexSyntaxNestedJavaScriptArrowFunctionRanges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "tool.js"), "const runMcpTool = async () => {\n"+
		"  const finish = () => {\n"+
		"    return 1;\n"+
		"  };\n"+
		"  return finish();\n"+
		"};\n")
	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	outer := syntaxGraphNode(t, value, "tool.js", "runMcpTool", graph.KindFunction)
	inner := syntaxGraphNode(t, value, "tool.js", "finish", graph.KindFunction)
	if outer.StartLine != 1 || outer.EndLine != 6 {
		t.Errorf("runMcpTool range = %d-%d, want 1-6", outer.StartLine, outer.EndLine)
	}
	if inner.StartLine != 2 || inner.EndLine != 4 {
		t.Errorf("finish range = %d-%d, want 2-4", inner.StartLine, inner.EndLine)
	}
}

func TestIndexSyntaxAttributesPostFinishCallToOuterArrowFunction(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := "const runMcpTool = async () => {\n" +
		"  const finish = () => {\n" +
		"    return 1;\n" +
		"  };\n" +
		"  return finish();\n" +
		"};\n"
	writeTestFile(t, filepath.Join(root, "tool.js"), source)
	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	outer := syntaxGraphNode(t, value, "tool.js", "runMcpTool", graph.KindFunction)
	if !hasCallTargetNamed(value, outer.ID, "finish") {
		t.Fatal("finish() call after the nested declaration was not attributed to runMcpTool")
	}
}

func TestParseSyntaxClassMethods(t *testing.T) {
	t.Parallel()
	cases := []struct {
		language  string
		filename  string
		source    string
		container string
	}{
		{"javascript", "item.js", "class Box {\n  save() { return 1; }\n}\n", "Box"},
		{"typescript", "item.ts", "class Box {\n  save(): number { return 1; }\n}\n", "Box"},
		{"tsx", "item.tsx", "class Box {\n  save(): number { return 1; }\n}\n", "Box"},
	}
	for _, tc := range cases {
		t.Run(tc.language, func(t *testing.T) {
			t.Parallel()
			facts := parseSyntaxTest(t, tc.language, tc.filename, tc.source)
			assertSyntaxDeclaration(t, tc.source, facts, "Box", graph.KindType, "", 1, 3)
			assertSyntaxDeclaration(t, tc.source, facts, "save", graph.KindMethod, tc.container, 2, 2)
		})
	}
}

func TestIndexSyntaxLinksRelativeJavaScriptImportAndCall(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src", "main.ts"), "import { run } from './worker';\nexport function start() { return run(); }\n")
	writeTestFile(t, filepath.Join(root, "src", "worker.ts"), "export function run() { return 1; }\n")
	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	mainFile := syntaxGraphNode(t, value, "src/main.ts", "main.ts", graph.KindFile)
	workerFile := syntaxGraphNode(t, value, "src/worker.ts", "worker.ts", graph.KindFile)
	start := syntaxGraphNode(t, value, "src/main.ts", "start", graph.KindFunction)
	run := syntaxGraphNode(t, value, "src/worker.ts", "run", graph.KindFunction)
	assertGraphEdge(t, value, mainFile.ID, workerFile.ID, graph.EdgeImports)
	assertGraphEdge(t, value, start.ID, run.ID, graph.EdgeCalls)
}

func TestIndexSyntaxLinksSideEffectJavaScriptImport(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src", "main.js"), "import './setup';\n")
	writeTestFile(t, filepath.Join(root, "src", "setup.ts"), "export const ready = true;\n")
	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	mainFile := syntaxGraphNode(t, value, "src/main.js", "main.js", graph.KindFile)
	setupFile := syntaxGraphNode(t, value, "src/setup.ts", "setup.ts", graph.KindFile)
	assertGraphEdge(t, value, mainFile.ID, setupFile.ID, graph.EdgeImports)
}

func TestIndexSyntaxDoesNotLinkTopLevelCallToClassMethod(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "app.js"), "class Worker {\n  foo() {}\n}\nfunction run() { foo(); }\n")
	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	run := syntaxGraphNode(t, value, "app.js", "run", graph.KindFunction)
	method := syntaxGraphNode(t, value, "app.js", "foo", graph.KindMethod)
	if hasGraphEdge(value, run.ID, method.ID, graph.EdgeCalls) {
		t.Fatal("top-level foo() call was linked to Worker.foo method")
	}
}

func TestIndexSyntaxStaticLanguageSameFileCalls(t *testing.T) {
	t.Parallel()
	cases := []struct {
		language string
		filename string
		source   string
		caller   string
		callee   string
		kind     string
	}{
		{
			language: "rust", filename: "src/lib.rs",
			source: "fn helper() {}\nfn run() { helper(); }\n",
			caller: "run", callee: "helper", kind: graph.KindFunction,
		},
		{
			language: "java", filename: "App.java",
			source: "class App {\n  void helper() {}\n  void run() { helper(); }\n}\n",
			caller: "run", callee: "helper", kind: graph.KindMethod,
		},
		{
			language: "kotlin", filename: "App.kt",
			source: "class App {\n  fun helper() {}\n  fun run() { helper() }\n}\n",
			caller: "run", callee: "helper", kind: graph.KindMethod,
		},
		{
			language: "csharp", filename: "App.cs",
			source: "class App {\n  void helper() {}\n  void Run() { helper(); }\n}\n",
			caller: "Run", callee: "helper", kind: graph.KindMethod,
		},
	}
	for _, tc := range cases {
		t.Run(tc.language, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, tc.filename), tc.source)
			value, err := New().Index(context.Background(), root)
			if err != nil {
				t.Fatalf("Index() error = %v", err)
			}
			assertNoSyntaxSkips(t, value)
			caller := syntaxGraphNode(t, value, tc.filename, tc.caller, tc.kind)
			callee := syntaxGraphNode(t, value, tc.filename, tc.callee, tc.kind)
			assertGraphEdge(t, value, caller.ID, callee.ID, graph.EdgeCalls)
		})
	}
}

func TestIndexSyntaxLinksPythonRelativeImportAndCall(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "pkg", "main.py"), "from .worker import build\n\ndef run():\n    return build()\n")
	writeTestFile(t, filepath.Join(root, "pkg", "worker.py"), "def build():\n    return 1\n")
	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	assertNoSyntaxSkips(t, value)
	mainFile := syntaxGraphNode(t, value, "pkg/main.py", "main.py", graph.KindFile)
	workerFile := syntaxGraphNode(t, value, "pkg/worker.py", "worker.py", graph.KindFile)
	run := syntaxGraphNode(t, value, "pkg/main.py", "run", graph.KindFunction)
	build := syntaxGraphNode(t, value, "pkg/worker.py", "build", graph.KindFunction)
	assertGraphEdge(t, value, mainFile.ID, workerFile.ID, graph.EdgeImports)
	assertGraphEdge(t, value, run.ID, build.ID, graph.EdgeCalls)
}

func TestIndexSyntaxResolvesThisMethodCallToClassMethod(t *testing.T) {
	t.Parallel()
	facts := parseSyntaxTest(t, "javascript", "app.js", "class Worker {\n  save() { this.save(); }\n}\n")
	if !hasSyntaxCall(facts, "this.save") {
		t.Fatalf("calls = %#v, want this.save target", facts.Calls)
	}
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "app.js"), "class Worker {\n  save() { this.save(); }\n}\n")
	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	save := syntaxGraphNode(t, value, "app.js", "save", graph.KindMethod)
	if !hasCallTargetNamed(value, save.ID, "save") {
		t.Fatal("this.save() call was not attributed to the class method")
	}
}

func TestIndexSyntaxIgnoresCallsInJavaScriptCommentsAndStrings(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "app.js"), "function run() {\n  // fakeCall()\n  const text = 'fakeCall()';\n}\n")
	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	run := syntaxGraphNode(t, value, "app.js", "run", graph.KindFunction)
	for _, edge := range value.Edges {
		if edge.From != run.ID || edge.Kind != graph.EdgeCalls {
			continue
		}
		if target := graphNodeByID(value, edge.To); target.Name == "fakeCall" {
			t.Fatal("comment or string content created a fakeCall edge")
		}
	}
}

func TestParseSyntaxPythonMethodsAndImportAliases(t *testing.T) {
	t.Parallel()
	source := "from package.worker import build as make_build\nimport package.tools as tools\n\n" +
		"class Builder:\n    def run(self):\n        make_build()\n        tools.clean()\n"
	facts := parseSyntaxTest(t, "python", "builder.py", source)
	assertSyntaxDeclaration(t, source, facts, "Builder", graph.KindType, "", 4, 7)
	assertSyntaxDeclaration(t, source, facts, "run", graph.KindMethod, "Builder", 5, 7)
	if !hasSyntaxImport(facts, "package.worker", "make_build", "build") {
		t.Errorf("imports = %#v, missing aliased from-import", facts.Imports)
	}
	if !hasSyntaxImport(facts, "package.tools", "tools", "") {
		t.Errorf("imports = %#v, missing module alias", facts.Imports)
	}
}

func TestParseSyntaxRustImplMethod(t *testing.T) {
	t.Parallel()
	facts := parseSyntaxTest(t, "rust", "lib.rs", "struct Store;\nimpl Store {\n    fn save(&self) {}\n}\n")
	assertSyntaxDeclaration(t, "struct Store;\nimpl Store {\n    fn save(&self) {}\n}\n", facts, "Store", graph.KindType, "", 1, 1)
	assertSyntaxDeclaration(t, "struct Store;\nimpl Store {\n    fn save(&self) {}\n}\n", facts, "save", graph.KindMethod, "Store", 3, 3)
}

func TestParseSyntaxJavaClassMethod(t *testing.T) {
	t.Parallel()
	facts := parseSyntaxTest(t, "java", "Store.java", "class Store {\n  void save() {}\n}\n")
	assertSyntaxDeclaration(t, "class Store {\n  void save() {}\n}\n", facts, "Store", graph.KindType, "", 1, 3)
	assertSyntaxDeclaration(t, "class Store {\n  void save() {}\n}\n", facts, "save", graph.KindMethod, "Store", 2, 2)
}

func TestParseSyntaxKotlinTopLevelAndClassMethod(t *testing.T) {
	t.Parallel()
	facts := parseSyntaxTest(t, "kotlin", "Store.kt", "fun create() {}\nclass Store {\n  fun save() {}\n}\n")
	assertSyntaxDeclaration(t, "fun create() {}\nclass Store {\n  fun save() {}\n}\n", facts, "create", graph.KindFunction, "", 1, 1)
	assertSyntaxDeclaration(t, "fun create() {}\nclass Store {\n  fun save() {}\n}\n", facts, "Store", graph.KindType, "", 2, 4)
	assertSyntaxDeclaration(t, "fun create() {}\nclass Store {\n  fun save() {}\n}\n", facts, "save", graph.KindMethod, "Store", 3, 3)
}

func TestParseSyntaxCSharpUsingAndMethod(t *testing.T) {
	t.Parallel()
	source := "using System.Collections;\nclass Store {\n  void Save() {}\n}\n"
	facts := parseSyntaxTest(t, "csharp", "Store.cs", source)
	assertSyntaxDeclaration(t, source, facts, "Store", graph.KindType, "", 2, 4)
	assertSyntaxDeclaration(t, source, facts, "Save", graph.KindMethod, "Store", 3, 3)
	if !hasSyntaxImport(facts, "System.Collections", "Collections", "Collections") {
		t.Errorf("imports = %#v, missing C# using directive", facts.Imports)
	}
}

func TestIndexSyntaxRecordsParseErrorInCoverage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "broken.ts"), "export function {")
	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if len(value.Coverage.SkippedFiles) != 1 {
		t.Fatalf("SkippedFiles = %#v, want one parse failure", value.Coverage.SkippedFiles)
	}
	skipped := value.Coverage.SkippedFiles[0]
	if skipped.File != "broken.ts" || !strings.Contains(skipped.Reason, "syntax") {
		t.Errorf("SkippedFiles[0] = %#v, want broken.ts with syntax error", skipped)
	}
}

func TestIndexSyntaxKeepsDeclarationsAroundRecoverableTypeScriptError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "recoverable.ts"), "export function keep() { return 1; }\nconst broken = ;\nexport class AlsoKeep {}\n")
	writeTestFile(t, filepath.Join(root, "malformed.ts"), "export function {\n")

	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	for _, name := range []string{"keep", "AlsoKeep"} {
		if !containsGraphNode(value, "recoverable.ts", name) {
			t.Errorf("recoverable declaration %q was not indexed", name)
		}
	}
	if containsGraphNode(value, "malformed.ts", "") {
		t.Fatal("malformed file unexpectedly produced a file node")
	}
	if len(value.Coverage.SkippedFiles) != 1 || value.Coverage.SkippedFiles[0].File != "malformed.ts" {
		t.Fatalf("SkippedFiles = %#v, want only malformed.ts", value.Coverage.SkippedFiles)
	}
}

func TestIndexSyntaxLinksConstructorsAndDynamicImports(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "main.ts"), "import { Widget } from './widget';\n"+
		"export const widget = new Widget();\n"+
		"import('./runtime');\n"+
		"require('./required');\n"+
		"type Alias = import('./types').WidgetType;\n")
	writeTestFile(t, filepath.Join(root, "widget.ts"), "export class Widget {}\n")
	writeTestFile(t, filepath.Join(root, "runtime.ts"), "export const ready = true;\n")
	writeTestFile(t, filepath.Join(root, "required.ts"), "export const ready = true;\n")
	writeTestFile(t, filepath.Join(root, "types.ts"), "export interface WidgetType {}\n")

	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	mainFile := syntaxGraphNode(t, value, "main.ts", "main.ts", graph.KindFile)
	widgetType := syntaxGraphNode(t, value, "widget.ts", "Widget", graph.KindType)
	assertGraphEdge(t, value, mainFile.ID, widgetType.ID, graph.EdgeCalls)
	for _, file := range []string{"widget.ts", "runtime.ts", "required.ts", "types.ts"} {
		target := syntaxGraphNode(t, value, file, filepath.Base(file), graph.KindFile)
		if !hasGraphEdge(value, mainFile.ID, target.ID, graph.EdgeImports) {
			t.Errorf("missing IMPORTS edge main.ts -> %s", file)
		}
	}
}

func TestIndexSyntaxCreatesTypeContainsMethodEdges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "service.ts"), "class Store { save() {} }\n")
	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	store := syntaxGraphNode(t, value, "service.ts", "Store", graph.KindType)
	save := syntaxGraphNode(t, value, "service.ts", "save", graph.KindMethod)
	assertGraphEdge(t, value, store.ID, save.ID, graph.EdgeContains)
}

func TestIndexSyntaxResolvesInjectedFieldCallToImportedMethod(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "controller.ts"), "import { Store } from './store';\n"+"class Controller {\n  private store: Store;\n  run() { this.store.save(); }\n}\n")
	writeTestFile(t, filepath.Join(root, "store.ts"), "export class Store { save() {} }\n")
	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	run := syntaxGraphNode(t, value, "controller.ts", "run", graph.KindMethod)
	save := syntaxGraphNode(t, value, "store.ts", "save", graph.KindMethod)
	assertGraphEdge(t, value, run.ID, save.ID, graph.EdgeCalls)
}

func TestIndexSyntaxRecognizesAdditionalTypeScriptAndPythonStubExtensions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "module.mts"), "export function create() { return 1; }\n")
	writeTestFile(t, filepath.Join(root, "legacy.cts"), "export function load() { return 1; }\n")
	writeTestFile(t, filepath.Join(root, "types.pyi"), "def build() -> int: ...\n")
	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	assertNoSyntaxSkips(t, value)
	if value.Coverage.IndexedFiles != 3 {
		t.Errorf("IndexedFiles = %d, want 3", value.Coverage.IndexedFiles)
	}
	if got := value.Coverage.IndexedByLanguage["typescript"]; got != 2 {
		t.Errorf("IndexedByLanguage[typescript] = %d, want 2", got)
	}
	if got := value.Coverage.IndexedByLanguage["python"]; got != 1 {
		t.Errorf("IndexedByLanguage[python] = %d, want 1", got)
	}
}

func parseSyntaxTest(t *testing.T, language, filename, source string) syntaxFacts {
	t.Helper()
	facts, err := parseSyntax(context.Background(), language, filename, []byte(source))
	if err != nil {
		t.Fatalf("parseSyntax(%q) error = %v", language, err)
	}
	return facts
}

func assertSyntaxDeclaration(t *testing.T, source string, facts syntaxFacts, name, kind, container string, startLine, endLine int) {
	t.Helper()
	for _, declaration := range facts.Declarations {
		if declaration.Name != name || declaration.Kind != kind || declaration.Container != container {
			continue
		}
		start := 1 + strings.Count(source[:declaration.StartByte], "\n")
		endOffset := declaration.EndByte - 1
		end := 1 + strings.Count(source[:endOffset], "\n")
		if start != startLine || end != endLine {
			t.Errorf("declaration %s range = %d-%d, want %d-%d", name, start, end, startLine, endLine)
		}
		return
	}
	t.Errorf("missing declaration name=%q kind=%q container=%q in %#v", name, kind, container, facts.Declarations)
}

func hasSyntaxImport(facts syntaxFacts, path, local, imported string) bool {
	for _, item := range facts.Imports {
		if item.Path == path && item.Local == local && item.Imported == imported {
			return true
		}
	}
	return false
}

func hasSyntaxCall(facts syntaxFacts, target string) bool {
	for _, call := range facts.Calls {
		if call.Target == target {
			return true
		}
	}
	return false
}

func syntaxGraphNode(t *testing.T, value *graph.Graph, file, name, kind string) graph.Node {
	t.Helper()
	for _, node := range value.Nodes {
		if node.File == file && node.Name == name && node.Kind == kind {
			return node
		}
	}
	t.Fatalf("missing graph node file=%q name=%q kind=%q", file, name, kind)
	return graph.Node{}
}

func assertGraphEdge(t *testing.T, value *graph.Graph, from, to, kind string) {
	t.Helper()
	for _, edge := range value.Edges {
		if edge.From == from && edge.To == to && edge.Kind == kind {
			return
		}
	}
	t.Errorf("missing graph edge %s -%s-> %s", from, kind, to)
}

func assertNoSyntaxSkips(t *testing.T, value *graph.Graph) {
	t.Helper()
	if len(value.Coverage.SkippedFiles) != 0 {
		t.Fatalf("SkippedFiles = %#v, want no syntax parse skips", value.Coverage.SkippedFiles)
	}
}

func hasGraphEdge(value *graph.Graph, from, to, kind string) bool {
	for _, edge := range value.Edges {
		if edge.From == from && edge.To == to && edge.Kind == kind {
			return true
		}
	}
	return false
}

func graphNodeByID(value *graph.Graph, id string) graph.Node {
	for _, node := range value.Nodes {
		if node.ID == id {
			return node
		}
	}
	return graph.Node{}
}

func hasCallTargetNamed(value *graph.Graph, sourceID, name string) bool {
	for _, edge := range value.Edges {
		if edge.From == sourceID && edge.Kind == graph.EdgeCalls && graphNodeByID(value, edge.To).Name == name {
			return true
		}
	}
	return false
}

func containsGraphNode(value *graph.Graph, file, name string) bool {
	for _, node := range value.Nodes {
		if node.File == file && (name == "" || node.Name == name) {
			return true
		}
	}
	return false
}
