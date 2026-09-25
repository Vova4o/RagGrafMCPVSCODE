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
		newlines := syntaxNewlineOffsets(payload)
		lines := len(newlines) + 1
		qualified := modulePath + ":" + rel
		fileNode := graph.Node{
			ID: nodeID(graph.KindFile, qualified, rel, 1), Kind: graph.KindFile,
			Name: filepath.Base(rel), QualifiedName: qualified, File: rel,
			StartLine: 1, EndLine: max(1, lines), Language: source.Language,
		}
		entry := &syntaxIndexedFile{source: source, rel: rel, payload: payload, newlines: newlines, node: fileNode, facts: facts,
			importFiles: make(map[string]string)}
		indexed = append(indexed, entry)
		byPath[rel] = entry
		byID[fileNode.ID] = entry
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
		for _, fact := range file.facts.Declarations {
			start := syntaxLine(file.newlines, int(fact.StartByte), len(file.payload))
			endOffset := fact.EndByte
			if endOffset > fact.StartByte {
				endOffset--
			}
			end := syntaxLine(file.newlines, int(endOffset), len(file.payload))
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
			qualified := modulePath + ":" + file.rel + "#" + name
			detail := fact.Detail
			if detail == "" {
				detail = syntaxSpanText(file.payload, fact.StartByte, fact.EndByte)
			}
			node := graph.Node{
				ID: nodeID(kind, qualified, file.rel, start), Kind: kind,
				Name: fact.Name, QualifiedName: qualified, File: file.rel,
				StartLine: start, EndLine: max(start, end), Detail: detail,
				Language: file.source.Language,
			}
			file.declarations = append(file.declarations, syntaxIndexedDeclaration{fact: fact, node: node})
			value.Nodes = append(value.Nodes, node)
			addEdge(edges, file.node.ID, node.ID, graph.EdgeDefines)
		}
		file.declarationsByScope = make(map[string][]syntaxIndexedDeclaration)
		for _, declaration := range file.declarations {
			key := syntaxDeclarationKey(declaration.fact.Container, declaration.fact.Name)
			file.declarationsByScope[key] = append(file.declarationsByScope[key], declaration)
		}
		for _, declaration := range file.declarations {
			if declaration.node.Kind != graph.KindMethod || declaration.fact.Container == "" {
				continue
			}
			if class := declarationNamed(file, declaration.fact.Container, "", graph.KindType); class != nil {
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
		return resolveSyntaxType(file, name, byID)
	}
	qualifier := ""
	if index := strings.LastIndexAny(target, ".:"); index >= 0 {
		qualifier = target[:index]
	}
	if qualifier == "this" || qualifier == "self" {
		return declarationsNamed(file, name, caller.Container)
	}
	if strings.HasPrefix(qualifier, "this.") || strings.HasPrefix(qualifier, "self.") {
		field := qualifier[strings.LastIndex(qualifier, ".")+1:]
		for _, binding := range file.facts.Bindings {
			if binding.Container != caller.Container || binding.Field != field {
				continue
			}
			if declarations := methodsOfType(file, binding.Type, name, byID, byPath); len(declarations) > 0 {
				return declarations
			}
		}
		return nil
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

func resolveSyntaxType(file *syntaxIndexedFile, name string, byID map[string]*syntaxIndexedFile) []syntaxIndexedDeclaration {
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
	return nil
}

func methodsOfType(file *syntaxIndexedFile, typeName, method string, byID, byPath map[string]*syntaxIndexedFile) []syntaxIndexedDeclaration {
	types := resolveSyntaxType(file, typeName, byID)
	if len(types) != 1 {
		return nil
	}
	typeDecl := types[0]
	candidate := byPath[typeDecl.node.File]
	if candidate == nil {
		return nil
	}
	return declarationsNamed(candidate, method, typeDecl.fact.Name)
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
