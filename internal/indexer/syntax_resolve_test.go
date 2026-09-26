package indexer

import (
	"testing"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

const testModulePath = "example.com/mod"

func newResolverTestFile(rel, language string, facts syntaxFacts) *syntaxIndexedFile {
	return buildSyntaxIndexedFile(rel, language, testModulePath, make([]byte, 1<<16), facts)
}

func indexResolverFiles(files ...*syntaxIndexedFile) (byID, byPath map[string]*syntaxIndexedFile) {
	byID = make(map[string]*syntaxIndexedFile)
	byPath = make(map[string]*syntaxIndexedFile)
	for _, file := range files {
		byID[file.node.ID] = file
		byPath[file.rel] = file
	}
	return byID, byPath
}

func typeDecl(name string, interfaceType bool) syntaxDeclaration {
	return syntaxDeclaration{Name: name, Kind: graph.KindType, Interface: interfaceType, StartByte: 0, EndByte: 1000, Detail: "type"}
}

func methodDecl(name, container string, start, end uint32) syntaxDeclaration {
	return syntaxDeclaration{Name: name, Kind: graph.KindMethod, Container: container, StartByte: start, EndByte: end, Detail: "method"}
}

func funcDecl(name string, start, end uint32) syntaxDeclaration {
	return syntaxDeclaration{Name: name, Kind: graph.KindFunction, StartByte: start, EndByte: end, Detail: "func"}
}

func findDeclaration(file *syntaxIndexedFile, name, container string) syntaxIndexedDeclaration {
	for _, declaration := range file.declarations {
		if declaration.fact.Name == name && declaration.fact.Container == container {
			return declaration
		}
	}
	panic("declaration not found: " + container + "." + name)
}

func assertResolvesTo(t *testing.T, got []syntaxIndexedDeclaration, want syntaxIndexedDeclaration) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("resolution = %#v, want exactly one declaration %s", got, want.node.ID)
	}
	if got[0].node.ID != want.node.ID {
		t.Fatalf("resolved %s, want %s", got[0].node.ID, want.node.ID)
	}
}

func assertExternal(t *testing.T, got []syntaxIndexedDeclaration) {
	t.Helper()
	if len(got) != 0 {
		t.Fatalf("resolution = %#v, want External (no declaration)", got)
	}
}

func TestResolveSyntaxCallLocalTypedBindingResolves(t *testing.T) {
	t.Parallel()
	run := funcDecl("run", 0, 100)
	store := typeDecl("Store", false)
	save := methodDecl("save", "Store", 10, 20)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{run, store, save},
		Calls:        []syntaxCall{{Target: "x.save", StartByte: 50}},
		Bindings:     []syntaxBinding{{Field: "x", Type: "Store", Local: true, StartByte: 0, EndByte: 100}},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "x.save", StartByte: 50}, byID, byPath)
	assertResolvesTo(t, got, findDeclaration(file, "save", "Store"))
}

func TestResolveSyntaxCallInnerUntypedLocalShadowsOuterTyped(t *testing.T) {
	t.Parallel()
	run := funcDecl("run", 0, 100)
	store := typeDecl("Store", false)
	save := methodDecl("save", "Store", 10, 20)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{run, store, save},
		Bindings: []syntaxBinding{
			{Field: "x", Type: "Store", Local: true, StartByte: 0, EndByte: 100},
			{Field: "x", Type: "", Local: true, StartByte: 40, EndByte: 60},
		},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "x.save", StartByte: 45}, byID, byPath)
	assertExternal(t, got)
}

func TestResolveSyntaxCallLambdaParamShadow(t *testing.T) {
	t.Parallel()
	run := funcDecl("run", 0, 100)
	widget := typeDecl("Widget", false)
	open := methodDecl("open", "Widget", 10, 20)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{run, widget, open},
		Bindings: []syntaxBinding{
			{Field: "item", Type: "Widget", Local: true, StartByte: 0, EndByte: 100},
			{Field: "item", Type: "string", Local: true, StartByte: 30, EndByte: 70},
		},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "")
	insideLambda := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "item.open", StartByte: 50}, byID, byPath)
	assertExternal(t, insideLambda)
	outsideLambda := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "item.open", StartByte: 10}, byID, byPath)
	assertResolvesTo(t, outsideLambda, findDeclaration(file, "open", "Widget"))
}

func TestResolveSyntaxCallBindingOutsideSpanIgnored(t *testing.T) {
	t.Parallel()
	run := funcDecl("run", 0, 200)
	store := typeDecl("Store", false)
	save := methodDecl("save", "Store", 10, 20)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{run, store, save},
		Bindings:     []syntaxBinding{{Field: "x", Type: "Store", Local: true, StartByte: 0, EndByte: 50}},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "x.save", StartByte: 150}, byID, byPath)
	assertExternal(t, got)
}

func TestResolveSyntaxCallFieldBindingTwoTypesForSameFieldIsExternal(t *testing.T) {
	t.Parallel()
	controller := typeDecl("Controller", false)
	run := methodDecl("run", "Controller", 0, 100)
	storeType := typeDecl("Store", false)
	storeSave := methodDecl("save", "Store", 10, 20)
	cacheType := typeDecl("Cache", false)
	cacheSave := methodDecl("save", "Cache", 30, 40)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{controller, run, storeType, storeSave, cacheType, cacheSave},
		Bindings: []syntaxBinding{
			{Container: "Controller", Field: "repo", Type: "Store"},
			{Container: "Controller", Field: "repo", Type: "Cache"},
		},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "Controller")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "this.repo.save", StartByte: 50}, byID, byPath)
	assertExternal(t, got)
}

