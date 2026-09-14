// Package service implements graph indexing and query operations.
package service

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
	"github.com/vladimirgavrilenko/codebase-graph/internal/indexer"
	"github.com/vladimirgavrilenko/codebase-graph/internal/repository"
	"github.com/vladimirgavrilenko/codebase-graph/internal/store"
)

// Service coordinates repository and workspace indexing and queries.
type Service struct {
	indexer        *indexer.Indexer
	store          *store.Store
	fixedRepo      string
	fixedWorkspace string
}

// NewWorkspace returns a service scoped to a workspace. Repository operations
// accept any repository discovered directly below that workspace.
func NewWorkspace(workspacePath string) (*Service, error) {
	resolved, err := canonicalRepo(workspacePath)
	if err != nil {
		return nil, err
	}
	return &Service{indexer: indexer.New(), store: store.New(), fixedWorkspace: resolved}, nil
}

// New returns a graph service. When fixedRepo is non-empty, all operations are
// restricted to that repository.
func New(fixedRepo string) (*Service, error) {
	resolved := ""
	if fixedRepo != "" {
		var err error
		resolved, err = canonicalRepo(fixedRepo)
		if err != nil {
			return nil, err
		}
	}
	return &Service{indexer: indexer.New(), store: store.New(), fixedRepo: resolved}, nil
}

// IndexResult describes a completed repository index.
type IndexResult struct {
	Project   graph.ProjectSummary `json:"project"`
	GraphPath string               `json:"graph_path"`
	Coverage  graph.Coverage       `json:"coverage"`
}

// Index builds and persists the graph for a repository.
func (s *Service) Index(ctx context.Context, repoPath string) (IndexResult, error) {
	repo, err := s.repo(repoPath)
	if err != nil {
		return IndexResult{}, err
	}
	value, err := s.indexer.Index(ctx, repo)
	if err != nil {
		return IndexResult{}, fmt.Errorf("index repository %s: %w", repo, err)
	}
	if err := s.store.Save(ctx, value); err != nil {
		return IndexResult{}, fmt.Errorf("save repository graph %s: %w", repo, err)
	}
	path, err := s.store.Path(repo)
	if err != nil {
		return IndexResult{}, err
	}
	return IndexResult{Project: value.Summary(), GraphPath: path, Coverage: value.Coverage}, nil
}

// StatusResult describes a repository-local graph.
type StatusResult struct {
	Indexed   bool                  `json:"indexed"`
	Project   *graph.ProjectSummary `json:"project,omitempty"`
	GraphPath string                `json:"graph_path"`
}

// Status reports whether a repository has a graph.
func (s *Service) Status(repoPath string) (StatusResult, error) {
	repo, err := s.repo(repoPath)
	if err != nil {
		return StatusResult{}, err
	}
	path, err := s.store.Path(repo)
	if err != nil {
		return StatusResult{}, err
	}
	value, err := s.store.Load(repo)
	if err != nil {
		if strings.Contains(err.Error(), "has not been indexed") {
			return StatusResult{Indexed: false, GraphPath: path}, nil
		}
		return StatusResult{}, err
	}
	summary := value.Summary()
	return StatusResult{Indexed: true, Project: &summary, GraphPath: path}, nil
}

// SearchResult contains matching graph nodes.
type SearchResult struct {
	Matches   []graph.Node `json:"matches"`
	Total     int          `json:"total"`
	Truncated bool         `json:"truncated"`
}

// Search finds nodes by case-insensitive literal text.
func (s *Service) Search(repoPath, query, kind string, limit int) (SearchResult, error) {
	value, err := s.load(repoPath)
	if err != nil {
		return SearchResult{}, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	kind = strings.ToLower(strings.TrimSpace(kind))
	if query == "" {
		return SearchResult{}, fmt.Errorf("search query is required")
	}
	limit = boundedLimit(limit, 50, 200)

	result := SearchResult{}
	for _, node := range value.Nodes {
		if kind != "" && strings.ToLower(node.Kind) != kind {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{
			node.Name, node.QualifiedName, node.File, node.Detail,
		}, "\n"))
		if !strings.Contains(haystack, query) {
			continue
		}
		result.Total++
		if len(result.Matches) < limit {
			result.Matches = append(result.Matches, node)
		}
	}
	result.Truncated = result.Total > len(result.Matches)
	return result, nil
}

