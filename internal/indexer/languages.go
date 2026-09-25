package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

const maximumSourceSize = 4 * 1024 * 1024

var extensionLanguages = map[string]string{
	".go": "go", ".ts": "typescript", ".tsx": "typescript", ".mts": "typescript", ".cts": "typescript", ".js": "javascript",
	".jsx": "javascript", ".mjs": "javascript", ".cjs": "javascript", ".py": "python", ".pyi": "python",
	".sql": "sql", ".sh": "shell", ".bash": "shell", ".zsh": "shell", ".rs": "rust",
	".java": "java", ".kt": "kotlin", ".kts": "kotlin", ".rb": "ruby", ".php": "php",
	".cs": "csharp", ".c": "c", ".cc": "cpp", ".cpp": "cpp", ".h": "c",
	".hpp": "cpp", ".proto": "protobuf", ".html": "html", ".css": "css",
	".scss": "scss", ".vue": "vue", ".svelte": "svelte", ".yaml": "yaml",
	".yml": "yaml", ".toml": "toml", ".json": "json", ".xml": "xml",
}

var (
	jsImport         = regexp.MustCompile(`(?:from\s+|import\s*\(|require\s*\()\s*["']([^"']+)`)
	pythonImport     = regexp.MustCompile(`^\s*(?:from\s+([A-Za-z_][\w.]*)\s+import|import\s+([A-Za-z_][\w.]*))`)
	commonImport     = regexp.MustCompile(`^\s*(?:import|use)\s+(?:static\s+)?([A-Za-z_][\w.:/]*)`)
	includeImport    = regexp.MustCompile(`^\s*#include\s*[<"]([^>"]+)`)
	rubyImport       = regexp.MustCompile(`^\s*require(?:_relative)?\s*["']([^"']+)`)
	phpImport        = regexp.MustCompile(`^\s*(?:use|require(?:_once)?|include(?:_once)?)\s*\(?["']?([^;"')]+)`)
	protoImport      = regexp.MustCompile(`^\s*import\s+["']([^"']+)`)
	sqlDependency    = regexp.MustCompile(`(?i)\b(?:from|join|insert\s+into|update|references)\s+([A-Za-z_][\w.$-]*)`)
	htmlDependency   = regexp.MustCompile(`(?i)\b(?:src|href)=["']([^"'#]+)`)
	callPattern      = regexp.MustCompile(`\b([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*)\s*\(`)
	serviceReference = regexp.MustCompile(`(?i)https?://((?:[a-z0-9][a-z0-9._-]*|\[[0-9a-f:.]+\])(?::[0-9]+)?)`)
)

type declaration struct {
	name   string
	kind   string
	detail string
	line   int
}

func languageForExtension(extension string) string {
	return extensionLanguages[extension]
}

func languageForFile(name string) string {
	switch strings.ToLower(name) {
	case "dockerfile":
		return "dockerfile"
	case "makefile":
		return "makefile"
	case "package-lock.json", "composer.lock":
		return ""
	default:
		return languageForExtension(strings.ToLower(filepath.Ext(name)))
	}
}

func supportedExtensions() []string {
	extensions := make([]string, 0, len(extensionLanguages))
	for extension := range extensionLanguages {
		extensions = append(extensions, extension)
	}
	extensions = append(extensions, "Dockerfile", "Makefile")
	sort.Strings(extensions)
	return extensions
}

