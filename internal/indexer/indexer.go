// Package indexer builds a structural graph from source code.
package indexer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

var skippedDirectories = map[string]struct{}{
	".git": {}, ".hg": {}, ".svn": {}, ".idea": {}, ".vscode": {},
	"vendor": {}, "node_modules": {}, "dist": {}, "out": {}, ".codebase-graph": {},
}

var builtins = map[string]struct{}{
	"append": {}, "cap": {}, "clear": {}, "close": {}, "complex": {}, "copy": {},
	"delete": {}, "imag": {}, "len": {}, "make": {}, "max": {}, "min": {}, "new": {},
	"panic": {}, "print": {}, "println": {}, "real": {}, "recover": {},
}

type parsedFile struct {
	abs        *ast.File
	rel        string
	absPath    string
	packageID  string
	packageQ   string
	fileID     string
	importPath map[string]string
}

// Indexer builds repository graphs without starting background processes.
type Indexer struct {
	now func() time.Time
}

// New returns an Indexer.
func New() *Indexer {
	return &Indexer{now: time.Now}
}

// Index parses a repository and returns a deterministic structural graph.
func (i *Indexer) Index(ctx context.Context, repoPath string) (*graph.Graph, error) {
	root, err := canonicalRoot(repoPath)
	if err != nil {
		return nil, err
	}

	modulePath, err := detectModulePath(root)
	if err != nil {
		return nil, fmt.Errorf("detect module path: %w", err)
	}
	if modulePath == "" {
		modulePath = filepath.Base(root)
	}

	files, err := sourceFiles(ctx, root)
	if err != nil {
		return nil, err
	}

	projectName := filepath.Base(root)
	g := &graph.Graph{
		SchemaVersion: graph.SchemaVersion,
		ProjectID:     shortHash(root),
		Name:          projectName,
		Root:          root,
		Module:        modulePath,
		IndexedAt:     i.now().UTC(),
		Coverage: graph.Coverage{
			SupportedExtensions: supportedExtensions(),
			IndexedByLanguage:   make(map[string]int),
		},
	}

	projectNode := graph.Node{
		ID:            nodeID(graph.KindProject, root, "", 0),
		Kind:          graph.KindProject,
		Name:          projectName,
		QualifiedName: root,
	}
	g.Nodes = append(g.Nodes, projectNode)

	fset := token.NewFileSet()
	parsed := make([]parsedFile, 0, len(files))
	packageNodes := make(map[string]graph.Node)
	nodesByQualified := make(map[string]graph.Node)
	nodesByQualified[root] = projectNode
	declarationIDs := make(map[*ast.FuncDecl]string)
	functionsByPackage := make(map[string]map[string]string)
	methodsByPackage := make(map[string]map[string][]string)
	methodsByName := make(map[string][]string)
	edges := make(map[string]graph.Edge)

	for _, source := range files {
		if source.Language != "go" {
			continue
		}
		absPath := source.Path
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("index repository: %w", err)
		}

		rel, err := filepath.Rel(root, absPath)
		if err != nil {
			return nil, fmt.Errorf("resolve relative path for %s: %w", absPath, err)
		}
		rel = filepath.ToSlash(rel)
		fileAST, parseErr := parser.ParseFile(fset, absPath, nil, parser.ParseComments)
		if parseErr != nil {
			g.Coverage.SkippedFiles = append(g.Coverage.SkippedFiles, graph.SkippedFile{
				File: rel, Reason: parseErr.Error(),
			})
			continue
		}

		packageQ := packageQualified(modulePath, filepath.Dir(rel))
		packageNode, exists := packageNodes[packageQ]
		if !exists {
			packageNode = graph.Node{
				ID:            nodeID(graph.KindPackage, packageQ, "", 0),
				Kind:          graph.KindPackage,
				Name:          fileAST.Name.Name,
				QualifiedName: packageQ,
				Package:       packageQ,
			}
			packageNodes[packageQ] = packageNode
			nodesByQualified[packageQ] = packageNode
			g.Nodes = append(g.Nodes, packageNode)
			addEdge(edges, projectNode.ID, packageNode.ID, graph.EdgeContains)
		}

		fileNode := graph.Node{
			ID:            nodeID(graph.KindFile, packageQ+":"+rel, rel, 1),
			Kind:          graph.KindFile,
			Name:          filepath.Base(rel),
			QualifiedName: packageQ + ":" + rel,
			Package:       packageQ,
			File:          rel,
			StartLine:     1,
			EndLine:       fset.Position(fileAST.End()).Line,
			Language:      "go",
		}
		g.Nodes = append(g.Nodes, fileNode)
		nodesByQualified[fileNode.QualifiedName] = fileNode
		addEdge(edges, packageNode.ID, fileNode.ID, graph.EdgeContains)

		imports := make(map[string]string)
		for _, spec := range fileAST.Imports {
			path, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				continue
			}
			alias := filepath.Base(path)
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			if alias != "_" && alias != "." {
				imports[alias] = path
			}
		}

		parsed = append(parsed, parsedFile{
			abs: fileAST, rel: rel, absPath: absPath, packageID: packageNode.ID,
			packageQ: packageQ, fileID: fileNode.ID, importPath: imports,
		})

		if functionsByPackage[packageQ] == nil {
			functionsByPackage[packageQ] = make(map[string]string)
		}
		if methodsByPackage[packageQ] == nil {
			methodsByPackage[packageQ] = make(map[string][]string)
		}

		for _, decl := range fileAST.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					qualified := packageQ + "." + typeSpec.Name.Name
					node := graph.Node{
						ID:   nodeID(graph.KindType, qualified, rel, fset.Position(typeSpec.Pos()).Line),
						Kind: graph.KindType, Name: typeSpec.Name.Name, QualifiedName: qualified,
						Package: packageQ, File: rel,
						StartLine: fset.Position(typeSpec.Pos()).Line,
						EndLine:   fset.Position(typeSpec.End()).Line,
						Detail:    typeDetail(typeSpec),
						Language:  "go",
					}
					g.Nodes = append(g.Nodes, node)
					nodesByQualified[qualified] = node
					addEdge(edges, fileNode.ID, node.ID, graph.EdgeDefines)
				}
			case *ast.FuncDecl:
				receiver := receiverName(d)
				kind := graph.KindFunction
				qualified := packageQ + "." + d.Name.Name
				if receiver != "" {
					kind = graph.KindMethod
					qualified = packageQ + "." + receiver + "." + d.Name.Name
				}
				node := graph.Node{
					ID:   nodeID(kind, qualified, rel, fset.Position(d.Pos()).Line),
					Kind: kind, Name: d.Name.Name, QualifiedName: qualified,
					Package: packageQ, File: rel,
					StartLine: fset.Position(d.Pos()).Line,
					EndLine:   fset.Position(d.End()).Line,
					Detail:    functionDetail(fset, d, receiver),
					Language:  "go",
				}
				g.Nodes = append(g.Nodes, node)
				nodesByQualified[qualified] = node
				declarationIDs[d] = node.ID
				addEdge(edges, fileNode.ID, node.ID, graph.EdgeDefines)
				if receiver == "" {
					functionsByPackage[packageQ][d.Name.Name] = node.ID
				} else {
					methodsByPackage[packageQ][d.Name.Name] = append(methodsByPackage[packageQ][d.Name.Name], node.ID)
					methodsByName[d.Name.Name] = append(methodsByName[d.Name.Name], node.ID)
				}
			}
		}
	}

	g.Coverage.IndexedFiles = len(parsed)
	if len(parsed) > 0 {
		g.Coverage.IndexedByLanguage["go"] = len(parsed)
	}

	for _, file := range parsed {
		for _, importPath := range file.importPath {
			g.Dependencies = append(g.Dependencies, importPath)
			g.ImportPaths = append(g.ImportPaths, importPath)
			target, exists := nodesByQualified[importPath]
			if !exists {
				target = externalNode(importPath, filepath.Base(importPath), "package")
				nodesByQualified[importPath] = target
				g.Nodes = append(g.Nodes, target)
			}
			addEdge(edges, file.packageID, target.ID, graph.EdgeImports)
		}

		for _, decl := range file.abs.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			sourceID := declarationIDs[fn]
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				targetID, externalQualified, externalName := resolveCall(
					call.Fun, file.packageQ, file.importPath, functionsByPackage,
					methodsByPackage, methodsByName,
				)
				if targetID == "" && externalQualified != "" {
					target, exists := nodesByQualified[externalQualified]
					if !exists {
						target = externalNode(externalQualified, externalName, "call target")
						nodesByQualified[externalQualified] = target
						g.Nodes = append(g.Nodes, target)
					}
					targetID = target.ID
				}
				if targetID != "" && targetID != sourceID {
					addEdge(edges, sourceID, targetID, graph.EdgeCalls)
				}
				return true
			})
		}
	}

	if err := indexAdditional(ctx, root, modulePath, files, projectNode, g, edges); err != nil {
		return nil, err
	}
	references, err := serviceReferences(ctx, files)
	if err != nil {
		return nil, err
	}
	g.URLHosts = append(g.URLHosts, references...)
	g.Dependencies = append(g.Dependencies, references...)
	g.Dependencies = uniqueSorted(g.Dependencies)
	g.ImportPaths = uniqueSorted(g.ImportPaths)
	g.URLHosts = uniqueSorted(g.URLHosts)

	for _, edge := range edges {
		g.Edges = append(g.Edges, edge)
	}
	sortGraph(g)
	return g, nil
}