// TraceResult is a bounded call or dependency traversal.
type TraceResult struct {
	Root      graph.Node   `json:"root"`
	Nodes     []graph.Node `json:"nodes"`
	Edges     []graph.Edge `json:"edges"`
	Direction string       `json:"direction"`
	Depth     int          `json:"depth"`
	Truncated bool         `json:"truncated"`
}

// Trace traverses matching edges around a symbol.
func (s *Service) Trace(repoPath, symbol, direction string, depth, limit int, edgeKinds []string) (TraceResult, error) {
	value, err := s.load(repoPath)
	if err != nil {
		return TraceResult{}, err
	}
	root, err := resolveSymbol(value, symbol)
	if err != nil {
		return TraceResult{}, err
	}
	direction = strings.ToLower(strings.TrimSpace(direction))
	if direction == "" {
		direction = "both"
	}
	if direction != "inbound" && direction != "outbound" && direction != "both" {
		return TraceResult{}, fmt.Errorf("direction must be inbound, outbound, or both")
	}
	if depth == 0 {
		depth = 3
	}
	if depth < 1 || depth > 8 {
		return TraceResult{}, fmt.Errorf("depth must be between 1 and 8")
	}
	limit = boundedLimit(limit, 200, 1000)

	allowedKinds := make(map[string]struct{}, len(edgeKinds))
	for _, kind := range edgeKinds {
		allowedKinds[strings.ToUpper(kind)] = struct{}{}
	}
	nodeByID := make(map[string]graph.Node, len(value.Nodes))
	for _, node := range value.Nodes {
		nodeByID[node.ID] = node
	}

	seenNodes := map[string]struct{}{root.ID: {}}
	seenEdges := make(map[string]graph.Edge)
	frontier := []string{root.ID}
	truncated := false
	for level := 0; level < depth && len(frontier) > 0; level++ {
		nextSet := make(map[string]struct{})
		for _, current := range frontier {
			for _, edge := range value.Edges {
				if len(allowedKinds) > 0 {
					if _, ok := allowedKinds[edge.Kind]; !ok {
						continue
					}
				}
				neighbor := ""
				if (direction == "outbound" || direction == "both") && edge.From == current {
					neighbor = edge.To
				}
				if (direction == "inbound" || direction == "both") && edge.To == current {
					neighbor = edge.From
				}
				if neighbor == "" {
					continue
				}
				if len(seenEdges) >= limit {
					truncated = true
					continue
				}
				seenEdges[edgeKey(edge)] = edge
				if _, exists := seenNodes[neighbor]; !exists {
					seenNodes[neighbor] = struct{}{}
					nextSet[neighbor] = struct{}{}
				}
			}
		}
		frontier = frontier[:0]
		for id := range nextSet {
			frontier = append(frontier, id)
		}
		sort.Strings(frontier)
	}

	result := TraceResult{Root: root, Direction: direction, Depth: depth, Truncated: truncated}
	for id := range seenNodes {
		if node, exists := nodeByID[id]; exists {
			result.Nodes = append(result.Nodes, node)
		}
	}
	for _, edge := range seenEdges {
		result.Edges = append(result.Edges, edge)
	}
	sort.Slice(result.Nodes, func(a, b int) bool { return result.Nodes[a].QualifiedName < result.Nodes[b].QualifiedName })
	sort.Slice(result.Edges, func(a, b int) bool { return edgeKey(result.Edges[a]) < edgeKey(result.Edges[b]) })
	return result, nil
}

// SnippetResult contains a symbol and its current source text.
type SnippetResult struct {
	Symbol    graph.Node `json:"symbol"`
	StartLine int        `json:"start_line"`
	EndLine   int        `json:"end_line"`
	Code      string     `json:"code"`
}

