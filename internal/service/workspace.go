package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
	"github.com/vladimirgavrilenko/codebase-graph/internal/repository"
)

// RepositoryState reports one repository discovered in a workspace.
type RepositoryState struct {
	Path      string                `json:"path"`
	Indexed   bool                  `json:"indexed"`
	Project   *graph.ProjectSummary `json:"project,omitempty"`
	GraphPath string                `json:"graph_path"`
}

// DiscoveryResult reports the physical graph layout for a workspace.
type DiscoveryResult struct {
	Workspace        string                `json:"workspace"`
	WorkspaceIndexed bool                  `json:"workspace_indexed"`
	WorkspaceProject *graph.ProjectSummary `json:"workspace_project,omitempty"`
	Repositories     []RepositoryState     `json:"repositories"`
	GraphPath        string                `json:"workspace_graph_path"`
}

// CodeContextResult tells an agent how to use the available graph before broad search.
type CodeContextResult struct {
	DiscoveryResult
	RecommendedNext []string `json:"recommended_next"`
}

// WorkspaceIndexResult describes all repository graphs and their aggregate.
type WorkspaceIndexResult struct {
	Workspace            graph.ProjectSummary `json:"workspace"`
	WorkspaceGraphPath   string               `json:"workspace_graph_path"`
	Repositories         []IndexResult        `json:"repositories"`
	CrossRepositoryEdges int                  `json:"cross_repository_edges"`
}

// DiscoverRepositories finds repository boundaries without modifying indexes.
func (s *Service) DiscoverRepositories(ctx context.Context, workspacePath string) (DiscoveryResult, error) {
	workspace, err := s.workspace(workspacePath)
	if err != nil {
		return DiscoveryResult{}, err
	}
	discovery, err := repository.Discover(ctx, workspace)
	if err != nil {
		return DiscoveryResult{}, err
	}
	result := DiscoveryResult{Workspace: workspace}
	result.GraphPath, err = s.store.WorkspacePath(workspace)
	if err != nil {
		return DiscoveryResult{}, err
	}
	if value, loadErr := s.store.LoadWorkspace(workspace); loadErr == nil {
		summary := value.Summary()
		result.WorkspaceIndexed = true
		result.WorkspaceProject = &summary
	}
	for _, repo := range discovery.Repositories {
		path, pathErr := s.store.Path(repo)
		if pathErr != nil {
			return DiscoveryResult{}, pathErr
		}
		state := RepositoryState{Path: repo, GraphPath: path}
		if value, loadErr := s.store.Load(repo); loadErr == nil {
			summary := value.Summary()
			state.Indexed = true
			state.Project = &summary
		}
		result.Repositories = append(result.Repositories, state)
	}
	return result, nil
}

// PrepareCodeContext reports graph availability and the preferred next tool calls.
func (s *Service) PrepareCodeContext(ctx context.Context, workspacePath string) (CodeContextResult, error) {
	discovery, err := s.DiscoverRepositories(ctx, workspacePath)
	if err != nil {
		return CodeContextResult{}, err
	}
	result := CodeContextResult{DiscoveryResult: discovery}
	if !discovery.WorkspaceIndexed {
		result.RecommendedNext = []string{
			"call index_workspace once before semantic code exploration",
			"use grep only when indexing is unavailable or the task is an exact literal search",
		}
		return result, nil
	}
	result.RecommendedNext = []string{
		"use search_workspace_graph to locate repositories, files, and symbols",
		"use query_workspace_graph for cross-repository DEPENDS_ON relationships",
		"select repo_path, then use search_graph, trace_path, and get_code_snippet for source-level work",
		"use search_code or grep for exact literals or when coverage does not include the needed source",
	}
	return result, nil
}

