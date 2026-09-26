package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

type syntaxIndexedDeclaration struct {
	fact syntaxDeclaration
	node graph.Node
}

type syntaxIndexedFile struct {
	source              sourceFile
	rel                 string
	payload             []byte
	newlines            []int
	node                graph.Node
	facts               syntaxFacts
	declarations        []syntaxIndexedDeclaration
	declarationsByScope map[string][]syntaxIndexedDeclaration
	importFiles         map[string]string
}

func isSyntaxCoreLanguage(language string) bool {
	switch language {
	case "javascript", "typescript", "python", "rust", "java", "kotlin", "csharp":
		return true
	default:
		return false
	}
}

// buildSyntaxIndexedFile turns parsed syntaxFacts into the indexed file
// structure the resolver consumes (declarations, per-scope lookup index).
// It performs no I/O, so tests can exercise call resolution against
// hand-written facts without invoking the tree-sitter parser.
func buildSyntaxIndexedFile(rel, language, modulePath string, payload []byte, facts syntaxFacts) *syntaxIndexedFile {
	newlines := syntaxNewlineOffsets(payload)
	lines := len(newlines) + 1
	qualified := modulePath + ":" + rel
	fileNode := graph.Node{
		ID: nodeID(graph.KindFile, qualified, rel, 1), Kind: graph.KindFile,
		Name: filepath.Base(rel), QualifiedName: qualified, File: rel,
		StartLine: 1, EndLine: max(1, lines), Language: language,
	}
	entry := &syntaxIndexedFile{
		source:      sourceFile{Path: rel, Language: language},
		rel:         rel,
		payload:     payload,
		newlines:    newlines,
		node:        fileNode,
		facts:       facts,
		importFiles: make(map[string]string),
	}
	for _, fact := range facts.Declarations {
		start := syntaxLine(entry.newlines, int(fact.StartByte), len(entry.payload))
		endOffset := fact.EndByte
		if endOffset > fact.StartByte {
			endOffset--
		}
		end := syntaxLine(entry.newlines, int(endOffset), len(entry.payload))
		name := fact.Name
		if fact.Container != "" {
			name = fact.Container + "." + name
		}
		kind := fact.Kind
		switch strings.ToLower(kind) {
		case "type":
			kind = graph.KindType
		case "method":
			kind = graph.KindMethod
		default:
			kind = graph.KindFunction
		}
		declQualified := modulePath + ":" + rel + "#" + name
		detail := fact.Detail
		if detail == "" {
			detail = syntaxSpanText(entry.payload, fact.StartByte, fact.EndByte)
		}
		node := graph.Node{
			ID: nodeID(kind, declQualified, rel, start), Kind: kind,
			Name: fact.Name, QualifiedName: declQualified, File: rel,
			StartLine: start, EndLine: max(start, end), Detail: detail,
			Language: language,
		}
		entry.declarations = append(entry.declarations, syntaxIndexedDeclaration{fact: fact, node: node})
	}
	entry.declarationsByScope = make(map[string][]syntaxIndexedDeclaration)
	for _, declaration := range entry.declarations {
		key := syntaxDeclarationKey(declaration.fact.Container, declaration.fact.Name)
		entry.declarationsByScope[key] = append(entry.declarationsByScope[key], declaration)
	}
	return entry
}