// Snippet reads a bounded source excerpt for a graph symbol.
func (s *Service) Snippet(repoPath, symbol string, contextLines int) (SnippetResult, error) {
	value, err := s.load(repoPath)
	if err != nil {
		return SnippetResult{}, err
	}
	node, err := resolveSymbol(value, symbol)
	if err != nil {
		return SnippetResult{}, err
	}
	if node.File == "" || node.StartLine == 0 {
		return SnippetResult{}, fmt.Errorf("symbol %s has no repository source", node.QualifiedName)
	}
	if contextLines < 0 || contextLines > 20 {
		return SnippetResult{}, fmt.Errorf("context_lines must be between 0 and 20")
	}

	path := filepath.Join(value.Root, filepath.FromSlash(node.File))
	if err := ensureInside(value.Root, path); err != nil {
		return SnippetResult{}, err
	}
	lines, err := readLines(path)
	if err != nil {
		return SnippetResult{}, err
	}
	start := max(1, node.StartLine-contextLines)
	end := min(len(lines), node.EndLine+contextLines)
	if start > end || start > len(lines) {
		return SnippetResult{}, fmt.Errorf("symbol %s has invalid source range", node.QualifiedName)
	}
	return SnippetResult{
		Symbol: node, StartLine: start, EndLine: end,
		Code: strings.Join(lines[start-1:end], "\n"),
	}, nil
}

// ArchitectureResult summarizes the indexed repository.
type ArchitectureResult struct {
	Project     graph.ProjectSummary `json:"project"`
	NodeKinds   map[string]int       `json:"node_kinds"`
	EdgeKinds   map[string]int       `json:"edge_kinds"`
	Packages    []string             `json:"packages"`
	TopFanIn    []Degree             `json:"top_fan_in"`
	TopFanOut   []Degree             `json:"top_fan_out"`
	ParseErrors int                  `json:"parse_errors"`
}

// Degree is a symbol and its edge count.
type Degree struct {
	QualifiedName string `json:"qualified_name"`
	Count         int    `json:"count"`
}

// Architecture returns compact structural statistics.
func (s *Service) Architecture(repoPath string) (ArchitectureResult, error) {
	value, err := s.load(repoPath)
	if err != nil {
		return ArchitectureResult{}, err
	}
	result := ArchitectureResult{
		Project: value.Summary(), NodeKinds: make(map[string]int), EdgeKinds: make(map[string]int),
		ParseErrors: len(value.Coverage.SkippedFiles),
	}
	nodeByID := make(map[string]graph.Node, len(value.Nodes))
	inDegree := make(map[string]int)
	outDegree := make(map[string]int)
	for _, node := range value.Nodes {
		nodeByID[node.ID] = node
		result.NodeKinds[node.Kind]++
		if node.Kind == graph.KindPackage && !strings.HasPrefix(node.QualifiedName, "method:") {
			result.Packages = append(result.Packages, node.QualifiedName)
		}
	}
	for _, edge := range value.Edges {
		result.EdgeKinds[edge.Kind]++
		if edge.Kind == graph.EdgeCalls {
			outDegree[edge.From]++
			inDegree[edge.To]++
		}
	}
	result.TopFanIn = topDegrees(inDegree, nodeByID, 10)
	result.TopFanOut = topDegrees(outDegree, nodeByID, 10)
	sort.Strings(result.Packages)
	return result, nil
}

// CoverageResult reports indexing coverage for a repository.
type CoverageResult struct {
	Project   graph.ProjectSummary `json:"project"`
	GraphPath string               `json:"graph_path"`
	Coverage  graph.Coverage       `json:"coverage"`
}

// Coverage returns the recorded index coverage.
func (s *Service) Coverage(repoPath string) (CoverageResult, error) {
	value, err := s.load(repoPath)
	if err != nil {
		return CoverageResult{}, err
	}
	path, err := s.store.Path(value.Root)
	if err != nil {
		return CoverageResult{}, err
	}
	return CoverageResult{Project: value.Summary(), GraphPath: path, Coverage: value.Coverage}, nil
}

// GraphQueryResult contains edges matching structured filters.
type GraphQueryResult struct {
	Edges     []ExpandedEdge `json:"edges"`
	Total     int            `json:"total"`
	Truncated bool           `json:"truncated"`
}

// ExpandedEdge includes both endpoint nodes.
type ExpandedEdge struct {
	From graph.Node `json:"from"`
	Kind string     `json:"kind"`
	To   graph.Node `json:"to"`
}