func TestResolveSyntaxCallFieldBindingsAgreeingResolves(t *testing.T) {
	t.Parallel()
	controller := typeDecl("Controller", false)
	run := methodDecl("run", "Controller", 0, 100)
	storeType := typeDecl("Store", false)
	storeSave := methodDecl("save", "Store", 10, 20)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{controller, run, storeType, storeSave},
		Bindings: []syntaxBinding{
			{Container: "Controller", Field: "repo", Type: "Store"},
			{Container: "Controller", Field: "repo", Type: "Store"},
		},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "Controller")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "this.repo.save", StartByte: 50}, byID, byPath)
	assertResolvesTo(t, got, findDeclaration(file, "save", "Store"))
}

func TestResolveSyntaxCallMultiSegmentQualifierSkipsFieldBinding(t *testing.T) {
	t.Parallel()
	controller := typeDecl("Controller", false)
	run := methodDecl("run", "Controller", 0, 100)
	storeType := typeDecl("Store", false)
	storeSave := methodDecl("save", "Store", 10, 20)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{controller, run, storeType, storeSave},
		Bindings: []syntaxBinding{
			{Container: "Controller", Field: "repo", Type: "Store"},
		},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "Controller")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "other.repo.save", StartByte: 50}, byID, byPath)
	assertExternal(t, got)
	got = resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "this.repo.extra.save", StartByte: 50}, byID, byPath)
	assertExternal(t, got)
}

func TestResolveSyntaxCallImplicitMemberForJava(t *testing.T) {
	t.Parallel()
	app := typeDecl("App", false)
	run := methodDecl("run", "App", 0, 100)
	storeType := typeDecl("Store", false)
	storeSave := methodDecl("save", "Store", 10, 20)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{app, run, storeType, storeSave},
		Bindings: []syntaxBinding{
			{Container: "App", Field: "store", Type: "Store"},
		},
	}
	file := newResolverTestFile("App.java", "java", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "App")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "store.save", StartByte: 50}, byID, byPath)
	assertResolvesTo(t, got, findDeclaration(file, "save", "Store"))
}

func TestResolveSyntaxCallImplicitMemberNotUsedForJavaScript(t *testing.T) {
	t.Parallel()
	app := typeDecl("App", false)
	run := methodDecl("run", "App", 0, 100)
	storeType := typeDecl("Store", false)
	storeSave := methodDecl("save", "Store", 10, 20)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{app, run, storeType, storeSave},
		Bindings: []syntaxBinding{
			{Container: "App", Field: "store", Type: "Store"},
		},
	}
	file := newResolverTestFile("app.js", "javascript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "App")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "store.save", StartByte: 50}, byID, byPath)
	assertExternal(t, got)
}

func TestResolveSyntaxCallInheritedMethodViaExtendsChain(t *testing.T) {
	t.Parallel()
	base := typeDecl("Base", false)
	baseSave := methodDecl("save", "Base", 10, 20)
	sub := typeDecl("Sub", false)
	app := typeDecl("App", false)
	run := methodDecl("run", "App", 0, 100)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{base, baseSave, sub, app, run},
		Heritage:     []syntaxHeritage{{Type: "Sub", Super: "Base", Kind: syntaxHeritageExtends}},
		Bindings:     []syntaxBinding{{Container: "App", Field: "item", Type: "Sub"}},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "App")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "this.item.save", StartByte: 50}, byID, byPath)
	assertResolvesTo(t, got, findDeclaration(file, "save", "Base"))
}

func TestResolveSyntaxCallThisMethodResolvedThroughSupertype(t *testing.T) {
	t.Parallel()
	base := typeDecl("Base", false)
	baseFoo := methodDecl("foo", "Base", 10, 20)
	sub := typeDecl("Sub", false)
	run := methodDecl("run", "Sub", 30, 90)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{base, baseFoo, sub, run},
		Heritage:     []syntaxHeritage{{Type: "Sub", Super: "Base", Kind: syntaxHeritageExtends}},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "Sub")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "this.foo", StartByte: 50}, byID, byPath)
	assertResolvesTo(t, got, findDeclaration(file, "foo", "Base"))
}

func TestResolveSyntaxCallAmbiguousAtNearestLevelIsExternal(t *testing.T) {
	t.Parallel()
	store := typeDecl("Store", false)
	saveOne := syntaxDeclaration{Name: "save", Kind: graph.KindMethod, Container: "Store", StartByte: 10, EndByte: 20, Detail: "save(int)"}
	saveTwo := syntaxDeclaration{Name: "save", Kind: graph.KindMethod, Container: "Store", StartByte: 20, EndByte: 30, Detail: "save(string)"}
	run := funcDecl("run", 0, 100)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{store, saveOne, saveTwo, run},
		Bindings:     []syntaxBinding{{Field: "x", Type: "Store", Local: true, StartByte: 0, EndByte: 100}},
	}
	file := newResolverTestFile("Store.java", "java", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "x.save", StartByte: 50}, byID, byPath)
	assertExternal(t, got)
}

func TestResolveSyntaxCallCycleInHeritageTerminates(t *testing.T) {
	t.Parallel()
	a := typeDecl("A", false)
	b := typeDecl("B", false)
	run := methodDecl("run", "A", 0, 100)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{a, b, run},
		Heritage: []syntaxHeritage{
			{Type: "A", Super: "B", Kind: syntaxHeritageExtends},
			{Type: "B", Super: "A", Kind: syntaxHeritageExtends},
		},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "A")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "this.missing", StartByte: 50}, byID, byPath)
	assertExternal(t, got)
}