func indexSyntaxCore(ctx context.Context, root, modulePath string, files []sourceFile, projectNode graph.Node, value *graph.Graph, edges map[string]graph.Edge) error {
	indexed := make([]*syntaxIndexedFile, 0)
	byPath := make(map[string]*syntaxIndexedFile)
	byID := make(map[string]*syntaxIndexedFile)
	externalIDs := make(map[string]string)
	for _, source := range files {
		if !isSyntaxCoreLanguage(source.Language) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("index repository: %w", err)
		}
		rel, err := filepath.Rel(root, source.Path)
		if err != nil {
			return fmt.Errorf("resolve relative path for %s: %w", source.Path, err)
		}
		rel = filepath.ToSlash(rel)
		info, err := os.Stat(source.Path)
		if err != nil {
			return fmt.Errorf("stat source file %s: %w", source.Path, err)
		}
		if info.Size() > maximumSourceSize {
			value.Coverage.SkippedFiles = append(value.Coverage.SkippedFiles, graph.SkippedFile{File: rel, Reason: "file exceeds 4 MiB"})
			continue
		}
		payload, err := os.ReadFile(source.Path)
		if err != nil {
			return fmt.Errorf("read source file %s: %w", source.Path, err)
		}
		facts, err := parseSyntax(ctx, source.Language, source.Path, payload)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("parse source file %s: %w", rel, ctx.Err())
			}
			value.Coverage.SkippedFiles = append(value.Coverage.SkippedFiles, graph.SkippedFile{File: rel, Reason: err.Error()})
			continue
		}
		entry := buildSyntaxIndexedFile(rel, source.Language, modulePath, payload, facts)
		entry.source = source
		indexed = append(indexed, entry)
		byPath[rel] = entry
		byID[entry.node.ID] = entry
	}

	// Build every file and declaration before resolving any call edges.
	for _, file := range indexed {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("index repository: %w", err)
		}
		value.Nodes = append(value.Nodes, file.node)
		addEdge(edges, projectNode.ID, file.node.ID, graph.EdgeContains)
		value.Coverage.IndexedFiles++
		value.Coverage.IndexedByLanguage[file.source.Language]++
		for _, declaration := range file.declarations {
			value.Nodes = append(value.Nodes, declaration.node)
			addEdge(edges, file.node.ID, declaration.node.ID, graph.EdgeDefines)
		}
		for _, declaration := range file.declarations {
			if declaration.node.Kind != graph.KindMethod || declaration.fact.Container == "" {
				continue
			}
			if class := typeDeclarationForContainer(file, declaration.fact.Container, &declaration.fact); class != nil {
				addEdge(edges, class.node.ID, declaration.node.ID, graph.EdgeContains)
			}
		}
	}

	for _, file := range indexed {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("index repository: %w", err)
		}
		for _, imp := range file.facts.Imports {
			if strings.TrimSpace(imp.Path) == "" {
				continue
			}
			importPath := imp.Path
			if file.source.Language == "python" && imp.Imported != "" && strings.Trim(importPath, ".") == "" {
				importPath += imp.Imported
			}
			target := resolveSyntaxImport(file, importPath, byPath)
			if target == nil && importPath != imp.Path {
				target = resolveSyntaxImport(file, imp.Path, byPath)
			}
			if target != nil {
				if previous, exists := file.importFiles[imp.Local]; !exists {
					file.importFiles[imp.Local] = target.node.ID
				} else if previous != target.node.ID {
					file.importFiles[imp.Local] = ""
				}
				if imp.Local == "" {
					file.importFiles[filepath.Base(strings.TrimSuffix(imp.Path, filepath.Ext(imp.Path)))] = target.node.ID
				}
				addEdge(edges, file.node.ID, target.node.ID, graph.EdgeImports)
				continue
			}
			value.ImportPaths = append(value.ImportPaths, importPath)
			dependency := imp.Path
			value.Dependencies = append(value.Dependencies, dependency)
			targetID := externalIDs[dependency]
			if targetID == "" {
				node := externalNode(dependency, filepath.Base(dependency), file.source.Language+" dependency")
				node.Language = file.source.Language
				value.Nodes = append(value.Nodes, node)
				targetID = node.ID
				externalIDs[dependency] = targetID
			}
			addEdge(edges, file.node.ID, targetID, graph.EdgeImports)
		}
	}

	for _, file := range indexed {
		for _, entry := range file.facts.Heritage {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("index repository: %w", err)
			}
			source := typeDeclarationForContainer(file, entry.Type, nil)
			if source == nil {
				continue
			}
			targets := resolveNormalizedType(file, entry.Super, byID, byPath)
			if len(targets) != 1 {
				continue
			}
			target := targets[0]
			addEdge(edges, source.node.ID, target.node.ID, classifyHeritageEdge(entry.Kind, source.fact.Interface, target.fact.Interface))
		}
	}

	for _, file := range indexed {
		for _, call := range file.facts.Calls {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("index repository: %w", err)
			}
			source := enclosingSyntaxDeclaration(file.declarations, call.StartByte)
			if source == nil {
				if !call.Constructor {
					continue
				}
			}
			caller := file.node
			if source != nil {
				caller = source.node
			}
			callerFact := syntaxDeclaration{}
			if source != nil {
				callerFact = source.fact
			}
			targets := resolveSyntaxCall(file, callerFact, call, byID, byPath)
			var targetID string
			if len(targets) == 1 {
				targetID = targets[0].node.ID
			} else {
				name := syntaxCallName(call.Target)
				key := file.source.Language + ":" + call.Target
				targetID = externalIDs[key]
				if targetID == "" {
					node := externalNode(key, name, file.source.Language+" call target")
					node.Language = file.source.Language
					value.Nodes = append(value.Nodes, node)
					targetID = node.ID
					externalIDs[key] = targetID
				}
			}
			addEdge(edges, caller.ID, targetID, graph.EdgeCalls)
		}
	}
	return nil
}

// classifyHeritageEdge maps a heritage relation to an EXTENDS or IMPLEMENTS
// edge kind. A source that is not itself an interface cannot legally extend
// an interface, so that combination is reported as IMPLEMENTS instead;
// interface-extends-interface keeps EXTENDS.
func classifyHeritageEdge(kind string, sourceInterface, targetInterface bool) string {
	switch kind {
	case syntaxHeritageImplements:
		return graph.EdgeImplements
	case syntaxHeritageExtends:
		if targetInterface && !sourceInterface {
			return graph.EdgeImplements
		}
		return graph.EdgeExtends
	default:
		if targetInterface {
			return graph.EdgeImplements
		}
		return graph.EdgeExtends
	}
}

func syntaxNewlineOffsets(src []byte) []int {
	var offsets []int
	for offset, value := range src {
		if value == '\n' {
			offsets = append(offsets, offset)
		}
	}
	return offsets
}

func syntaxLine(newlines []int, offset, sourceLength int) int {
	if offset < 0 {
		offset = 0
	}
	if offset > sourceLength {
		offset = sourceLength
	}
	return 1 + sort.SearchInts(newlines, offset)
}

func syntaxSpanText(src []byte, start, end uint32) string {
	left, right := int(start), int(end)
	if left < 0 || left > len(src) {
		return ""
	}
	if right < left {
		right = left
	}
	if right > len(src) {
		right = len(src)
	}
	return strings.TrimSpace(string(src[left:right]))
}