// QueryGraph filters edges by endpoint text and edge kind.
func (s *Service) QueryGraph(repoPath, from, edgeKind, to string, limit int) (GraphQueryResult, error) {
	value, err := s.load(repoPath)
	if err != nil {
		return GraphQueryResult{}, err
	}
	if from == "" && edgeKind == "" && to == "" {
		return GraphQueryResult{}, fmt.Errorf("at least one graph filter is required")
	}
	limit = boundedLimit(limit, 100, 500)
	from = strings.ToLower(from)
	to = strings.ToLower(to)
	edgeKind = strings.ToUpper(edgeKind)
	nodeByID := make(map[string]graph.Node, len(value.Nodes))
	for _, node := range value.Nodes {
		nodeByID[node.ID] = node
	}
	result := GraphQueryResult{}
	for _, edge := range value.Edges {
		fromNode, fromOK := nodeByID[edge.From]
		toNode, toOK := nodeByID[edge.To]
		if !fromOK || !toOK {
			continue
		}
		if from != "" && !nodeMatches(fromNode, from) {
			continue
		}
		if to != "" && !nodeMatches(toNode, to) {
			continue
		}
		if edgeKind != "" && edge.Kind != edgeKind {
			continue
		}
		result.Total++
		if len(result.Edges) < limit {
			result.Edges = append(result.Edges, ExpandedEdge{From: fromNode, Kind: edge.Kind, To: toNode})
		}
	}
	result.Truncated = result.Total > len(result.Edges)
	return result, nil
}

// CodeMatch is one literal source-code match.
type CodeMatch struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// SearchCodeResult contains literal matches in indexed source files.
type SearchCodeResult struct {
	Matches   []CodeMatch `json:"matches"`
	Total     int         `json:"total"`
	Truncated bool        `json:"truncated"`
}