func TestResolveSyntaxCallQualifiedSuperNotMatchedToSameFileType(t *testing.T) {
	t.Parallel()
	sameFileC := typeDecl("C", false)
	sub := typeDecl("Sub", false)
	run := methodDecl("run", "Sub", 0, 100)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{sameFileC, sub, run},
		Heritage:     []syntaxHeritage{{Type: "Sub", Super: "a.b.C", Kind: syntaxHeritageExtends}},
	}
	file := newResolverTestFile("Sub.java", "java", facts)
	byID, byPath := indexResolverFiles(file)
	got := resolveNormalizedType(file, "a.b.C", byID, byPath)
	if len(got) != 0 {
		t.Fatalf("resolveNormalizedType(a.b.C) = %#v, want no match against same-file C", got)
	}
}

func TestResolveSyntaxTypeJavaImportPathMapping(t *testing.T) {
	t.Parallel()
	base := typeDecl("Base", false)
	baseFile := newResolverTestFile("com/example/Base.java", "java", syntaxFacts{Declarations: []syntaxDeclaration{base}})

	sub := typeDecl("Sub", false)
	run := methodDecl("run", "Sub", 30, 90)
	appFacts := syntaxFacts{
		Declarations: []syntaxDeclaration{sub, run},
		Imports:      []syntaxImport{{Path: "com.example.Base", Local: "Base", Imported: "Base"}},
		Heritage:     []syntaxHeritage{{Type: "Sub", Super: "Base", Kind: syntaxHeritageExtends}},
	}
	appFile := newResolverTestFile("com/example/Sub.java", "java", appFacts)

	byID, byPath := indexResolverFiles(baseFile, appFile)
	target := resolveSyntaxImport(appFile, "com.example.Base", byPath)
	if target == nil || target.rel != "com/example/Base.java" {
		t.Fatalf("resolveSyntaxImport(com.example.Base) = %#v, want com/example/Base.java", target)
	}
	appFile.importFiles["Base"] = target.node.ID

	got := resolveNormalizedType(appFile, "Base", byID, byPath)
	assertResolvesTo(t, got, findDeclaration(baseFile, "Base", ""))
}

func TestResolveSyntaxTypeSameDirectoryFallbackUniqueAndAmbiguous(t *testing.T) {
	t.Parallel()
	helper := typeDecl("Helper", false)
	helperFile := newResolverTestFile("pkg/Helper.java", "java", syntaxFacts{Declarations: []syntaxDeclaration{helper}})
	appFile := newResolverTestFile("pkg/App.java", "java", syntaxFacts{})
	byID, byPath := indexResolverFiles(helperFile, appFile)
	got := resolveSyntaxType(appFile, "Helper", byID, byPath)
	assertResolvesTo(t, got, findDeclaration(helperFile, "Helper", ""))

	otherHelper := typeDecl("Helper", false)
	otherHelperFile := newResolverTestFile("pkg/OtherHelper.java", "java", syntaxFacts{Declarations: []syntaxDeclaration{otherHelper}})
	byID2, byPath2 := indexResolverFiles(helperFile, otherHelperFile, appFile)
	ambiguous := resolveSyntaxType(appFile, "Helper", byID2, byPath2)
	assertExternal(t, ambiguous)
}

func TestClassifyHeritageEdge(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		kind            string
		sourceInterface bool
		targetInterface bool
		want            string
	}{
		{"implements always implements", syntaxHeritageImplements, false, false, graph.EdgeImplements},
		{"extends class to class", syntaxHeritageExtends, false, false, graph.EdgeExtends},
		{"extends class to interface reroutes to implements", syntaxHeritageExtends, false, true, graph.EdgeImplements},
		{"interface extends interface keeps extends", syntaxHeritageExtends, true, true, graph.EdgeExtends},
		{"unknown kind to interface implies implements", "", false, true, graph.EdgeImplements},
		{"unknown kind to class implies extends", "", false, false, graph.EdgeExtends},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyHeritageEdge(tc.kind, tc.sourceInterface, tc.targetInterface)
			if got != tc.want {
				t.Errorf("classifyHeritageEdge(%q, %v, %v) = %q, want %q", tc.kind, tc.sourceInterface, tc.targetInterface, got, tc.want)
			}
		})
	}
}

func TestResolveSyntaxCallPythonConstructorEdge(t *testing.T) {
	t.Parallel()
	foo := typeDecl("Foo", false)
	run := funcDecl("run", 0, 100)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{foo, run},
	}
	file := newResolverTestFile("app.py", "python", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "Foo", StartByte: 50}, byID, byPath)
	assertResolvesTo(t, got, findDeclaration(file, "Foo", ""))
}

func TestResolveSyntaxCallRustImplicitMemberNotUsed(t *testing.T) {
	t.Parallel()
	worker := typeDecl("Worker", false)
	run := methodDecl("run", "Worker", 0, 100)
	cache := typeDecl("Cache", false)
	save := methodDecl("save", "Cache", 10, 20)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{worker, run, cache, save},
		Bindings: []syntaxBinding{
			{Container: "Worker", Field: "store", Type: "Cache"},
		},
	}
	file := newResolverTestFile("worker.rs", "rust", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "Worker")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "store.save", StartByte: 50}, byID, byPath)
	assertExternal(t, got)
}

func TestResolveSyntaxCallThisNestedContainerNotSameDirectoryFallback(t *testing.T) {
	t.Parallel()
	outer := typeDecl("Outer", false)
	builder := syntaxDeclaration{Name: "Builder", Kind: graph.KindType, Container: "Outer", StartByte: 0, EndByte: 500, Detail: "type"}
	build := methodDecl("build", "Builder", 100, 200)
	assemble := methodDecl("assemble", "Builder", 250, 400)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{outer, builder, build, assemble},
	}
	file := newResolverTestFile("pkg/Outer.java", "java", facts)

	siblingBuilder := typeDecl("Builder", false)
	siblingBuild := methodDecl("build", "Builder", 10, 20)
	siblingFile := newResolverTestFile("pkg/Builder.java", "java", syntaxFacts{Declarations: []syntaxDeclaration{siblingBuilder, siblingBuild}})

	byID, byPath := indexResolverFiles(file, siblingFile)
	assembleDecl := findDeclaration(file, "assemble", "Builder")
	got := resolveSyntaxCall(file, assembleDecl.fact, syntaxCall{Target: "this.build", StartByte: 300}, byID, byPath)
	assertResolvesTo(t, got, findDeclaration(file, "build", "Builder"))
}