func enclosingSyntaxDeclaration(declarations []syntaxIndexedDeclaration, offset uint32) *syntaxIndexedDeclaration {
	var best *syntaxIndexedDeclaration
	var bestSize uint32
	for index := range declarations {
		declaration := &declarations[index]
		if declaration.node.Kind == graph.KindType || offset < declaration.fact.StartByte || offset >= declaration.fact.EndByte {
			continue
		}
		size := declaration.fact.EndByte - declaration.fact.StartByte
		if best == nil || size < bestSize {
			best = declaration
			bestSize = size
		}
	}
	return best
}

func syntaxCallName(target string) string {
	if index := strings.LastIndexAny(target, ".:"); index >= 0 {
		return target[index+1:]
	}
	return target
}

// isImplicitMemberLanguage reports whether a bare identifier receiver (for
// example `store.save()` with no `this.`/`self.` prefix) may refer to an
// instance field of the enclosing type. JS/TS/Python require an explicit
// this./self. qualifier to reach a field, so they are excluded. Rust is also
// excluded: fields there always require an explicit `self.` qualifier, and
// match/if-let bindings (which could otherwise shadow a field name) are not
// extracted as Local bindings.
func isImplicitMemberLanguage(language string) bool {
	switch language {
	case "java", "kotlin", "csharp":
		return true
	default:
		return false
	}
}

// isSameDirectoryFallbackLanguage reports whether an unresolved simple type
// name may be looked up among sibling files in the same directory, matching
// the package/namespace conventions of these languages.
func isSameDirectoryFallbackLanguage(language string) bool {
	switch language {
	case "java", "kotlin", "csharp":
		return true
	default:
		return false
	}
}

// singleSegmentReceiverField reports whether qualifier is exactly
// "<prefix>.<one identifier>" with no further dots, returning that
// identifier. Multi-segment qualifiers such as "this.a.b" are rejected so
// they never resolve via caller-field bindings.
func singleSegmentReceiverField(qualifier, prefix string) (string, bool) {
	full := prefix + "."
	if !strings.HasPrefix(qualifier, full) {
		return "", false
	}
	remainder := qualifier[len(full):]
	if remainder == "" || strings.ContainsAny(remainder, ".:") {
		return "", false
	}
	return remainder, true
}

// normalizeSyntaxBindingType strips reference/pointer sigils, nullability
// markers and generic arguments from a syntactically-written type so it can
// be compared against declaration names. It reports ok=false for array
// types, whose element type cannot be resolved to a single declaration.
func normalizeSyntaxBindingType(raw string) (string, bool) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return "", false
	}
	for {
		trimmed := strings.TrimPrefix(t, "&mut ")
		if trimmed == t {
			trimmed = strings.TrimPrefix(t, "&")
		}
		if trimmed == t {
			trimmed = strings.TrimPrefix(t, "*")
		}
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == t {
			break
		}
		t = trimmed
	}
	t = strings.TrimSuffix(t, "?")
	t = strings.TrimSuffix(t, "!")
	t = strings.TrimSpace(t)
	if strings.HasSuffix(t, "[]") {
		return "", false
	}
	if idx := strings.IndexByte(t, '['); idx >= 0 {
		end := strings.LastIndexByte(t, ']')
		if end <= idx {
			return "", false
		}
		inner := strings.TrimSpace(t[idx+1 : end])
		if inner == "" {
			return "", false
		}
		t = strings.TrimSpace(t[:idx])
	}
	if idx := strings.IndexByte(t, '<'); idx >= 0 {
		t = strings.TrimSpace(t[:idx])
	}
	if t == "" {
		return "", false
	}
	return t, true
}

// resolveNormalizedType resolves a raw, syntactically-written type (from a
// binding or heritage fact) to its declaration, normalising it first.
func resolveNormalizedType(file *syntaxIndexedFile, raw string, byID, byPath map[string]*syntaxIndexedFile) []syntaxIndexedDeclaration {
	normalized, ok := normalizeSyntaxBindingType(raw)
	if !ok {
		return nil
	}
	return resolveSyntaxType(file, normalized, byID, byPath)
}

// innermostLocalBinding finds the Local binding for name whose span covers
// at, preferring the covering binding whose owning scope [ScopeStart,
// EndByte) is smallest (most nested) — not the binding's own [StartByte,
// EndByte) span, since a binding declared late in a wide scope can have a
// smaller span than one declared early in a narrower nested scope. When two
// candidates share the same scope (identical ScopeStart and EndByte — the
// signature of a genuine reassignment or shadow redeclaration within one
// scope), the one with the latest StartByte at or before at wins, as the
// declaration or reassignment most recently in effect at the call site.
func innermostLocalBinding(file *syntaxIndexedFile, name string, at uint32) (syntaxBinding, bool) {
	var best syntaxBinding
	found := false
	for _, binding := range file.facts.Bindings {
		if !binding.Local || binding.Field != name {
			continue
		}
		if at < binding.StartByte || at >= binding.EndByte {
			continue
		}
		if !found {
			best, found = binding, true
			continue
		}
		bestScope := best.EndByte - best.ScopeStart
		scope := binding.EndByte - binding.ScopeStart
		switch {
		case scope < bestScope:
			best = binding
		case scope == bestScope && binding.ScopeStart == best.ScopeStart && binding.StartByte > best.StartByte:
			best = binding
		}
	}
	return best, found
}