// SearchCode searches the currently indexed file set.
func (s *Service) SearchCode(repoPath, query string, limit int) (SearchCodeResult, error) {
	value, err := s.load(repoPath)
	if err != nil {
		return SearchCodeResult{}, err
	}
	if query == "" {
		return SearchCodeResult{}, fmt.Errorf("code query is required")
	}
	limit = boundedLimit(limit, 100, 500)
	files := make(map[string]struct{})
	for _, node := range value.Nodes {
		if node.Kind == graph.KindFile && node.File != "" {
			files[node.File] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(files))
	for file := range files {
		ordered = append(ordered, file)
	}
	sort.Strings(ordered)

	result := SearchCodeResult{}
	for _, rel := range ordered {
		path := filepath.Join(value.Root, filepath.FromSlash(rel))
		if err := ensureInside(value.Root, path); err != nil {
			return SearchCodeResult{}, err
		}
		file, err := os.Open(path)
		if err != nil {
			return SearchCodeResult{}, fmt.Errorf("open indexed file %s: %w", path, err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		line := 0
		for scanner.Scan() {
			line++
			if !strings.Contains(scanner.Text(), query) {
				continue
			}
			result.Total++
			if len(result.Matches) < limit {
				result.Matches = append(result.Matches, CodeMatch{File: rel, Line: line, Text: scanner.Text()})
			}
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return SearchCodeResult{}, fmt.Errorf("scan indexed file %s: %w", path, scanErr)
		}
		if closeErr != nil {
			return SearchCodeResult{}, fmt.Errorf("close indexed file %s: %w", path, closeErr)
		}
	}
	result.Truncated = result.Total > len(result.Matches)
	return result, nil
}

// Delete removes a repository's graph.
func (s *Service) Delete(ctx context.Context, repoPath string) error {
	repo, err := s.repo(repoPath)
	if err != nil {
		return err
	}
	if err := s.store.Delete(ctx, repo); err != nil {
		return fmt.Errorf("delete repository graph %s: %w", repo, err)
	}
	return nil
}

func (s *Service) load(repoPath string) (*graph.Graph, error) {
	repo, err := s.repo(repoPath)
	if err != nil {
		return nil, err
	}
	value, err := s.store.Load(repo)
	if err != nil {
		return nil, err
	}
	return value, nil
}

func (s *Service) repo(requested string) (string, error) {
	if s.fixedRepo != "" {
		if requested == "" {
			return s.fixedRepo, nil
		}
		resolved, err := canonicalRepo(requested)
		if err != nil {
			return "", err
		}
		if resolved != s.fixedRepo {
			return "", fmt.Errorf("server is restricted to repository %s", s.fixedRepo)
		}
		return s.fixedRepo, nil
	}
	if s.fixedWorkspace != "" {
		discovery, err := repository.Discover(context.Background(), s.fixedWorkspace)
		if err != nil {
			return "", err
		}
		if requested == "" {
			if len(discovery.Repositories) == 1 {
				return discovery.Repositories[0], nil
			}
			if len(discovery.Repositories) == 0 {
				return "", fmt.Errorf("workspace %s contains no repositories", s.fixedWorkspace)
			}
			return "", fmt.Errorf("repo_path is required because workspace %s contains %d repositories: %s", s.fixedWorkspace, len(discovery.Repositories), strings.Join(discovery.Repositories, ", "))
		}
		resolved, err := canonicalRepo(requested)
		if err != nil {
			return "", err
		}
		for _, repo := range discovery.Repositories {
			if resolved == repo {
				return resolved, nil
			}
		}
		return "", fmt.Errorf("repository %s is outside workspace %s", resolved, s.fixedWorkspace)
	}
	return canonicalRepo(requested)
}

func canonicalRepo(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("repo_path is required for an unscoped server")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve repository path %s: %w", path, err)
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

func resolveSymbol(value *graph.Graph, symbol string) (graph.Node, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return graph.Node{}, fmt.Errorf("symbol is required")
	}
	var exact []graph.Node
	for _, node := range value.Nodes {
		if node.ID == symbol || node.QualifiedName == symbol {
			return node, nil
		}
		if node.Name == symbol {
			exact = append(exact, node)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	if len(exact) > 1 {
		return graph.Node{}, ambiguousSymbol(symbol, exact)
	}

	needle := strings.ToLower(symbol)
	var partial []graph.Node
	for _, node := range value.Nodes {
		if strings.Contains(strings.ToLower(node.QualifiedName), needle) {
			partial = append(partial, node)
		}
	}
	if len(partial) == 1 {
		return partial[0], nil
	}
	if len(partial) > 1 {
		return graph.Node{}, ambiguousSymbol(symbol, partial)
	}
	return graph.Node{}, fmt.Errorf("symbol %s was not found", symbol)
}

func ambiguousSymbol(symbol string, nodes []graph.Node) error {
	qualified := make([]string, 0, min(len(nodes), 10))
	for index, node := range nodes {
		if index == 10 {
			break
		}
		qualified = append(qualified, node.QualifiedName)
	}
	return fmt.Errorf("symbol %s is ambiguous: %s", symbol, strings.Join(qualified, ", "))
}

func nodeMatches(node graph.Node, query string) bool {
	return strings.Contains(strings.ToLower(node.Name), query) ||
		strings.Contains(strings.ToLower(node.QualifiedName), query) ||
		strings.Contains(strings.ToLower(node.File), query)
}

func edgeKey(edge graph.Edge) string {
	return edge.From + "\x00" + edge.Kind + "\x00" + edge.To
}

func topDegrees(degrees map[string]int, nodes map[string]graph.Node, limit int) []Degree {
	result := make([]Degree, 0, len(degrees))
	for id, count := range degrees {
		if node, exists := nodes[id]; exists {
			result = append(result, Degree{QualifiedName: node.QualifiedName, Count: count})
		}
	}
	sort.Slice(result, func(a, b int) bool {
		if result[a].Count != result[b].Count {
			return result[a].Count > result[b].Count
		}
		return result[a].QualifiedName < result[b].QualifiedName
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

func boundedLimit(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	return min(value, maximum)
}

func ensureInside(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("resolve source path %s: %w", path, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("source path %s escapes repository %s", path, root)
	}
	return nil
}

func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open source file %s: %w", path, err)
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read source file %s: %w", path, err)
	}
	return lines, nil
}