func detectModulePath(root string) (string, error) {
	modulePath, err := readModulePath(filepath.Join(root, "go.mod"))
	if err != nil || modulePath != "" {
		return modulePath, err
	}
	payload, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err == nil {
		var manifest struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(payload, &manifest) == nil && strings.TrimSpace(manifest.Name) != "" {
			return strings.TrimSpace(manifest.Name), nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read package.json: %w", err)
	}
	return filepath.Base(root), nil
}

func indexAdditional(
	ctx context.Context,
	root string,
	modulePath string,
	files []sourceFile,
	projectNode graph.Node,
	value *graph.Graph,
	edges map[string]graph.Edge,
) error {
	if err := indexSyntaxCore(ctx, root, modulePath, files, projectNode, value, edges); err != nil {
		return err
	}
	externalIDs := make(map[string]string)
	for _, source := range files {
		if source.Language == "go" || isSyntaxCoreLanguage(source.Language) {
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
		lines := strings.Split(string(payload), "\n")
		fileQualified := modulePath + ":" + rel
		fileNode := graph.Node{
			ID: nodeID(graph.KindFile, fileQualified, rel, 1), Kind: graph.KindFile,
			Name: filepath.Base(rel), QualifiedName: fileQualified, File: rel,
			StartLine: 1, EndLine: max(1, len(lines)), Language: source.Language,
		}
		value.Nodes = append(value.Nodes, fileNode)
		addEdge(edges, projectNode.ID, fileNode.ID, graph.EdgeContains)
		value.Coverage.IndexedFiles++
		value.Coverage.IndexedByLanguage[source.Language]++

		declarations := declarationsFor(source.Language, lines)
		localSymbols := make(map[string]string, len(declarations))
		declarationNodes := make([]graph.Node, 0, len(declarations))
		for index, item := range declarations {
			endLine := len(lines)
			if index+1 < len(declarations) {
				endLine = declarations[index+1].line - 1
			}
			qualified := modulePath + ":" + rel + "#" + item.name
			node := graph.Node{
				ID: nodeID(item.kind, qualified, rel, item.line), Kind: item.kind,
				Name: item.name, QualifiedName: qualified, File: rel,
				StartLine: item.line, EndLine: max(item.line, endLine), Detail: item.detail,
				Language: source.Language,
			}
			value.Nodes = append(value.Nodes, node)
			declarationNodes = append(declarationNodes, node)
			localSymbols[item.name] = node.ID
			addEdge(edges, fileNode.ID, node.ID, graph.EdgeDefines)
		}

		for _, dependency := range dependenciesFor(source.Language, lines) {
			value.Dependencies = append(value.Dependencies, dependency)
			targetID := externalIDs[dependency]
			if targetID == "" {
				target := externalNode(dependency, filepath.Base(dependency), source.Language+" dependency")
				target.Language = source.Language
				value.Nodes = append(value.Nodes, target)
				targetID = target.ID
				externalIDs[dependency] = targetID
			}
			addEdge(edges, fileNode.ID, targetID, graph.EdgeImports)
		}
		value.ImportPaths = append(value.ImportPaths, importPathsFor(source.Language, lines)...)
		for _, node := range declarationNodes {
			for line := node.StartLine; line <= node.EndLine && line <= len(lines); line++ {
				for _, match := range callPattern.FindAllStringSubmatch(lines[line-1], -1) {
					name := match[1]
					shortName := name
					if dot := strings.LastIndex(name, "."); dot >= 0 {
						shortName = name[dot+1:]
					}
					if isCallKeyword(shortName) || shortName == node.Name {
						continue
					}
					targetID := localSymbols[shortName]
					if targetID == "" {
						targetID = externalIDs[name]
						if targetID == "" {
							target := externalNode(name, shortName, source.Language+" call target")
							target.Language = source.Language
							value.Nodes = append(value.Nodes, target)
							targetID = target.ID
							externalIDs[name] = targetID
						}
					}
					addEdge(edges, node.ID, targetID, graph.EdgeCalls)
				}
			}
		}
	}
	return nil
}

func importPathsFor(language string, lines []string) []string {
	var pattern *regexp.Regexp
	switch language {
	case "vue", "svelte":
		pattern = jsImport
	case "c", "cpp":
		pattern = includeImport
	case "ruby":
		pattern = rubyImport
	case "php":
		pattern = phpImport
	case "protobuf":
		pattern = protoImport
	default:
		return nil
	}
	var result []string
	for _, line := range lines {
		if match := pattern.FindStringSubmatch(line); len(match) > 1 {
			if value := strings.TrimSpace(match[1]); value != "" {
				result = append(result, value)
			}
		}
	}
	return uniqueSorted(result)
}

func declarationsFor(language string, lines []string) []declaration {
	var patterns []*regexp.Regexp
	switch language {
	case "typescript", "javascript", "vue", "svelte":
		patterns = []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)`),
			regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:abstract\s+)?(?:class|interface|type|enum)\s+([A-Za-z_$][\w$]*)`),
			regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::[^=]+)?=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*=>`),
		}
	case "python":
		patterns = []*regexp.Regexp{regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)`), regexp.MustCompile(`^\s*class\s+([A-Za-z_]\w*)`)}
	case "sql":
		patterns = []*regexp.Regexp{regexp.MustCompile(`(?i)^\s*create\s+(?:or\s+replace\s+)?(?:table|view|function|procedure|trigger)\s+(?:if\s+not\s+exists\s+)?([A-Za-z_][\w.$-]*)`)}
	case "rust":
		patterns = []*regexp.Regexp{regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+([A-Za-z_]\w*)`), regexp.MustCompile(`^\s*(?:pub\s+)?(?:struct|enum|trait)\s+([A-Za-z_]\w*)`)}
	case "ruby":
		patterns = []*regexp.Regexp{regexp.MustCompile(`^\s*def\s+(?:self\.)?([A-Za-z_]\w*[!?=]?)`), regexp.MustCompile(`^\s*(?:class|module)\s+([A-Za-z_:]\w*)`)}
	case "shell":
		patterns = []*regexp.Regexp{regexp.MustCompile(`^\s*(?:function\s+)?([A-Za-z_]\w*)\s*\(\)\s*\{`)}
	case "php":
		patterns = []*regexp.Regexp{regexp.MustCompile(`(?i)^\s*(?:(?:public|private|protected|static|final|abstract)\s+)*function\s+([A-Za-z_]\w*)`), regexp.MustCompile(`(?i)^\s*(?:final\s+|abstract\s+)?(?:class|interface|trait)\s+([A-Za-z_]\w*)`)}
	case "java", "kotlin", "csharp", "c", "cpp":
		patterns = []*regexp.Regexp{regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|final|abstract|virtual|override|async|suspend|inline|extern)\s+)*(?:class|interface|enum|struct|record)\s+([A-Za-z_]\w*)`), regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|final|virtual|override|async|suspend|inline|extern|const)\s+)*(?:[A-Za-z_][\w<>,.*?\[\]:]*\s+)+([A-Za-z_]\w*)\s*\([^;]*\)\s*(?:\{|=>)?\s*$`)}
	case "protobuf":
		patterns = []*regexp.Regexp{regexp.MustCompile(`^\s*(?:message|service|enum|rpc)\s+([A-Za-z_]\w*)`)}
	}
	var result []declaration
	for index, line := range lines {
		for patternIndex, pattern := range patterns {
			match := pattern.FindStringSubmatch(line)
			if len(match) < 2 {
				continue
			}
			kind := graph.KindFunction
			lower := strings.ToLower(strings.TrimSpace(line))
			if strings.Contains(lower, "class ") || strings.Contains(lower, "interface ") || strings.Contains(lower, "type ") || strings.Contains(lower, "enum ") || strings.Contains(lower, "struct ") || strings.Contains(lower, "trait ") || strings.Contains(lower, "message ") || strings.Contains(lower, "service ") || strings.Contains(lower, "module ") || (language == "sql" && patternIndex == 0) {
				kind = graph.KindType
			}
			result = append(result, declaration{name: match[1], kind: kind, detail: strings.TrimSpace(line), line: index + 1})
			break
		}
	}
	return result
}

func dependenciesFor(language string, lines []string) []string {
	var result []string
	for _, line := range lines {
		var patterns []*regexp.Regexp
		switch language {
		case "typescript", "javascript", "vue", "svelte":
			patterns = []*regexp.Regexp{jsImport}
		case "python":
			patterns = []*regexp.Regexp{pythonImport}
		case "rust", "java", "kotlin", "csharp":
			patterns = []*regexp.Regexp{commonImport}
		case "c", "cpp":
			patterns = []*regexp.Regexp{includeImport}
		case "ruby":
			patterns = []*regexp.Regexp{rubyImport}
		case "php":
			patterns = []*regexp.Regexp{phpImport}
		case "protobuf":
			patterns = []*regexp.Regexp{protoImport}
		case "sql":
			patterns = []*regexp.Regexp{sqlDependency}
		case "html", "css", "scss":
			patterns = []*regexp.Regexp{htmlDependency}
		case "yaml", "toml", "json", "xml", "dockerfile", "makefile":
			patterns = []*regexp.Regexp{serviceReference}
		}
		for _, pattern := range patterns {
			for _, match := range pattern.FindAllStringSubmatch(line, -1) {
				for index := 1; index < len(match); index++ {
					if strings.TrimSpace(match[index]) != "" {
						result = append(result, strings.TrimSpace(match[index]))
						break
					}
				}
			}
		}
	}
	return uniqueSorted(result)
}

func serviceReferences(ctx context.Context, files []sourceFile) ([]string, error) {
	var result []string
	for _, source := range files {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("scan service references: %w", err)
		}
		info, err := os.Stat(source.Path)
		if err != nil {
			return nil, fmt.Errorf("stat source file %s: %w", source.Path, err)
		}
		if info.Size() > maximumSourceSize {
			continue
		}
		payload, err := os.ReadFile(source.Path)
		if err != nil {
			return nil, fmt.Errorf("read source file %s: %w", source.Path, err)
		}
		for _, match := range serviceReference.FindAllStringSubmatch(string(payload), -1) {
			if len(match) > 1 {
				result = append(result, match[1])
			}
		}
	}
	return uniqueSorted(result), nil
}

func isCallKeyword(name string) bool {
	switch name {
	case "if", "for", "while", "switch", "catch", "function", "func", "def", "class", "return", "sizeof", "typeof":
		return true
	default:
		return false
	}
}