// resolveLocalBindingCall resolves a `name.method()` call where name may be
// a local variable or parameter. The bound bool reports whether a Local
// binding for name was found at all: when true, the outcome (possibly nil
// for External) is final and no other resolution rule should run.
func resolveLocalBindingCall(file *syntaxIndexedFile, call syntaxCall, name, method string, byID, byPath map[string]*syntaxIndexedFile) (declarations []syntaxIndexedDeclaration, bound bool) {
	binding, found := innermostLocalBinding(file, name, call.StartByte)
	if !found {
		return nil, false
	}
	if binding.Type == "" || localBindingTypeAmbiguous(file, name, binding) {
		return nil, true
	}
	return methodsOfType(file, binding.Type, method, byID, byPath), true
}

// localBindingTypeAmbiguous reports whether name is reassigned anywhere in
// the file with a different static type: it looks for other Local bindings
// of name that share binding's scope — the same (ScopeStart, EndByte) pair,
// identifying the same variable's own scope, rather than EndByte alone,
// which is not a scope identity: nested scopes can coincidentally end on the
// same byte as an enclosing or sibling scope (for example a Python
// `def inner()` that is the last statement of `def outer()`, or two nested
// JS arrow function bodies that close on the same `}`), and pairing on
// EndByte alone would wrongly treat an unrelated nested binding as a
// reassignment of the outer one. Bindings that genuinely share a scope
// report true as soon as one of them has a different Type (including "" vs.
// a concrete type). The scan is not restricted to any enclosing declaration:
// a reassignment reachable from a nested closure (for example
// `function outer(){ let s = new A(); if (f) s = new B();
// function inner(){ s.exec() } }`, or a module-level variable reassigned
// inside one function and read inside another) still shares the same scoped
// binding, and the call sees whichever assignment ran most recently at
// runtime rather than whichever declaration textually encloses it. When that
// happens the name's type at the call site is unknown regardless of which
// assignment textually precedes the call, so the caller must treat the call
// as External.
func localBindingTypeAmbiguous(file *syntaxIndexedFile, name string, binding syntaxBinding) bool {
	for _, candidate := range file.facts.Bindings {
		if !candidate.Local || candidate.Field != name || candidate.ScopeStart != binding.ScopeStart || candidate.EndByte != binding.EndByte {
			continue
		}
		if candidate.Type != binding.Type {
			return true
		}
	}
	return false
}

// resolveFieldBindingCall resolves a call on a member field (Container==
// caller.Container, Field==field). All matching bindings must agree on a
// single resolved declaration, otherwise the call is External. Any matching
// binding with an empty Type (an untyped or unresolvable initializer) makes
// the field's type unknown outright, since methodsOfType would silently skip
// it rather than count it as a conflicting candidate. The bound bool reports
// whether any field binding for this name exists at all.
func resolveFieldBindingCall(file *syntaxIndexedFile, caller syntaxDeclaration, field, method string, byID, byPath map[string]*syntaxIndexedFile) (declarations []syntaxIndexedDeclaration, bound bool) {
	matched := false
	seen := make(map[string]syntaxIndexedDeclaration)
	for _, binding := range file.facts.Bindings {
		if binding.Local || binding.Container != caller.Container || binding.Field != field {
			continue
		}
		matched = true
		if binding.Type == "" {
			return nil, true
		}
		if candidates := methodsOfType(file, binding.Type, method, byID, byPath); len(candidates) == 1 {
			seen[candidates[0].node.ID] = candidates[0]
		}
	}
	if !matched {
		return nil, false
	}
	if len(seen) == 1 {
		for _, declaration := range seen {
			return []syntaxIndexedDeclaration{declaration}, true
		}
	}
	return nil, true
}

func resolveSyntaxCall(file *syntaxIndexedFile, caller syntaxDeclaration, call syntaxCall, byID, byPath map[string]*syntaxIndexedFile) []syntaxIndexedDeclaration {
	target := call.Target
	name := syntaxCallName(target)
	if call.Constructor {
		name = strings.TrimSpace(strings.TrimPrefix(name, "new "))
	}
	if name == "" {
		return nil
	}
	if call.Constructor {
		if index := strings.LastIndexAny(target, ".:"); index >= 0 {
			qualifier := target[:index]
			if fileID := file.importFiles[qualifier]; fileID != "" {
				return declarationsInFilesOfKind(byID, fileID, name, graph.KindType)
			}
		}
		return resolveSyntaxType(file, name, byID, byPath)
	}

	qualifier := ""
	if index := strings.LastIndexAny(target, ".:"); index >= 0 {
		qualifier = target[:index]
	}

	if qualifier == "super" {
		return superMethodsOfType(file, caller, name, byID, byPath)
	}
	if qualifier == "this" || qualifier == "self" {
		return resolveOwnContainerMethod(file, caller, name, byID, byPath)
	}
	if field, ok := singleSegmentReceiverField(qualifier, "this"); ok {
		if declarations, bound := resolveFieldBindingCall(file, caller, field, name, byID, byPath); bound {
			return declarations
		}
		return nil
	}
	if field, ok := singleSegmentReceiverField(qualifier, "self"); ok {
		if declarations, bound := resolveFieldBindingCall(file, caller, field, name, byID, byPath); bound {
			return declarations
		}
		return nil
	}
	if strings.HasPrefix(qualifier, "this.") || strings.HasPrefix(qualifier, "self.") {
		// Multi-segment qualifiers beyond this./self. (e.g. this.a.b) are not
		// resolvable through a single field binding.
		return nil
	}
	if qualifier != "" && !strings.ContainsAny(qualifier, ".:") {
		if declarations, bound := resolveLocalBindingCall(file, call, qualifier, name, byID, byPath); bound {
			return declarations
		}
		if isImplicitMemberLanguage(file.source.Language) {
			if declarations, bound := resolveFieldBindingCall(file, caller, qualifier, name, byID, byPath); bound {
				return declarations
			}
		}
	}
	if qualifier != "" {
		localQualified := declarationsNamed(file, name, qualifier)
		if len(localQualified) > 0 {
			return localQualified
		}
		if fileID := file.importFiles[qualifier]; fileID != "" {
			return declarationsInFiles(byID, []string{fileID}, name)
		}
		return nil
	}
	for _, scope := range syntaxCallScopes(caller) {
		local := declarationsNamed(file, name, scope)
		if len(local) > 0 {
			return local
		}
	}
	if fileID := file.importFiles[name]; fileID != "" {
		for _, imp := range file.facts.Imports {
			if imp.Local == name {
				imported := imp.Imported
				if imported == "" {
					imported = name
				}
				return declarationsInFiles(byID, []string{fileID}, imported)
			}
		}
	}
	if file.source.Language == "python" {
		if declarations := resolveSyntaxType(file, name, byID, byPath); len(declarations) == 1 {
			return declarations
		}
	}
	return nil
}