func TestResolveSyntaxTypeExplicitImportShadowsSameDirectoryFallback(t *testing.T) {
	t.Parallel()
	store := typeDecl("Store", false)
	siblingFile := newResolverTestFile("pkg/Store.java", "java", syntaxFacts{Declarations: []syntaxDeclaration{store}})

	appFacts := syntaxFacts{
		Imports: []syntaxImport{{Path: "com.ext.Store", Local: "Store", Imported: "Store"}},
	}
	appFile := newResolverTestFile("pkg/App.java", "java", appFacts)

	byID, byPath := indexResolverFiles(siblingFile, appFile)
	got := resolveSyntaxType(appFile, "Store", byID, byPath)
	assertExternal(t, got)
}

func TestResolveRustCratePathScopedToCallerCrate(t *testing.T) {
	t.Parallel()
	rootPool := typeDecl("Pool", false)
	rootDb := newResolverTestFile("src/db.rs", "rust", syntaxFacts{Declarations: []syntaxDeclaration{rootPool}})

	poolA := typeDecl("Pool", false)
	dbA := newResolverTestFile("crates/a/src/db.rs", "rust", syntaxFacts{Declarations: []syntaxDeclaration{poolA}})

	poolB := typeDecl("Pool", false)
	dbB := newResolverTestFile("crates/b/src/db.rs", "rust", syntaxFacts{Declarations: []syntaxDeclaration{poolB}})

	callerA := newResolverTestFile("crates/a/src/lib.rs", "rust", syntaxFacts{})
	callerB := newResolverTestFile("crates/b/src/lib.rs", "rust", syntaxFacts{})

	_, byPath := indexResolverFiles(rootDb, dbA, dbB, callerA, callerB)

	gotA := resolveRustCratePath(callerA, "crate::db::Pool", byPath)
	assertResolvesTo(t, gotA, findDeclaration(dbA, "Pool", ""))

	gotB := resolveRustCratePath(callerB, "crate::db::Pool", byPath)
	assertResolvesTo(t, gotB, findDeclaration(dbB, "Pool", ""))
}

func TestResolveSyntaxCallLocalReassignmentDifferentTypesIsExternal(t *testing.T) {
	t.Parallel()
	run := funcDecl("run", 0, 200)
	typeA := typeDecl("A", false)
	saveA := methodDecl("save", "A", 10, 20)
	typeB := typeDecl("B", false)
	saveB := methodDecl("save", "B", 30, 40)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{run, typeA, saveA, typeB, saveB},
		Bindings: []syntaxBinding{
			{Field: "x", Type: "A", Local: true, StartByte: 5, EndByte: 200},
			{Field: "x", Type: "B", Local: true, StartByte: 100, EndByte: 200},
		},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "x.save", StartByte: 150}, byID, byPath)
	assertExternal(t, got)
}

func TestResolveSyntaxCallLocalReassignmentSameTypeResolves(t *testing.T) {
	t.Parallel()
	run := funcDecl("run", 0, 200)
	typeA := typeDecl("A", false)
	saveA := methodDecl("save", "A", 10, 20)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{run, typeA, saveA},
		Bindings: []syntaxBinding{
			{Field: "x", Type: "A", Local: true, StartByte: 5, EndByte: 200},
			{Field: "x", Type: "A", Local: true, StartByte: 100, EndByte: 200},
		},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "x.save", StartByte: 150}, byID, byPath)
	assertResolvesTo(t, got, findDeclaration(file, "save", "A"))
}

func TestResolveSyntaxCallPythonConditionalReassignmentIsExternal(t *testing.T) {
	t.Parallel()
	run := funcDecl("run", 0, 200)
	typeA := typeDecl("A", false)
	saveA := methodDecl("save", "A", 10, 20)
	typeB := typeDecl("B", false)
	saveB := methodDecl("save", "B", 30, 40)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{run, typeA, saveA, typeB, saveB},
		Bindings: []syntaxBinding{
			{Field: "x", Type: "A", Local: true, StartByte: 5, EndByte: 200},
			{Field: "x", Type: "B", Local: true, StartByte: 100, EndByte: 200},
		},
	}
	file := newResolverTestFile("app.py", "python", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "x.save", StartByte: 150}, byID, byPath)
	assertExternal(t, got)
}

func TestResolveSyntaxCallFieldBindingUntypedInitializerIsExternal(t *testing.T) {
	t.Parallel()
	controller := typeDecl("Controller", false)
	run := methodDecl("run", "Controller", 0, 100)
	storeType := typeDecl("Store", false)
	storeSave := methodDecl("save", "Store", 10, 20)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{controller, run, storeType, storeSave},
		Bindings: []syntaxBinding{
			{Container: "Controller", Field: "repo", Type: "Store"},
			{Container: "Controller", Field: "repo", Type: ""},
		},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "Controller")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "this.repo.save", StartByte: 50}, byID, byPath)
	assertExternal(t, got)
}