// IndexWorkspace rebuilds every repository graph and then writes one aggregate graph.
func (s *Service) IndexWorkspace(ctx context.Context, workspacePath string) (WorkspaceIndexResult, error) {
	workspace, err := s.workspace(workspacePath)
	if err != nil {
		return WorkspaceIndexResult{}, err
	}
	discovery, err := repository.Discover(ctx, workspace)
	if err != nil {
		return WorkspaceIndexResult{}, err
	}
	if len(discovery.Repositories) == 0 {
		return WorkspaceIndexResult{}, fmt.Errorf("workspace %s contains no repositories", workspace)
	}

	graphs := make([]*graph.Graph, 0, len(discovery.Repositories))
	result := WorkspaceIndexResult{}
	for _, repo := range discovery.Repositories {
		value, indexErr := s.indexer.Index(ctx, repo)
		if indexErr != nil {
			return WorkspaceIndexResult{}, fmt.Errorf("index repository %s: %w", repo, indexErr)
		}
		if saveErr := s.store.Save(ctx, value); saveErr != nil {
			return WorkspaceIndexResult{}, fmt.Errorf("save repository graph %s: %w", repo, saveErr)
		}
		path, pathErr := s.store.Path(repo)
		if pathErr != nil {
			return WorkspaceIndexResult{}, pathErr
		}
		graphs = append(graphs, value)
		result.Repositories = append(result.Repositories, IndexResult{Project: value.Summary(), GraphPath: path, Coverage: value.Coverage})
	}

	aggregate, crossEdges, err := aggregateWorkspace(workspace, graphs)
	if err != nil {
		return WorkspaceIndexResult{}, err
	}
	if err := s.store.SaveWorkspace(ctx, aggregate); err != nil {
		return WorkspaceIndexResult{}, fmt.Errorf("save workspace graph %s: %w", workspace, err)
	}
	result.Workspace = aggregate.Summary()
	result.WorkspaceGraphPath, err = s.store.WorkspacePath(workspace)
	if err != nil {
		return WorkspaceIndexResult{}, err
	}
	result.CrossRepositoryEdges = crossEdges
	return result, nil
}