func syntaxCallScopes(caller syntaxDeclaration) []string {
	scope := caller.Name
	if caller.Container != "" {
		scope = caller.Container + "." + caller.Name
	}
	scopes := []string{scope}
	parent := caller.Container
	for parent != "" {
		scopes = append(scopes, parent)
		index := strings.LastIndex(parent, ".")
		if index < 0 {
			parent = ""
		} else {
			parent = parent[:index]
		}
	}
	scopes = append(scopes, "")
	return scopes
}

func declarationsNamed(file *syntaxIndexedFile, name, container string) []syntaxIndexedDeclaration {
	var matches []syntaxIndexedDeclaration
	for _, declaration := range file.declarationsByScope[syntaxDeclarationKey(container, name)] {
		if declaration.node.Kind != graph.KindType {
			matches = append(matches, declaration)
		}
	}
	return matches
}

func syntaxDeclarationKey(container, name string) string { return container + "\x00" + name }

func declarationNamed(file *syntaxIndexedFile, name, container string, kind string) *syntaxIndexedDeclaration {
	if file == nil {
		return nil
	}
	for index := range file.declarationsByScope[syntaxDeclarationKey(container, name)] {
		declaration := &file.declarationsByScope[syntaxDeclarationKey(container, name)][index]
		if declaration.node.Kind == kind {
			return declaration
		}
	}
	return nil
}

// methodsOfType resolves a method by name on typeName (a raw, possibly
// unnormalised type expression), walking supertypes when the method is not
// declared directly on the type. See searchMethodInHierarchy for the
// uniqueness rules.
func methodsOfType(file *syntaxIndexedFile, typeName, method string, byID, byPath map[string]*syntaxIndexedFile) []syntaxIndexedDeclaration {
	types := resolveNormalizedType(file, typeName, byID, byPath)
	if len(types) != 1 {
		return nil
	}
	return searchMethodInHierarchy([]syntaxIndexedDeclaration{types[0]}, method, byID, byPath, map[string]bool{})
}

// resolveOwnContainerMethod resolves `this.method()`/`self.method()` by
// first looking for a method declared directly on the caller's own
// container in this file, then walking that container's own heritage chain.
// It never falls back to resolveSyntaxType's same-directory search: the
// caller's own container is a fact already known from this file, including
// for nested types, whose simple Container name would otherwise wrongly
// match an unrelated top-level type of the same name in a sibling file.
// resolveOwnContainerMethod is never reached for a bare `method()` call with
// no receiver at all, even in implicit-member languages (Java/Kotlin/C#):
// resolveSyntaxCall resolves that case through syntaxCallScopes instead,
// which only matches a method declared directly in the caller's own
// container and does not walk the heritage chain for an inherited call.
func resolveOwnContainerMethod(file *syntaxIndexedFile, caller syntaxDeclaration, method string, byID, byPath map[string]*syntaxIndexedFile) []syntaxIndexedDeclaration {
	if caller.Container == "" {
		return nil
	}
	if matches := declarationsNamed(file, method, caller.Container); len(matches) > 0 {
		if len(matches) == 1 {
			return matches
		}
		return nil
	}
	ownType := typeDeclarationForContainer(file, caller.Container, &caller)
	if ownType == nil {
		return nil
	}
	visited := map[string]bool{ownType.node.ID: true}
	supertypes := supertypesOf(file, caller.Container, byID, byPath)
	return searchMethodInHierarchy(supertypes, method, byID, byPath, visited)
}

// isDynamicScopeChainLanguage reports whether language builds a member's
// Container as the full dotted enclosing-scope chain (dynamicContainer),
// rather than only the nearest container's simple name (staticContainer).
func isDynamicScopeChainLanguage(language string) bool {
	switch language {
	case "javascript", "typescript", "python":
		return true
	default:
		return false
	}
}