func TestTypeDeclarationForContainerJavaInnerClassMethodResolvesContains(t *testing.T) {
	t.Parallel()
	source := `class Base {
}

class Outer {
    class Inner extends Base {
        void run() {
        }
    }
}
`
	facts := parseSyntaxTest(t, "java", "Outer.java", source)
	file := newResolverTestFile("Outer.java", "java", facts)

	innerType := findDeclaration(file, "Inner", "Outer")
	runMethod := findDeclaration(file, "run", "Inner")

	got := typeDeclarationForContainer(file, runMethod.fact.Container, &runMethod.fact)
	if got == nil || got.node.ID != innerType.node.ID {
		t.Fatalf("typeDeclarationForContainer(%q) = %#v, want Inner type declaration %s", runMethod.fact.Container, got, innerType.node.ID)
	}
}

func TestTypeDeclarationForContainerJavaInnerClassExtendsResolvesSourceType(t *testing.T) {
	t.Parallel()
	source := `class Base {
}

class Outer {
    class Inner extends Base {
        void run() {
        }
    }
}
`
	facts := parseSyntaxTest(t, "java", "Outer.java", source)
	file := newResolverTestFile("Outer.java", "java", facts)
	byID, byPath := indexResolverFiles(file)

	innerType := findDeclaration(file, "Inner", "Outer")
	baseType := findDeclaration(file, "Base", "")

	var heritage syntaxHeritage
	found := false
	for _, entry := range facts.Heritage {
		if entry.Super == "Base" {
			heritage, found = entry, true
			break
		}
	}
	if !found {
		t.Fatalf("no heritage entry extending Base found in %#v", facts.Heritage)
	}

	resolvedSource := typeDeclarationForContainer(file, heritage.Type, nil)
	if resolvedSource == nil || resolvedSource.node.ID != innerType.node.ID {
		t.Fatalf("typeDeclarationForContainer(%q) = %#v, want Inner type declaration %s", heritage.Type, resolvedSource, innerType.node.ID)
	}

	targets := resolveNormalizedType(file, heritage.Super, byID, byPath)
	if len(targets) != 1 || targets[0].node.ID != baseType.node.ID {
		t.Fatalf("resolveNormalizedType(%q) = %#v, want Base type declaration %s", heritage.Super, targets, baseType.node.ID)
	}
}

func TestTypeDeclarationForContainerTSClassNestedInFunctionResolvesContainsAndExtends(t *testing.T) {
	t.Parallel()
	source := `class Base {
}

function outer() {
  class Inner extends Base {
    run() {
    }
  }
}
`
	facts := parseSyntaxTest(t, "typescript", "app.ts", source)
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)

	innerType := findDeclaration(file, "Inner", "outer")
	runMethod := findDeclaration(file, "run", "outer.Inner")
	baseType := findDeclaration(file, "Base", "")

	got := typeDeclarationForContainer(file, runMethod.fact.Container, &runMethod.fact)
	if got == nil || got.node.ID != innerType.node.ID {
		t.Fatalf("typeDeclarationForContainer(%q) = %#v, want Inner type declaration %s", runMethod.fact.Container, got, innerType.node.ID)
	}

	var heritage syntaxHeritage
	found := false
	for _, entry := range facts.Heritage {
		if entry.Super == "Base" {
			heritage, found = entry, true
			break
		}
	}
	if !found {
		t.Fatalf("no heritage entry extending Base found in %#v", facts.Heritage)
	}

	resolvedSource := typeDeclarationForContainer(file, heritage.Type, nil)
	if resolvedSource == nil || resolvedSource.node.ID != innerType.node.ID {
		t.Fatalf("typeDeclarationForContainer(%q) = %#v, want Inner type declaration %s", heritage.Type, resolvedSource, innerType.node.ID)
	}

	targets := resolveNormalizedType(file, heritage.Super, byID, byPath)
	if len(targets) != 1 || targets[0].node.ID != baseType.node.ID {
		t.Fatalf("resolveNormalizedType(%q) = %#v, want Base type declaration %s", heritage.Super, targets, baseType.node.ID)
	}
}

func TestTypeDeclarationForContainerAmbiguousStaticNestedNamesIsNoEdge(t *testing.T) {
	t.Parallel()
	outerA := typeDecl("OuterA", false)
	innerA := syntaxDeclaration{Name: "Inner", Kind: graph.KindType, Container: "OuterA", StartByte: 0, EndByte: 100, Detail: "type"}
	outerB := typeDecl("OuterB", false)
	innerB := syntaxDeclaration{Name: "Inner", Kind: graph.KindType, Container: "OuterB", StartByte: 200, EndByte: 300, Detail: "type"}
	facts := syntaxFacts{Declarations: []syntaxDeclaration{outerA, innerA, outerB, innerB}}
	file := newResolverTestFile("Dup.java", "java", facts)

	got := typeDeclarationForContainer(file, "Inner", nil)
	if got != nil {
		t.Fatalf("typeDeclarationForContainer(Inner) = %#v, want nil (ambiguous simple name across two outers, no member position)", got)
	}
}

// L2: a top-level type and a nested type of the same simple name must not be
// conflated. typeDeclarationForContainer previously matched c against a
// top-level (Container "") declaration first, so a member declared inside
// the nested type wrongly resolved to the unrelated top-level type of the
// same name.
func TestTypeDeclarationForContainerJavaTopLevelAndNestedSameNameResolvesNested(t *testing.T) {
	t.Parallel()
	source := `class Config {
}

class Outer {
    static class Config {
        void run() {
        }
    }
}
`
	facts := parseSyntaxTest(t, "java", "Outer.java", source)
	file := newResolverTestFile("Outer.java", "java", facts)

	topLevelConfig := findDeclaration(file, "Config", "")
	nestedConfig := findDeclaration(file, "Config", "Outer")
	runMethod := findDeclaration(file, "run", "Config")

	got := typeDeclarationForContainer(file, runMethod.fact.Container, &runMethod.fact)
	if got == nil || got.node.ID != nestedConfig.node.ID {
		t.Fatalf("typeDeclarationForContainer(%q) = %#v, want nested Config type declaration %s (top-level was %s)",
			runMethod.fact.Container, got, nestedConfig.node.ID, topLevelConfig.node.ID)
	}
}

