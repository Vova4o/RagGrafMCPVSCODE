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
	"github.com/vladimirgavrilenko/codebase-graph/internal/indexer"
	"github.com/vladimirgavrilenko/codebase-graph/internal/repository"
)

// RepositoryState reports one repository discovered in a workspace.
type RepositoryState struct {
	Path    string `json:"path"`
	Indexed bool   `json:"indexed"`
	// Stale is true when on-disk source no longer matches this repository's
	// persisted graph. Discovery never rebuilds; it only reports the state.
	Stale     bool                  `json:"stale"`
	Project   *graph.ProjectSummary `json:"project,omitempty"`
	GraphPath string                `json:"graph_path"`
}

// DiscoveryResult reports the physical graph layout for a workspace.
type DiscoveryResult struct {
	Workspace        string                `json:"workspace"`
	WorkspaceIndexed bool                  `json:"workspace_indexed"`
	WorkspaceProject *graph.ProjectSummary `json:"workspace_project,omitempty"`
	// WorkspaceStale is true when the discovered repositories or the
	// service-host configuration no longer match the aggregate graph.
	WorkspaceStale bool              `json:"workspace_stale"`
	Repositories   []RepositoryState `json:"repositories"`
	GraphPath      string            `json:"workspace_graph_path"`
}

// CodeContextResult tells an agent how to use the available graph before broad search.
type CodeContextResult struct {
	DiscoveryResult
	// Refreshed is true when a stale indexed workspace was rebuilt as part
	// of preparing this context.
	Refreshed       bool     `json:"refreshed"`
	RecommendedNext []string `json:"recommended_next"`
}

// WorkspaceIndexResult describes all repository graphs and their aggregate.
type WorkspaceIndexResult struct {
	Workspace            graph.ProjectSummary `json:"workspace"`
	WorkspaceGraphPath   string               `json:"workspace_graph_path"`
	Repositories         []IndexResult        `json:"repositories"`
	CrossRepositoryEdges int                  `json:"cross_repository_edges"`
}

// resolveWorkspaceState discovers repository boundaries for a workspace and
// resolves each discovered repository's staleness exactly once. Every caller
// that needs both facts for a single request must go through this helper
// instead of calling repository.Discover or resolveWorkspaceRepositories
// directly, so each is computed at most once per request.
func (s *Service) resolveWorkspaceState(ctx context.Context, requested string) (string, []workspaceRepoResolution, error) {
	workspace, err := s.workspace(requested)
	if err != nil {
		return "", nil, err
	}
	discovery, err := repository.Discover(ctx, workspace)
	if err != nil {
		return "", nil, err
	}
	resolutions, err := s.resolveWorkspaceRepositories(ctx, discovery.Repositories)
	if err != nil {
		return "", nil, err
	}
	return workspace, resolutions, nil
}

// buildDiscoveryResult assembles a DiscoveryResult from already-resolved
// repository state. It performs no repository.Discover or Fingerprint calls.
func (s *Service) buildDiscoveryResult(workspace string, resolutions []workspaceRepoResolution) (DiscoveryResult, error) {
	result := DiscoveryResult{Workspace: workspace}
	var err error
	result.GraphPath, err = s.store.WorkspacePath(workspace)
	if err != nil {
		return DiscoveryResult{}, err
	}

	if workspaceGraph, loadErr := s.store.LoadWorkspace(workspace); loadErr == nil {
		summary := workspaceGraph.Summary()
		result.WorkspaceIndexed = true
		result.WorkspaceProject = &summary
		result.WorkspaceStale, err = workspaceIsStale(workspace, resolutions, workspaceGraph)
		if err != nil {
			return DiscoveryResult{}, err
		}
	}

	for _, resolution := range resolutions {
		path, pathErr := s.store.Path(resolution.repo)
		if pathErr != nil {
			return DiscoveryResult{}, pathErr
		}
		state := RepositoryState{Path: resolution.repo, GraphPath: path}
		if resolution.graph != nil {
			summary := resolution.graph.Summary()
			state.Indexed = true
			state.Project = &summary
			state.Stale = !resolution.current
		}
		result.Repositories = append(result.Repositories, state)
	}
	return result, nil
}