// typeDeclarationForContainer resolves the Type declaration whose members
// carry Container c: the counterpart of a member's own fact.Container (for a
// method→class CONTAINS edge, where member is that method's own declaration)
// or of a heritage entry's Type field (for the EXTENDS/IMPLEMENTS source
// type, where member is nil since heritage entries carry no byte position).
// The two syntax extractors write c using different conventions: for
// dynamic-scope-chain languages (JS/TS/Python) c is either an exact
// top-level type name (Container "") or, split at its last ".", the owning
// type's own Container and simple name, matching dynamicContainer's full
// scope-chain join — that convention alone is enough to identify a unique
// declaration, ambiguity aside. For static languages (Rust/Java/Kotlin/C#),
// c is only ever the nearest enclosing type's simple name with no path
// information (staticContainer), so nested types can legally share a name
// with an unrelated type elsewhere in the file; see
// staticTypeDeclarationForContainer for how that is disambiguated.
func typeDeclarationForContainer(file *syntaxIndexedFile, c string, member *syntaxDeclaration) *syntaxIndexedDeclaration {
	if c == "" {
		return nil
	}
	if isDynamicScopeChainLanguage(file.source.Language) {
		index := strings.LastIndex(c, ".")
		if index < 0 {
			return declarationNamed(file, c, "", graph.KindType)
		}
		return declarationNamed(file, c[index+1:], c[:index], graph.KindType)
	}
	return staticTypeDeclarationForContainer(file, c, member)
}

// staticTypeDeclarationForContainer resolves a static-language container
// name c (see typeDeclarationForContainer) to its Type declaration when more
// than one Type named c exists in the file — legal since c alone carries no
// path information. member, when non-nil, is used to disambiguate: the
// unique Type declaration named c whose [StartByte, EndByte) encloses
// member's own StartByte wins, matching ordinary nested classes (Java/
// Kotlin/C#) whose body genuinely contains their methods' bytes. If more
// than one same-named candidate encloses member, the result is nil rather
// than guessing which one is innermost. A Rust impl block sits outside the
// struct/enum/trait body it extends, so no candidate encloses a method
// declared inside it; that case, and any heritage entry (member == nil),
// cannot be disambiguated from the facts available here either, so more than
// one same-named candidate resolves to nil rather than guessing.
func staticTypeDeclarationForContainer(file *syntaxIndexedFile, c string, member *syntaxDeclaration) *syntaxIndexedDeclaration {
	var candidates []*syntaxIndexedDeclaration
	for index := range file.declarations {
		declaration := &file.declarations[index]
		if declaration.node.Kind == graph.KindType && declaration.fact.Name == c {
			candidates = append(candidates, declaration)
		}
	}
	if len(candidates) != 1 {
		if len(candidates) < 2 || member == nil {
			return nil
		}
		return enclosingTypeDeclaration(candidates, member.StartByte)
	}
	return candidates[0]
}

// enclosingTypeDeclaration returns the unique candidate whose
// [StartByte, EndByte) encloses at, or nil when no candidate or more than
// one candidate encloses it.
func enclosingTypeDeclaration(candidates []*syntaxIndexedDeclaration, at uint32) *syntaxIndexedDeclaration {
	var enclosing *syntaxIndexedDeclaration
	for _, candidate := range candidates {
		if at < candidate.fact.StartByte || at >= candidate.fact.EndByte {
			continue
		}
		if enclosing != nil {
			return nil
		}
		enclosing = candidate
	}
	return enclosing
}

// superMethodsOfType resolves `super.method()` by starting the hierarchy
// search at the direct supertypes of the caller's own type, resolved the
// same container-agnostic way as resolveOwnContainerMethod.
func superMethodsOfType(file *syntaxIndexedFile, caller syntaxDeclaration, method string, byID, byPath map[string]*syntaxIndexedFile) []syntaxIndexedDeclaration {
	ownType := typeDeclarationForContainer(file, caller.Container, &caller)
	if ownType == nil {
		return nil
	}
	visited := map[string]bool{ownType.node.ID: true}
	supertypes := supertypesOf(file, caller.Container, byID, byPath)
	return searchMethodInHierarchy(supertypes, method, byID, byPath, visited)
}

// searchMethodInHierarchy performs a breadth-first search for method over
// frontier and its supertypes, one level at a time, with a cycle guard and a
// depth cap. The first level where the method is declared wins; if more
// than one declaration is found at that level (overloads or a diamond of
// unrelated declarations) the call is ambiguous and resolves to nil.
func searchMethodInHierarchy(frontier []syntaxIndexedDeclaration, method string, byID, byPath map[string]*syntaxIndexedFile, visited map[string]bool) []syntaxIndexedDeclaration {
	for depth := 0; depth <= 8 && len(frontier) > 0; depth++ {
		var found []syntaxIndexedDeclaration
		var next []syntaxIndexedDeclaration
		for _, decl := range frontier {
			if visited[decl.node.ID] {
				continue
			}
			visited[decl.node.ID] = true
			ownerFile := byPath[decl.node.File]
			if ownerFile == nil {
				continue
			}
			if matches := declarationsNamed(ownerFile, method, decl.fact.Name); len(matches) > 0 {
				found = append(found, matches...)
				continue
			}
			next = append(next, supertypesOf(ownerFile, decl.fact.Name, byID, byPath)...)
		}
		if len(found) > 0 {
			if len(found) == 1 {
				return found
			}
			return nil
		}
		frontier = next
	}
	return nil
}