// L2: a Rust impl block sits outside the struct body it extends, so its
// methods' byte positions are never enclosed by any struct declaration. When
// more than one struct in the file shares the impl's simple name (here, a
// top-level Config and a nested tests::Config), the facts available to the
// resolver cannot tell which one the impl belongs to, so the CONTAINS edge
// must resolve to nil rather than guessing the wrong (e.g. top-level) type.
func TestTypeDeclarationForContainerRustImplOutsideStructAmbiguousIsNoEdge(t *testing.T) {
	t.Parallel()
	source := `struct Config {
}

mod tests {
    struct Config;

    impl Config {
        fn new() {
        }
    }
}
`
	facts := parseSyntaxTest(t, "rust", "lib.rs", source)
	file := newResolverTestFile("lib.rs", "rust", facts)

	newMethod := findDeclaration(file, "new", "Config")

	got := typeDeclarationForContainer(file, newMethod.fact.Container, &newMethod.fact)
	if got != nil {
		t.Fatalf("typeDeclarationForContainer(%q) = %#v, want nil (Rust impl body never encloses its methods, and two Config structs exist)", newMethod.fact.Container, got)
	}
}

func TestResolveSyntaxCallOverloadIsExternal(t *testing.T) {
	t.Parallel()
	app := typeDecl("App", false)
	saveOne := syntaxDeclaration{Name: "save", Kind: graph.KindMethod, Container: "App", StartByte: 10, EndByte: 20, Detail: "save(int)"}
	saveTwo := syntaxDeclaration{Name: "save", Kind: graph.KindMethod, Container: "App", StartByte: 20, EndByte: 30, Detail: "save(string)"}
	run := methodDecl("run", "App", 40, 90)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{app, saveOne, saveTwo, run},
	}
	file := newResolverTestFile("App.java", "java", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "App")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "this.save", StartByte: 60}, byID, byPath)
	assertExternal(t, got)
}

// Base is declared at top level and Inner is nested inside outer() so that
// Inner's own Container ("outer.Inner") is a dotted scope chain, reproducing
// the defect where resolveOwnContainerMethod/superMethodsOfType looked up
// the caller's own type by simple name (typeDeclarationNamed) instead of by
// the dotted-chain-aware typeDeclarationForContainer. Base is intentionally
// kept top level: resolveNormalizedType (used to resolve a heritage Super
// reference such as "Base") only matches simple type names declared at file
// top level or via imports/same-directory fallback; it has no lexical/
// sibling-scope lookup, so a Base nested in the same outer() as Inner would
// not resolve regardless of this fix. That gap is a separate, pre-existing
// defect (it also affects the main EXTENDS/IMPLEMENTS edge-building loop)
// and is out of scope here.
func TestResolveSyntaxCallThisMethodThroughNestedContainerHeritageChain(t *testing.T) {
	t.Parallel()
	source := `class Base {
  helper() {
  }
}

function outer() {
  class Inner extends Base {
    run() {
      this.helper()
    }
  }
}
`
	facts := parseSyntaxTest(t, "typescript", "app.ts", source)
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)

	runDecl := findDeclaration(file, "run", "outer.Inner")
	helperDecl := findDeclaration(file, "helper", "Base")

	got := resolveOwnContainerMethod(file, runDecl.fact, "helper", byID, byPath)
	assertResolvesTo(t, got, helperDecl)
}

func TestResolveSyntaxCallSuperMethodThroughNestedContainerHeritageChain(t *testing.T) {
	t.Parallel()
	source := `class Base {
  helper() {
  }
}

function outer() {
  class Inner extends Base {
    run() {
      super.helper()
    }
  }
}
`
	facts := parseSyntaxTest(t, "typescript", "app.ts", source)
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)

	runDecl := findDeclaration(file, "run", "outer.Inner")
	helperDecl := findDeclaration(file, "helper", "Base")

	got := superMethodsOfType(file, runDecl.fact, "helper", byID, byPath)
	assertResolvesTo(t, got, helperDecl)
}

func TestResolveSyntaxCallPythonSelfMethodThroughNestedContainerHeritageChain(t *testing.T) {
	t.Parallel()
	source := `class Base:
    def helper(self):
        pass

def outer():
    class Inner(Base):
        def run(self):
            self.helper()
`
	facts := parseSyntaxTest(t, "python", "app.py", source)
	file := newResolverTestFile("app.py", "python", facts)
	byID, byPath := indexResolverFiles(file)

	runDecl := findDeclaration(file, "run", "outer.Inner")
	helperDecl := findDeclaration(file, "helper", "Base")

	got := resolveOwnContainerMethod(file, runDecl.fact, "helper", byID, byPath)
	assertResolvesTo(t, got, helperDecl)
}