// WorkspaceSearch searches the aggregate graph.
func (s *Service) WorkspaceSearch(workspacePath, query, kind string, limit int) (SearchResult, error) {
	value, err := s.loadWorkspace(workspacePath)
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
		if !nodeMatches(node, query) && !strings.Contains(strings.ToLower(node.Detail), query) {
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

// QueryWorkspaceGraph filters relationships in the aggregate graph.
func (s *Service) QueryWorkspaceGraph(workspacePath, from, edgeKind, to string, limit int) (GraphQueryResult, error) {
	value, err := s.loadWorkspace(workspacePath)
	if err != nil {
		return GraphQueryResult{}, err
	}
	return queryValue(value, from, edgeKind, to, limit)
}

// WorkspaceArchitecture summarizes the aggregate graph.
func (s *Service) WorkspaceArchitecture(workspacePath string) (ArchitectureResult, error) {
	value, err := s.loadWorkspace(workspacePath)
	if err != nil {
		return ArchitectureResult{}, err
	}
	return architectureValue(value), nil
}

func (s *Service) loadWorkspace(requested string) (*graph.Graph, error) {
	workspace, err := s.workspace(requested)
	if err != nil {
		return nil, err
	}
	return s.store.LoadWorkspace(workspace)
}

func (s *Service) workspace(requested string) (string, error) {
	if s.fixedWorkspace != "" {
		if requested == "" {
			return s.fixedWorkspace, nil
		}
		resolved, err := canonicalRepo(requested)
		if err != nil {
			return "", err
		}
		if resolved != s.fixedWorkspace {
			return "", fmt.Errorf("server is restricted to workspace %s", s.fixedWorkspace)
		}
		return resolved, nil
	}
	if s.fixedRepo != "" {
		if requested == "" {
			return s.fixedRepo, nil
		}
	}
	return canonicalRepo(requested)
}

func aggregateWorkspace(workspace string, repositories []*graph.Graph) (*graph.Graph, int, error) {
	now := time.Now().UTC()
	for _, repositoryGraph := range repositories {
		if repositoryGraph.IndexedAt.After(now) {
			now = repositoryGraph.IndexedAt
		}
	}
	workspaceNode := graph.Node{ID: aggregateID("workspace", workspace), Kind: graph.KindWorkspace, Name: filepath.Base(workspace), QualifiedName: workspace}
	result := &graph.Graph{
		SchemaVersion: graph.SchemaVersion, ProjectID: shortServiceHash(workspace), Name: filepath.Base(workspace),
		Root: workspace, Module: filepath.Base(workspace), IndexedAt: now, Nodes: []graph.Node{workspaceNode},
		Coverage: graph.Coverage{SupportedExtensions: make([]string, 0), IndexedByLanguage: make(map[string]int)},
	}
	edges := make(map[string]graph.Edge)
	repositoryNodes := make(map[string]graph.Node, len(repositories))
	targets := make(map[string]*graph.Graph, len(repositories))

	for _, repositoryGraph := range repositories {
		relRoot, err := filepath.Rel(workspace, repositoryGraph.Root)
		if err != nil || relRoot == ".." || strings.HasPrefix(relRoot, ".."+string(filepath.Separator)) {
			return nil, 0, fmt.Errorf("repository %s is outside workspace %s", repositoryGraph.Root, workspace)
		}
		repositoryNode := graph.Node{
			ID: aggregateID("repository", repositoryGraph.Root), Kind: graph.KindRepository,
			Name: repositoryGraph.Name, QualifiedName: repositoryGraph.Root, Detail: repositoryGraph.Module,
		}
		repositoryNodes[repositoryGraph.Root] = repositoryNode
		targets[repositoryGraph.Root] = repositoryGraph
		result.Nodes = append(result.Nodes, repositoryNode)
		addWorkspaceEdge(edges, workspaceNode.ID, repositoryNode.ID, graph.EdgeContains)

		idMap := make(map[string]string, len(repositoryGraph.Nodes))
		for _, node := range repositoryGraph.Nodes {
			oldID := node.ID
			node.ID = repositoryGraph.ProjectID + ":" + oldID
			idMap[oldID] = node.ID
			if node.File != "" && relRoot != "." {
				node.File = filepath.ToSlash(filepath.Join(relRoot, filepath.FromSlash(node.File)))
			}
			result.Nodes = append(result.Nodes, node)
			if node.Kind == graph.KindProject {
				addWorkspaceEdge(edges, repositoryNode.ID, node.ID, graph.EdgeContains)
			}
		}
		for _, edge := range repositoryGraph.Edges {
			from, fromOK := idMap[edge.From]
			to, toOK := idMap[edge.To]
			if fromOK && toOK {
				addWorkspaceEdge(edges, from, to, edge.Kind)
			}
		}
		result.Coverage.IndexedFiles += repositoryGraph.Coverage.IndexedFiles
		for _, skipped := range repositoryGraph.Coverage.SkippedFiles {
			if relRoot != "." {
				skipped.File = filepath.ToSlash(filepath.Join(relRoot, filepath.FromSlash(skipped.File)))
			}
			result.Coverage.SkippedFiles = append(result.Coverage.SkippedFiles, skipped)
		}
		result.Coverage.SupportedExtensions = append(result.Coverage.SupportedExtensions, repositoryGraph.Coverage.SupportedExtensions...)
		for language, count := range repositoryGraph.Coverage.IndexedByLanguage {
			result.Coverage.IndexedByLanguage[language] += count
		}
	}

	serviceHosts, err := loadServiceHosts(workspace, targets)
	if err != nil {
		return nil, 0, err
	}

	crossEdges := 0
	for _, source := range repositories {
		for _, importPath := range source.ImportPaths {
			for targetRoot, target := range targets {
				if targetRoot == source.Root || !dependencyMatchesModule(importPath, target.Module, supportsDottedModule(target)) {
					continue
				}
				addWorkspaceEdge(edges, repositoryNodes[source.Root].ID, repositoryNodes[targetRoot].ID, graph.EdgeDependsOn)
			}
		}
		for _, rawHost := range source.URLHosts {
			host, hostErr := normalizeServiceHost(rawHost)
			if hostErr == nil {
				if targetRoot, ok := serviceHosts[host]; ok && targetRoot != source.Root {
					addWorkspaceEdge(edges, repositoryNodes[source.Root].ID, repositoryNodes[targetRoot].ID, graph.EdgeDependsOn)
				}
			}
		}
	}
	for _, edge := range edges {
		result.Edges = append(result.Edges, edge)
		if edge.Kind == graph.EdgeDependsOn {
			crossEdges++
		}
	}
	result.Coverage.SupportedExtensions = uniqueServiceStrings(result.Coverage.SupportedExtensions)
	sort.Slice(result.Nodes, func(a, b int) bool {
		if result.Nodes[a].QualifiedName != result.Nodes[b].QualifiedName {
			return result.Nodes[a].QualifiedName < result.Nodes[b].QualifiedName
		}
		return result.Nodes[a].ID < result.Nodes[b].ID
	})
	sort.Slice(result.Edges, func(a, b int) bool { return edgeKey(result.Edges[a]) < edgeKey(result.Edges[b]) })
	return result, crossEdges, nil
}

func dependencyMatchesModule(dependency, module string, dottedModule bool) bool {
	dependency = strings.TrimSpace(dependency)
	if strings.HasPrefix(dependency, ".") {
		return false
	}
	module = strings.TrimSpace(module)
	if module == "" {
		return false
	}
	if dependency == module || strings.HasPrefix(dependency, module+"/") {
		return true
	}
	return dottedModule && strings.Contains(module, ".") && !strings.Contains(module, "/") && strings.HasPrefix(dependency, module+".")
}

func supportsDottedModule(target *graph.Graph) bool {
	for _, language := range []string{"java", "kotlin", "python", "csharp"} {
		if target.Coverage.IndexedByLanguage[language] > 0 {
			return true
		}
	}
	return false
}

func addWorkspaceEdge(edges map[string]graph.Edge, from, to, kind string) {
	edge := graph.Edge{From: from, To: to, Kind: kind}
	edges[edgeKey(edge)] = edge
}

func aggregateID(kind, value string) string {
	return strings.ToLower(kind) + ":" + shortServiceHash(kind+"\x00"+value)
}

func shortServiceHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

func uniqueServiceStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func queryValue(value *graph.Graph, from, edgeKind, to string, limit int) (GraphQueryResult, error) {
	if from == "" && edgeKind == "" && to == "" {
		return GraphQueryResult{}, fmt.Errorf("at least one graph filter is required")
	}
	limit = boundedLimit(limit, 100, 500)
	from, to, edgeKind = strings.ToLower(from), strings.ToLower(to), strings.ToUpper(edgeKind)
	nodeByID := make(map[string]graph.Node, len(value.Nodes))
	for _, node := range value.Nodes {
		nodeByID[node.ID] = node
	}
	result := GraphQueryResult{}
	for _, edge := range value.Edges {
		fromNode, fromOK := nodeByID[edge.From]
		toNode, toOK := nodeByID[edge.To]
		if !fromOK || !toOK || (from != "" && !nodeMatches(fromNode, from)) || (to != "" && !nodeMatches(toNode, to)) || (edgeKind != "" && edge.Kind != edgeKind) {
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

func architectureValue(value *graph.Graph) ArchitectureResult {
	result := ArchitectureResult{Project: value.Summary(), NodeKinds: make(map[string]int), EdgeKinds: make(map[string]int), ParseErrors: len(value.Coverage.SkippedFiles)}
	nodeByID := make(map[string]graph.Node, len(value.Nodes))
	inDegree, outDegree := make(map[string]int), make(map[string]int)
	for _, node := range value.Nodes {
		nodeByID[node.ID] = node
		result.NodeKinds[node.Kind]++
		if node.Kind == graph.KindPackage {
			result.Packages = append(result.Packages, node.QualifiedName)
		}
	}
	for _, edge := range value.Edges {
		result.EdgeKinds[edge.Kind]++
		if edge.Kind == graph.EdgeCalls || edge.Kind == graph.EdgeDependsOn {
			outDegree[edge.From]++
			inDegree[edge.To]++
		}
	}
	result.TopFanIn, result.TopFanOut = topDegrees(inDegree, nodeByID, 10), topDegrees(outDegree, nodeByID, 10)
	sort.Strings(result.Packages)
	return result
}