func canonicalRoot(repoPath string) (string, error) {
	if strings.TrimSpace(repoPath) == "" {
		return "", fmt.Errorf("repository path is required")
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("resolve repository path %s: %w", repoPath, err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve repository symlinks %s: %w", abs, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat repository %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository path %s is not a directory", abs)
	}
	return filepath.Clean(abs), nil
}

type sourceFile struct {
	Path     string
	Language string
}

func sourceFiles(ctx context.Context, root string) ([]sourceFile, error) {
	var files []sourceFile
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %s: %w", path, walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() && path != root {
			if _, skip := skippedDirectories[entry.Name()]; skip || strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
				return filepath.SkipDir
			}
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		language := languageForFile(entry.Name())
		if language == "" {
			return nil
		}
		files = append(files, sourceFile{Path: path, Language: language})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover source files in %s: %w", root, err)
	}
	sort.Slice(files, func(a, b int) bool { return files[a].Path < files[b].Path })
	return files, nil
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func readModulePath(path string) (string, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", nil
}

func packageQualified(modulePath, relDir string) string {
	relDir = filepath.ToSlash(filepath.Clean(relDir))
	if relDir == "." || relDir == "" {
		return modulePath
	}
	return strings.TrimSuffix(modulePath, "/") + "/" + relDir
}

func receiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	return expressionName(fn.Recv.List[0].Type)
}

func expressionName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		return expressionName(value.X)
	case *ast.IndexExpr:
		return expressionName(value.X)
	case *ast.IndexListExpr:
		return expressionName(value.X)
	case *ast.SelectorExpr:
		return expressionName(value.X) + "." + value.Sel.Name
	default:
		return ""
	}
}

func typeDetail(spec *ast.TypeSpec) string {
	switch spec.Type.(type) {
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "interface"
	default:
		if spec.Assign.IsValid() {
			return "alias"
		}
		return "type"
	}
}

func functionDetail(fset *token.FileSet, fn *ast.FuncDecl, receiver string) string {
	var signature bytes.Buffer
	if err := format.Node(&signature, fset, fn.Type); err != nil {
		return ""
	}
	if receiver == "" {
		return fn.Name.Name + strings.TrimPrefix(signature.String(), "func")
	}
	return "(" + receiver + ")." + fn.Name.Name + strings.TrimPrefix(signature.String(), "func")
}

func resolveCall(
	expr ast.Expr,
	packageQ string,
	imports map[string]string,
	functions map[string]map[string]string,
	methodsByPackage map[string]map[string][]string,
	methodsByName map[string][]string,
) (targetID, externalQualified, externalName string) {
	switch fun := expr.(type) {
	case *ast.Ident:
		if _, builtin := builtins[fun.Name]; builtin {
			return "", "", ""
		}
		if id := functions[packageQ][fun.Name]; id != "" {
			return id, "", ""
		}
		return "", packageQ + "." + fun.Name, fun.Name
	case *ast.SelectorExpr:
		if ident, ok := fun.X.(*ast.Ident); ok {
			if importPath := imports[ident.Name]; importPath != "" {
				qualified := importPath + "." + fun.Sel.Name
				return "", qualified, fun.Sel.Name
			}
		}
		local := methodsByPackage[packageQ][fun.Sel.Name]
		if len(local) == 1 {
			return local[0], "", ""
		}
		all := methodsByName[fun.Sel.Name]
		if len(all) == 1 {
			return all[0], "", ""
		}
		return "", "method:" + fun.Sel.Name, fun.Sel.Name
	default:
		return "", "", ""
	}
}

func externalNode(qualified, name, detail string) graph.Node {
	return graph.Node{
		ID: nodeID(graph.KindExternal, qualified, "", 0), Kind: graph.KindExternal,
		Name: name, QualifiedName: qualified, Detail: detail,
	}
}

func addEdge(edges map[string]graph.Edge, from, to, kind string) {
	key := from + "\x00" + kind + "\x00" + to
	edges[key] = graph.Edge{From: from, To: to, Kind: kind}
}

func sortGraph(g *graph.Graph) {
	sort.Slice(g.Nodes, func(a, b int) bool {
		if g.Nodes[a].QualifiedName != g.Nodes[b].QualifiedName {
			return g.Nodes[a].QualifiedName < g.Nodes[b].QualifiedName
		}
		if g.Nodes[a].Kind != g.Nodes[b].Kind {
			return g.Nodes[a].Kind < g.Nodes[b].Kind
		}
		return g.Nodes[a].ID < g.Nodes[b].ID
	})
	sort.Slice(g.Edges, func(a, b int) bool {
		if g.Edges[a].From != g.Edges[b].From {
			return g.Edges[a].From < g.Edges[b].From
		}
		if g.Edges[a].Kind != g.Edges[b].Kind {
			return g.Edges[a].Kind < g.Edges[b].Kind
		}
		return g.Edges[a].To < g.Edges[b].To
	})
	sort.Slice(g.Coverage.SkippedFiles, func(a, b int) bool {
		return g.Coverage.SkippedFiles[a].File < g.Coverage.SkippedFiles[b].File
	})
}

func nodeID(kind, qualified, file string, line int) string {
	return strings.ToLower(kind) + ":" + shortHash(kind+"\x00"+qualified+"\x00"+file+"\x00"+strconv.Itoa(line))
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}