// M1: localBindingTypeAmbiguous previously restricted its scan for
// conflicting reassignments to bindings that start within the calling
// declaration's own byte span. A call made from a nested closure has a
// caller (the closure itself) whose span starts after the reassignment it
// needs to see, so the restriction hid the reassignment entirely and let the
// call wrongly resolve through whichever binding innermostLocalBinding chose
// as if it were the only one. Removing the restriction makes the ambiguity
// visible regardless of where in the file the reassignment sits, as long as
// it shares the same declaring binding's EndByte.
func TestResolveSyntaxCallLocalReassignmentVisibleFromNestedClosureIsExternal(t *testing.T) {
	t.Parallel()
	defaultType := typeDecl("Default", false)
	defaultExec := methodDecl("exec", "Default", 400, 410)
	customType := typeDecl("Custom", false)
	customExec := methodDecl("exec", "Custom", 420, 430)
	inner := funcDecl("inner", 210, 300)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{defaultType, defaultExec, customType, customExec, inner},
		Bindings: []syntaxBinding{
			// s = new Default(), declared before inner()'s own span begins.
			{Field: "s", Type: "Default", Local: true, StartByte: 10, EndByte: 300},
			// Conditional reassignment (e.g. `if (f) s = new Custom();`),
			// also textually before inner()'s own span begins, but sharing
			// the same declaring scope end (300) as the binding above.
			{Field: "s", Type: "Custom", Local: true, StartByte: 50, EndByte: 300},
		},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	innerDecl := findDeclaration(file, "inner", "")
	// The call to s.exec() lives inside inner(), at byte 250.
	got := resolveSyntaxCall(file, innerDecl.fact, syntaxCall{Target: "s.exec", StartByte: 250}, byID, byPath)
	assertExternal(t, got)
}

// M1: the module-level counterpart of the closure case above — a variable
// declared at module scope, reassigned to a different type inside one
// function, and read inside a different function entirely. Neither the
// reassignment nor the original declaration lies within the reading
// function's own span, so the old caller-restricted scan missed the
// ambiguity there too.
func TestResolveSyntaxCallModuleLevelReassignmentAcrossFunctionsIsExternal(t *testing.T) {
	t.Parallel()
	typeA := typeDecl("A", false)
	execA := methodDecl("exec", "A", 500, 510)
	typeB := typeDecl("B", false)
	execB := methodDecl("exec", "B", 520, 530)
	g := funcDecl("g", 300, 400)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{typeA, execA, typeB, execB, g},
		Bindings: []syntaxBinding{
			// let x = new A(); at module scope, scoped to the whole file.
			{Field: "x", Type: "A", Local: true, StartByte: 5, EndByte: 1000},
			// x = new B(); inside a different function f(), also scoped to
			// the whole file (module-level scope), sharing EndByte with the
			// declaration above.
			{Field: "x", Type: "B", Local: true, StartByte: 100, EndByte: 1000},
		},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	gDecl := findDeclaration(file, "g", "")
	// The call to x.exec() lives inside g(), at byte 350 — a different
	// function from the one that reassigned x.
	got := resolveSyntaxCall(file, gDecl.fact, syntaxCall{Target: "x.exec", StartByte: 350}, byID, byPath)
	assertExternal(t, got)
}

// M1 end-to-end: confirms the dynamic extractor (syntax_dynamic.go) gives a
// reassignment inside a nested function the declaring scope's EndByte, so
// the resolver-level fix above actually fires against real parsed source,
// not only hand-built facts.
func TestResolveSyntaxCallClosureCapturesConditionallyReassignedVariableIsExternal(t *testing.T) {
	t.Parallel()
	source := `class Default {
  exec() {
  }
}

class Custom {
  exec() {
  }
}

function outer() {
  let s = new Default();
  if (cond) {
    s = new Custom();
  }
  function inner() {
    s.exec();
  }
}
`
	facts := parseSyntaxTest(t, "typescript", "app.ts", source)
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)

	innerDecl := findDeclaration(file, "inner", "outer")
	var call syntaxCall
	found := false
	for _, c := range facts.Calls {
		if c.Target == "s.exec" {
			call, found = c, true
			break
		}
	}
	if !found {
		t.Fatalf("no s.exec call found in %#v", facts.Calls)
	}

	got := resolveSyntaxCall(file, innerDecl.fact, call, byID, byPath)
	assertExternal(t, got)
}

// L3: a Rust file with no "src" ancestor directory at all (crates/a/tests/,
// benches/, examples/) has no discoverable crate root, so rustCrateRoot must
// report that as unresolvable ("") rather than falling back to a bare "src"
// that could coincidentally match an unrelated repo-root src/ tree.
func TestRustCrateRootReturnsEmptyWithoutSrcAncestor(t *testing.T) {
	t.Parallel()
	cases := []string{
		"crates/a/tests/x.rs",
		"crates/a/benches/bench.rs",
		"crates/a/examples/demo.rs",
		"lib.rs",
	}
	for _, rel := range cases {
		if got := rustCrateRoot(rel); got != "" {
			t.Errorf("rustCrateRoot(%q) = %q, want empty (no src ancestor)", rel, got)
		}
	}
}

// L3: a crate::-qualified reference from a file with no src ancestor
// (crates/a/tests/x.rs) must not resolve against an unrelated repo-root
// src/db.rs, even when that file happens to declare a type of the right
// name.
func TestResolveRustCratePathNoSrcAncestorIsUnresolvable(t *testing.T) {
	t.Parallel()
	rootPool := typeDecl("Pool", false)
	rootDb := newResolverTestFile("src/db.rs", "rust", syntaxFacts{Declarations: []syntaxDeclaration{rootPool}})
	callerTests := newResolverTestFile("crates/a/tests/x.rs", "rust", syntaxFacts{})
	_, byPath := indexResolverFiles(rootDb, callerTests)

	got := resolveRustCratePath(callerTests, "crate::db::Pool", byPath)
	assertExternal(t, got)
}