// DiscoverRepositories finds repository boundaries without modifying indexes.
func (s *Service) DiscoverRepositories(ctx context.Context, workspacePath string) (DiscoveryResult, error) {
	workspace, resolutions, err := s.resolveWorkspaceState(ctx, workspacePath)
	if err != nil {
		return DiscoveryResult{}, err
	}
	return s.buildDiscoveryResult(workspace, resolutions)
}

// PrepareCodeContext reports graph availability and the preferred next tool calls.
// A stale indexed workspace is refreshed in place before recommendations are
// returned, so callers never act on a graph known to be out of date.
func (s *Service) PrepareCodeContext(ctx context.Context, workspacePath string) (CodeContextResult, error) {
	workspace, resolutions, err := s.resolveWorkspaceState(ctx, workspacePath)
	if err != nil {
		return CodeContextResult{}, err
	}
	discovery, err := s.buildDiscoveryResult(workspace, resolutions)
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
	if discovery.WorkspaceStale {
		_, refreshedResolutions, err := s.rebuildWorkspace(ctx, workspace, resolutions)
		if err != nil {
			return CodeContextResult{}, err
		}
		result.Refreshed = true
		result.DiscoveryResult, err = s.buildDiscoveryResult(workspace, refreshedResolutions)
		if err != nil {
			return CodeContextResult{}, err
		}
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
		value, indexErr := s.indexAndSave(ctx, repo)
		if indexErr != nil {
			return WorkspaceIndexResult{}, indexErr
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
func (s *Service) WorkspaceSearch(ctx context.Context, workspacePath, query, kind string, limit int) (SearchResult, error) {
	value, err := s.loadWorkspace(ctx, workspacePath)
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
func (s *Service) QueryWorkspaceGraph(ctx context.Context, workspacePath, from, edgeKind, to string, limit int) (GraphQueryResult, error) {
	value, err := s.loadWorkspace(ctx, workspacePath)
	if err != nil {
		return GraphQueryResult{}, err
	}
	return queryValue(value, from, edgeKind, to, limit)
}

// WorkspaceArchitecture summarizes the aggregate graph.
func (s *Service) WorkspaceArchitecture(ctx context.Context, workspacePath string) (ArchitectureResult, error) {
	value, err := s.loadWorkspace(ctx, workspacePath)
	if err != nil {
		return ArchitectureResult{}, err
	}
	return architectureValue(value), nil
}

// workspaceRepoResolution reports, for one discovered repository, whether its
// persisted graph exists and still matches the current on-disk source.
type workspaceRepoResolution struct {
	repo    string
	graph   *graph.Graph // nil only when the repository has never been indexed
	current bool
	// fingerprint and hasFingerprint carry the already-computed source
	// fingerprint (when the repository has a persisted graph), so a rebuild
	// triggered later in the same request can reuse it instead of computing
	// it again.
	fingerprint    string
	hasFingerprint bool
}

// resolveWorkspaceRepositories loads each discovered repository's own graph
// and compares it against the live source fingerprint, without rebuilding
// anything. Callers decide whether to act on the result.
func (s *Service) resolveWorkspaceRepositories(ctx context.Context, discovered []string) ([]workspaceRepoResolution, error) {
	resolutions := make([]workspaceRepoResolution, 0, len(discovered))
	for _, repo := range discovered {
		value, err := s.store.Load(repo)
		if err != nil {
			resolutions = append(resolutions, workspaceRepoResolution{repo: repo})
			continue
		}
		fingerprint, err := s.fingerprint(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("compute source fingerprint for %s: %w", repo, err)
		}
		resolutions = append(resolutions, workspaceRepoResolution{
			repo: repo, graph: value, current: fingerprint == value.SourceFingerprint,
			fingerprint: fingerprint, hasFingerprint: true,
		})
	}
	return resolutions, nil
}

// rebuildWorkspace rebuilds only the repositories whose resolution is not
// current, re-aggregates, and persists the result. Repositories with an
// already-computed fingerprint reuse it instead of recomputing, so no
// repository's Fingerprint is computed more than once per request. It
// returns the updated per-repository resolutions (all current) alongside the
// aggregate graph so callers can report fresh state without recomputing.
func (s *Service) rebuildWorkspace(ctx context.Context, workspace string, resolutions []workspaceRepoResolution) (*graph.Graph, []workspaceRepoResolution, error) {
	graphs := make([]*graph.Graph, 0, len(resolutions))
	updated := make([]workspaceRepoResolution, len(resolutions))
	for i, resolution := range resolutions {
		if resolution.graph != nil && resolution.current {
			graphs = append(graphs, resolution.graph)
			updated[i] = resolution
			continue
		}
		var rebuilt *graph.Graph
		var err error
		if resolution.hasFingerprint {
			rebuilt, err = s.indexAndSaveWithFingerprint(ctx, resolution.repo, resolution.fingerprint)
		} else {
			rebuilt, err = s.indexAndSave(ctx, resolution.repo)
		}
		if err != nil {
			return nil, nil, err
		}
		graphs = append(graphs, rebuilt)
		updated[i] = workspaceRepoResolution{
			repo: resolution.repo, graph: rebuilt, current: true,
			fingerprint: rebuilt.SourceFingerprint, hasFingerprint: true,
		}
	}

	aggregate, _, err := aggregateWorkspace(workspace, graphs)
	if err != nil {
		return nil, nil, err
	}
	if err := s.store.SaveWorkspace(ctx, aggregate); err != nil {
		return nil, nil, fmt.Errorf("save workspace graph %s: %w", workspace, err)
	}
	return aggregate, updated, nil
}

// workspaceIsStale reports whether an aggregate graph must be rebuilt: any
// discovered repository is missing or changed since it was last indexed, or
// the resolved repository set together with the service-host configuration
// no longer hashes to the aggregate graph's recorded fingerprint.
func workspaceIsStale(workspace string, resolutions []workspaceRepoResolution, stored *graph.Graph) (bool, error) {
	freshGraphs := make([]*graph.Graph, 0, len(resolutions))
	for _, resolution := range resolutions {
		if resolution.graph == nil || !resolution.current {
			return true, nil
		}
		freshGraphs = append(freshGraphs, resolution.graph)
	}
	expected, err := workspaceFingerprint(workspace, freshGraphs)
	if err != nil {
		return false, err
	}
	return expected != stored.SourceFingerprint, nil
}

// workspaceFingerprint hashes the repository graphs actually aggregated
// together with the service-host configuration file, so a repository being
// added or removed, or the host mapping changing, is always detected even
// when no individual repository's own source changed.
func workspaceFingerprint(workspace string, repositories []*graph.Graph) (string, error) {
	lines := make([]string, 0, len(repositories)+1)
	for _, repositoryGraph := range repositories {
		lines = append(lines, repositoryGraph.Root+"\t"+repositoryGraph.SourceFingerprint)
	}
	hostLine, err := indexer.EntryLine(workspace, filepath.Join(workspace, serviceHostsFile))
	if err != nil {
		return "", fmt.Errorf("compute service host fingerprint for %s: %w", workspace, err)
	}
	lines = append(lines, hostLine)
	sort.Strings(lines)
	hasher := sha256.New()
	for _, line := range lines {
		hasher.Write([]byte(line))
		hasher.Write([]byte("\n"))
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// loadWorkspace returns the current aggregate graph for workspacePath,
// rebuilding only the repositories whose source changed and re-aggregating
// when the workspace as a whole is stale.
func (s *Service) loadWorkspace(ctx context.Context, requested string) (*graph.Graph, error) {
	workspace, resolutions, err := s.resolveWorkspaceState(ctx, requested)
	if err != nil {
		return nil, err
	}
	if len(resolutions) == 0 {
		return nil, fmt.Errorf("workspace %s contains no repositories", workspace)
	}
	value, err := s.store.LoadWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	stale, err := workspaceIsStale(workspace, resolutions, value)
	if err != nil {
		return nil, err
	}
	if !stale {
		return value, nil
	}

	aggregate, _, err := s.rebuildWorkspace(ctx, workspace, resolutions)
	return aggregate, err
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
	fingerprint, err := workspaceFingerprint(workspace, repositories)
	if err != nil {
		return nil, 0, err
	}
	result.SourceFingerprint = fingerprint
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