// supertypesOf resolves the direct supertypes of typeName as declared in
// file's own heritage facts. typeName must be in the same identifier space
// as syntaxHeritage.Type (see typeDeclarationForContainer): a class's own
// fact.Container/fact.Name pair for dynamic-scope-chain languages, or its
// simple fact.Name for static languages — not necessarily fact.Name alone.
// Unresolved or ambiguous supertypes are skipped rather than aborting the
// walk.
func supertypesOf(file *syntaxIndexedFile, typeName string, byID, byPath map[string]*syntaxIndexedFile) []syntaxIndexedDeclaration {
	var result []syntaxIndexedDeclaration
	for _, entry := range file.facts.Heritage {
		if entry.Type != typeName {
			continue
		}
		if matches := resolveNormalizedType(file, entry.Super, byID, byPath); len(matches) == 1 {
			result = append(result, matches[0])
		}
	}
	return result
}

func declarationsOfKind(file *syntaxIndexedFile, name, kind string) []syntaxIndexedDeclaration {
	var matches []syntaxIndexedDeclaration
	for _, declaration := range file.declarationsByScope[syntaxDeclarationKey("", name)] {
		if declaration.node.Kind == kind {
			matches = append(matches, declaration)
		}
	}
	return matches
}

func declarationsInFilesOfKind(files map[string]*syntaxIndexedFile, fileID, name, kind string) []syntaxIndexedDeclaration {
	file := files[fileID]
	if file != nil {
		if name == "" {
			var matches []syntaxIndexedDeclaration
			for _, declarations := range file.declarationsByScope {
				for _, declaration := range declarations {
					if declaration.node.Kind == kind {
						matches = append(matches, declaration)
					}
				}
			}
			return matches
		}
		return declarationsOfKind(file, name, kind)
	}
	return nil
}

func declarationsInFiles(files map[string]*syntaxIndexedFile, ids []string, name string) []syntaxIndexedDeclaration {
	var matches []syntaxIndexedDeclaration
	for _, id := range ids {
		file := files[id]
		if file == nil {
			continue
		}
		for _, declaration := range file.declarationsByScope[syntaxDeclarationKey("", name)] {
			if declaration.node.Kind != graph.KindType {
				matches = append(matches, declaration)
			}
		}
	}
	return matches
}

// resolveSyntaxType resolves a type name to its declaration. Names
// containing "::" (Rust module paths) or "." (package/namespace-qualified
// names) are resolved as qualified names and never match a same-file
// declaration by simple name, so a same-file type of that simple name is
// never mistaken for the qualified target. Plain simple names are looked up
// in the current file, then via an import alias, then (for Java/Kotlin/C#)
// among sibling files in the same directory.
func resolveSyntaxType(file *syntaxIndexedFile, name string, byID, byPath map[string]*syntaxIndexedFile) []syntaxIndexedDeclaration {
	if strings.Contains(name, "::") {
		return resolveRustCratePath(file, name, byPath)
	}
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		return resolveQualifiedSyntaxType(file, name[:idx], name[idx+1:], byID, byPath)
	}
	if matches := declarationsOfKind(file, name, graph.KindType); len(matches) > 0 {
		return matches
	}
	if fileID := file.importFiles[name]; fileID != "" {
		for _, imp := range file.facts.Imports {
			if imp.Local == name {
				imported := imp.Imported
				if imported == "default" {
					matches := declarationsInFilesOfKind(byID, fileID, "", graph.KindType)
					if len(matches) == 1 {
						return matches
					}
				}
				if imported == "" || imported == "*" {
					imported = name
				}
				return declarationsInFilesOfKind(byID, fileID, imported, graph.KindType)
			}
		}
	}
	if isSameDirectoryFallbackLanguage(file.source.Language) && !importShadowsSameDirectoryType(file, name) {
		return sameDirectoryTypeDeclaration(file, name, byPath)
	}
	return nil
}

// importShadowsSameDirectoryType reports whether file declares an import
// whose local binding is exactly name. By the time this is checked,
// file.importFiles[name] is already known not to point at an indexed file
// (the caller already tried that), so any such import is either external or
// otherwise unresolved: it shadows the simple name and the same-directory
// fallback must not silently substitute an unrelated sibling file.
func importShadowsSameDirectoryType(file *syntaxIndexedFile, name string) bool {
	for _, imp := range file.facts.Imports {
		if imp.Local == name {
			return true
		}
	}
	return false
}

// resolveQualifiedSyntaxType resolves a dotted, package/namespace-qualified
// type reference. It never falls back to matching simple against a
// same-file declaration.
func resolveQualifiedSyntaxType(file *syntaxIndexedFile, qualifier, simple string, byID, byPath map[string]*syntaxIndexedFile) []syntaxIndexedDeclaration {
	if fileID := file.importFiles[qualifier]; fileID != "" {
		return declarationsInFilesOfKind(byID, fileID, simple, graph.KindType)
	}
	full := qualifier + "." + simple
	if fileID := file.importFiles[full]; fileID != "" {
		return declarationsInFilesOfKind(byID, fileID, simple, graph.KindType)
	}
	if file.source.Language == "java" || file.source.Language == "kotlin" {
		if target := staticPackagePathFile(byPath, file.source.Language, full); target != nil {
			return declarationsOfKind(target, simple, graph.KindType)
		}
	}
	return nil
}

// sameDirectoryTypeDeclaration looks up a simple type name among sibling
// files of the same language in the same directory, returning it only when
// the match is unique.
func sameDirectoryTypeDeclaration(file *syntaxIndexedFile, name string, byPath map[string]*syntaxIndexedFile) []syntaxIndexedDeclaration {
	dir := filepath.Dir(file.rel)
	var matches []syntaxIndexedDeclaration
	for _, candidate := range byPath {
		if candidate == file || candidate.source.Language != file.source.Language || filepath.Dir(candidate.rel) != dir {
			continue
		}
		matches = append(matches, declarationsOfKind(candidate, name, graph.KindType)...)
	}
	if len(matches) == 1 {
		return matches
	}
	return nil
}