// M2: localBindingTypeAmbiguous previously paired bindings of the same name
// by EndByte alone, which is not a scope identity: a Python `def inner()`
// nested as the last statement of `def outer()` ends on the exact same byte
// as outer() itself. A binding local to inner() (here modelling `c = Other()`
// assigned inside inner) then wrongly looked like a reassignment of outer's
// own `c = Client()` binding, even though the call to c.send() sits inside
// outer(), before inner() is even declared, and the two bindings live in
// unrelated scopes that merely happen to close on the same byte. Pairing on
// (ScopeStart, EndByte) distinguishes them and lets outer's own call resolve.
func TestResolveSyntaxCallOuterBindingResolvesWhenInnerBindingSharesEndByteDifferentScope(t *testing.T) {
	t.Parallel()
	outer := funcDecl("outer", 0, 500)
	client := typeDecl("Client", false)
	clientSend := methodDecl("send", "Client", 10, 20)
	other := typeDecl("Other", false)
	otherSend := methodDecl("send", "Other", 30, 40)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{outer, client, clientSend, other, otherSend},
		Bindings: []syntaxBinding{
			// c = Client(), local to outer's own scope [0, 500).
			{Field: "c", Type: "Client", Local: true, StartByte: 50, EndByte: 500, ScopeStart: 0},
			// c = Other() inside a nested inner() that is the last statement of
			// outer(), so inner's own scope also ends at byte 500 — the same
			// EndByte as outer's binding above — despite starting at a wholly
			// different scope (byte 300, inner's own declaration).
			{Field: "c", Type: "Other", Local: true, StartByte: 350, EndByte: 500, ScopeStart: 300},
		},
	}
	file := newResolverTestFile("app.py", "python", facts)
	byID, byPath := indexResolverFiles(file)
	outerDecl := findDeclaration(file, "outer", "")
	got := resolveSyntaxCall(file, outerDecl.fact, syntaxCall{Target: "c.send", StartByte: 100}, byID, byPath)
	assertResolvesTo(t, got, findDeclaration(file, "send", "Client"))
}

// M2: the module-level counterpart of the above with a genuine same-scope
// reassignment: two bindings sharing both ScopeStart and EndByte (not just
// EndByte) are the same variable reassigned to a different type, so the call
// must still resolve to External.
func TestResolveSyntaxCallSameScopeReassignmentDifferentTypesStillExternal(t *testing.T) {
	t.Parallel()
	run := funcDecl("run", 0, 200)
	typeA := typeDecl("A", false)
	saveA := methodDecl("save", "A", 10, 20)
	typeB := typeDecl("B", false)
	saveB := methodDecl("save", "B", 30, 40)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{run, typeA, saveA, typeB, saveB},
		Bindings: []syntaxBinding{
			{Field: "x", Type: "A", Local: true, StartByte: 5, EndByte: 200, ScopeStart: 0},
			{Field: "x", Type: "B", Local: true, StartByte: 100, EndByte: 200, ScopeStart: 0},
		},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "x.save", StartByte: 150}, byID, byPath)
	assertExternal(t, got)
}

// M2: the JS counterpart — `(s: A) => () => { let s = new B(); s.run() }`.
// The outer arrow's own body is the inner arrow expression with no braces of
// its own, so the outer parameter's scope and the inner arrow's block both
// end on the same closing `}`. `let s = new B()` inside the inner block is a
// fresh shadow of the outer parameter, not a reassignment, so the call to
// s.run() inside the inner block must resolve to the inner shadow's own type
// (B), not be wrongly flagged ambiguous against the outer parameter (A) just
// because their scopes happen to close on the same byte.
func TestResolveSyntaxCallJSNestedArrowShadowSameEndByteDifferentScopeResolvesInnerShadow(t *testing.T) {
	t.Parallel()
	run := funcDecl("run", 0, 200)
	typeA := typeDecl("A", false)
	runA := methodDecl("run", "A", 10, 20)
	typeB := typeDecl("B", false)
	runB := methodDecl("run", "B", 30, 40)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{run, typeA, runA, typeB, runB},
		Bindings: []syntaxBinding{
			// (s: A) => ...: the outer arrow's own parameter, scoped over its
			// whole body.
			{Field: "s", Type: "A", Local: true, StartByte: 1, EndByte: 200, ScopeStart: 0},
			// let s = new B() inside the inner arrow's own block: a fresh shadow,
			// whose block happens to close on the same byte as the outer arrow's
			// own scope.
			{Field: "s", Type: "B", Local: true, StartByte: 150, EndByte: 200, ScopeStart: 100},
		},
	}
	file := newResolverTestFile("app.ts", "typescript", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "s.run", StartByte: 180}, byID, byPath)
	assertResolvesTo(t, got, findDeclaration(file, "run", "B"))
}

// N1: resolveOwnContainerMethod's doc comment previously claimed it also
// handles a bare `method()` call (no receiver) on the enclosing type for
// implicit-member languages (Java/Kotlin/C#). It does not: resolveSyntaxCall
// never routes a qualifier-less call through resolveOwnContainerMethod, so a
// bare call to an inherited method (declared only on a supertype, not the
// caller's own container) stays unresolved rather than walking the heritage
// chain. This locks in that (now accurately documented) behaviour.
func TestResolveSyntaxCallBareMethodDoesNotWalkHeritageChain(t *testing.T) {
	t.Parallel()
	base := typeDecl("Base", false)
	baseHelper := methodDecl("helper", "Base", 10, 20)
	sub := typeDecl("Sub", false)
	run := methodDecl("run", "Sub", 30, 90)
	facts := syntaxFacts{
		Declarations: []syntaxDeclaration{base, baseHelper, sub, run},
		Heritage:     []syntaxHeritage{{Type: "Sub", Super: "Base", Kind: syntaxHeritageExtends}},
	}
	file := newResolverTestFile("Sub.java", "java", facts)
	byID, byPath := indexResolverFiles(file)
	runDecl := findDeclaration(file, "run", "Sub")
	got := resolveSyntaxCall(file, runDecl.fact, syntaxCall{Target: "helper", StartByte: 50}, byID, byPath)
	assertExternal(t, got)
}