// staticPackagePathFile finds the unique indexed file of language whose
// repository-relative path ends with dotted converted to a path (a.b.C ->
// a/b/C.<ext>), matching Java/Kotlin package-qualified references to files
// that were never explicitly imported by path.
func staticPackagePathFile(byPath map[string]*syntaxIndexedFile, language, dotted string) *syntaxIndexedFile {
	ext := ""
	switch language {
	case "java":
		ext = ".java"
	case "kotlin":
		ext = ".kt"
	default:
		return nil
	}
	suffix := "/" + strings.ReplaceAll(dotted, ".", "/") + ext
	var match *syntaxIndexedFile
	count := 0
	for rel, candidate := range byPath {
		if candidate.source.Language != language {
			continue
		}
		if strings.HasSuffix("/"+rel, suffix) {
			match = candidate
			count++
		}
	}
	if count == 1 {
		return match
	}
	return nil
}

// resolveRustCratePath resolves a crate::a::b::Foo reference to the Type
// declaration in <crate>/src/a/b.rs or <crate>/src/a/b/mod.rs, where <crate>
// is the caller's own crate root. Any other qualified form (relative
// `super::`/`self::`, external crates), and a caller file with no
// discoverable crate root, are left unresolved.
func resolveRustCratePath(file *syntaxIndexedFile, qualified string, byPath map[string]*syntaxIndexedFile) []syntaxIndexedDeclaration {
	if file.source.Language != "rust" {
		return nil
	}
	segments := strings.Split(qualified, "::")
	if len(segments) < 2 || segments[0] != "crate" {
		return nil
	}
	simple := segments[len(segments)-1]
	modSegments := segments[1 : len(segments)-1]
	base := rustCrateRoot(file.rel)
	if base == "" {
		return nil
	}
	if len(modSegments) > 0 {
		base = filepath.ToSlash(filepath.Join(append([]string{base}, modSegments...)...))
	}
	for _, candidate := range []string{base + ".rs", base + "/mod.rs"} {
		if target := byPath[candidate]; target != nil {
			return declarationsOfKind(target, simple, graph.KindType)
		}
	}
	return nil
}

// rustCrateRoot infers the caller file's crate root ("<crate>/src") as the
// path prefix up to and including the nearest "src" ancestor directory of
// rel, so `crate::` paths in a Cargo workspace resolve within the caller's
// own crate (e.g. crates/a/src/x.rs -> crates/a/src) instead of always
// against a single repo-root src/. Returns "" when rel has no "src" ancestor
// at all (a crate's tests/, benches/, or examples/ directory sitting outside
// its own src tree), so the caller treats crate::-qualified references from
// such a file as unresolvable rather than guessing at an unrelated
// repo-root src/.
func rustCrateRoot(rel string) string {
	segments := strings.Split(rel, "/")
	for i := len(segments) - 2; i >= 0; i-- {
		if segments[i] == "src" {
			return strings.Join(segments[:i+1], "/")
		}
	}
	return ""
}

func resolveSyntaxImport(file *syntaxIndexedFile, importPath string, byPath map[string]*syntaxIndexedFile) *syntaxIndexedFile {
	base := filepath.FromSlash(importPath)
	if strings.HasPrefix(importPath, ".") {
		if file.source.Language == "python" {
			dots := len(importPath) - len(strings.TrimLeft(importPath, "."))
			module := strings.TrimLeft(importPath, ".")
			base = filepath.Dir(filepath.FromSlash(file.rel))
			for index := 1; index < dots; index++ {
				base = filepath.Dir(base)
			}
			if module != "" {
				base = filepath.Join(base, strings.ReplaceAll(module, ".", string(filepath.Separator)))
			}
		} else {
			base = filepath.Join(filepath.Dir(filepath.FromSlash(file.rel)), base)
		}
	} else if file.source.Language == "python" {
		base = strings.ReplaceAll(importPath, ".", string(filepath.Separator))
	} else if file.source.Language == "java" || file.source.Language == "kotlin" {
		return staticPackagePathFile(byPath, file.source.Language, importPath)
	} else {
		return nil
	}
	base = filepath.Clean(base)
	candidates := []string{filepath.ToSlash(base)}
	ext := filepath.Ext(base)
	if ext == ".js" || ext == ".jsx" || ext == ".mjs" || ext == ".cjs" {
		stem := strings.TrimSuffix(base, ext)
		candidates = append(candidates, filepath.ToSlash(stem+".ts"), filepath.ToSlash(stem+".tsx"))
	}
	if ext == "" {
		switch file.source.Language {
		case "python":
			candidates = append(candidates, filepath.ToSlash(base+".py"), filepath.ToSlash(filepath.Join(base, "__init__.py")))
		default:
			for _, extension := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"} {
				candidates = append(candidates, filepath.ToSlash(filepath.Join(base, "index"+extension)))
			}
			for _, extension := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"} {
				candidates = append(candidates, filepath.ToSlash(base+extension))
			}
		}
	}
	for _, candidate := range candidates {
		if target := byPath[candidate]; target != nil {
			return target
		}
	}
	return nil
}
