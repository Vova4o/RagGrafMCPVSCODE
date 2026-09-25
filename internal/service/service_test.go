package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

func TestServiceIndexesSearchesAndTraces(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeServiceFile(t, filepath.Join(root, "go.mod"), "module example.com/service\n")
	writeServiceFile(t, filepath.Join(root, "service.go"), `package service

func Handle() { Save() }
func Save() {}
`)

	graphService, err := New(root)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	indexed, err := graphService.Index(context.Background(), "")
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	if indexed.Project.Nodes == 0 || indexed.GraphPath != filepath.Join(canonicalRoot, ".codebase-graph", "graph.json") {
		t.Fatalf("Index() = %#v", indexed)
	}

	found, err := graphService.Search("", "Handle", graph.KindFunction, 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(found.Matches) != 1 || found.Matches[0].QualifiedName != "example.com/service.Handle" {
		t.Fatalf("Search() = %#v", found)
	}

	trace, err := graphService.Trace("", "example.com/service.Handle", "outbound", 1, 20, []string{graph.EdgeCalls})
	if err != nil {
		t.Fatalf("Trace() error = %v", err)
	}
	if !traceContains(trace, "example.com/service.Save") {
		t.Fatalf("Trace() does not contain Save: %#v", trace)
	}

	snippet, err := graphService.Snippet("", "example.com/service.Handle", 0)
	if err != nil {
		t.Fatalf("Snippet() error = %v", err)
	}
	if !strings.Contains(snippet.Code, "func Handle()") {
		t.Fatalf("Snippet().Code = %q", snippet.Code)
	}
}

func TestServiceRejectsDifferentRepository(t *testing.T) {
	t.Parallel()
	graphService, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = graphService.Index(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("Index() error = %v, want repository restriction", err)
	}
}

func TestServiceClassTraceAggregatesMethodCallersAndCallees(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeServiceFile(t, filepath.Join(root, "package.json"), `{"name":"class-trace-fixture"}`)
	writeServiceFile(t, filepath.Join(root, "engine.ts"), "export class Engine {\n"+
		"  start() { return helper(); }\n"+
		"  helper() { return 1; }\n"+
		"}\n"+
		"export function launch() { const engine = new Engine(); engine.start(); }\n")

	graphService, err := New(root)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := graphService.Index(context.Background(), ""); err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	typeSearch, err := graphService.Search("", "Engine", graph.KindType, 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(typeSearch.Matches) != 1 {
		t.Fatalf("Type search matches = %#v, want one Engine", typeSearch.Matches)
	}

	trace, err := graphService.Trace("", typeSearch.Matches[0].QualifiedName, "both", 2, 20, []string{graph.EdgeCalls})
	if err != nil {
		t.Fatalf("Trace() error = %v", err)
	}
	for _, name := range []string{"launch", "helper"} {
		if !traceContainsName(trace, name) {
			t.Errorf("class trace does not contain %q caller/callee: %#v", name, trace.Nodes)
		}
	}
}

func TestIndexWorkspaceCreatesIndividualAndAggregateGraphs(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	api := filepath.Join(workspace, "api")
	web := filepath.Join(workspace, "web")
	for _, repo := range []string{api, web} {
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeServiceFile(t, filepath.Join(api, "go.mod"), "module example.com/api\n")
	writeServiceFile(t, filepath.Join(api, "api.go"), "package api\nfunc Serve() {}\n")
	writeServiceFile(t, filepath.Join(web, "package.json"), `{"name":"example-web"}`)
	writeServiceFile(t, filepath.Join(web, "app.ts"), `import { Serve } from "example.com/api/client";
export function start() { Serve(); }
`)

	graphService, err := NewWorkspace(workspace)
	if err != nil {
		t.Fatalf("NewWorkspace() error = %v", err)
	}
	before, err := graphService.PrepareCodeContext(context.Background(), "")
	if err != nil {
		t.Fatalf("PrepareCodeContext() before index error = %v", err)
	}
	if before.WorkspaceIndexed || len(before.RecommendedNext) == 0 || !strings.Contains(before.RecommendedNext[0], "index_workspace") {
		t.Fatalf("PrepareCodeContext() before index = %#v", before)
	}
	result, err := graphService.IndexWorkspace(context.Background(), "")
	if err != nil {
		t.Fatalf("IndexWorkspace() error = %v", err)
	}
	if len(result.Repositories) != 2 || result.CrossRepositoryEdges != 1 {
		t.Fatalf("IndexWorkspace() = %#v", result)
	}
	after, err := graphService.PrepareCodeContext(context.Background(), "")
	if err != nil {
		t.Fatalf("PrepareCodeContext() after index error = %v", err)
	}
	if !after.WorkspaceIndexed || after.WorkspaceProject == nil || len(after.RecommendedNext) < 3 {
		t.Fatalf("PrepareCodeContext() after index = %#v", after)
	}
	for _, path := range []string{
		filepath.Join(api, ".codebase-graph", "graph.json"),
		filepath.Join(web, ".codebase-graph", "graph.json"),
		filepath.Join(workspace, ".codebase-graph", "workspace.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("graph %s was not written: %v", path, err)
		}
	}
	dependencies, err := graphService.QueryWorkspaceGraph("", "web", graph.EdgeDependsOn, "api", 10)
	if err != nil {
		t.Fatalf("QueryWorkspaceGraph() error = %v", err)
	}
	if dependencies.Total != 1 {
		t.Fatalf("dependency edges = %#v", dependencies)
	}
	if _, err := graphService.Index(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "repo_path is required") {
		t.Fatalf("Index() error = %v, want multi-repository selection error", err)
	}
}

func TestIndexWorkspaceDoesNotInferDependencyFromHostOrAssetName(t *testing.T) {
	t.Parallel()
	workspace, api, web := newWorkspaceRepositories(t)
	writeServiceFile(t, filepath.Join(web, "config.ts"), `const apiURL = "https://api.example.test:8080";
const logo = "api-logo.svg";
`)

	graphService, err := NewWorkspace(workspace)
	if err != nil {
		t.Fatalf("NewWorkspace() error = %v", err)
	}
	if _, err := graphService.IndexWorkspace(context.Background(), ""); err != nil {
		t.Fatalf("IndexWorkspace() error = %v", err)
	}
	dependencies, err := graphService.QueryWorkspaceGraph("", "web", graph.EdgeDependsOn, "api", 10)
	if err != nil {
		t.Fatalf("QueryWorkspaceGraph() error = %v", err)
	}
	if dependencies.Total != 0 {
		t.Fatalf("dependency edges = %#v, want none for unconfigured host and asset references (api: %s)", dependencies, api)
	}
}

func TestIndexWorkspaceDoesNotInferDependencyFromSQLTableName(t *testing.T) {
	t.Parallel()
	workspace, api, web := newWorkspaceRepositories(t)
	writeServiceFile(t, filepath.Join(api, "go.mod"), "module api\n")
	writeServiceFile(t, filepath.Join(web, "schema.sql"), "SELECT * FROM api;\n")

	graphService, err := NewWorkspace(workspace)
	if err != nil {
		t.Fatalf("NewWorkspace() error = %v", err)
	}
	if _, err := graphService.IndexWorkspace(context.Background(), ""); err != nil {
		t.Fatalf("IndexWorkspace() error = %v", err)
	}
	indexedWeb, err := graphService.store.Load(web)
	if err != nil {
		t.Fatalf("load indexed web repository: %v", err)
	}
	foundDependency := false
	for _, dependency := range indexedWeb.Dependencies {
		if dependency == "api" {
			foundDependency = true
			break
		}
	}
	if !foundDependency {
		t.Fatalf("indexed dependencies = %#v, want SQL dependency api", indexedWeb.Dependencies)
	}
	dependencies, err := graphService.QueryWorkspaceGraph("", "web", graph.EdgeDependsOn, "api", 10)
	if err != nil {
		t.Fatalf("QueryWorkspaceGraph() error = %v", err)
	}
	if dependencies.Total != 0 {
		t.Fatalf("dependency edges = %#v, want none for a SQL table named after the repository", dependencies)
	}
}

func TestIndexWorkspaceDoesNotInferDependencyFromUnresolvedRelativeImport(t *testing.T) {
	t.Parallel()
	workspace, api, web := newWorkspaceRepositories(t)
	writeServiceFile(t, filepath.Join(api, "go.mod"), "module api\n")
	writeServiceFile(t, filepath.Join(web, "app.ts"), `import { call } from "./api";
export function start() { return call(); }
`)

	graphService, err := NewWorkspace(workspace)
	if err != nil {
		t.Fatalf("NewWorkspace() error = %v", err)
	}
	if _, err := graphService.IndexWorkspace(context.Background(), ""); err != nil {
		t.Fatalf("IndexWorkspace() error = %v", err)
	}
	indexedWeb, err := graphService.store.Load(web)
	if err != nil {
		t.Fatalf("load indexed web repository: %v", err)
	}
	foundImport := false
	for _, importPath := range indexedWeb.ImportPaths {
		if importPath == "./api" {
			foundImport = true
			break
		}
	}
	if !foundImport {
		t.Fatalf("indexed import paths = %#v, want unresolved relative import ./api", indexedWeb.ImportPaths)
	}
	dependencies, err := graphService.QueryWorkspaceGraph("", "web", graph.EdgeDependsOn, "api", 10)
	if err != nil {
		t.Fatalf("QueryWorkspaceGraph() error = %v", err)
	}
	if dependencies.Total != 0 {
		t.Fatalf("dependency edges = %#v, want none for unresolved relative import", dependencies)
	}
}

func TestIndexWorkspaceRejectsUnknownServiceHostRepository(t *testing.T) {
	t.Parallel()
	workspace, _, _ := newWorkspaceRepositories(t)
	if err := os.MkdirAll(filepath.Join(workspace, "missing"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeServiceFile(t, filepath.Join(workspace, "codebase-graph.services.json"), `{
  "services": [{"repository": "missing", "hosts": ["api.example.test"]}]
}
`)

	graphService, err := NewWorkspace(workspace)
	if err != nil {
		t.Fatalf("NewWorkspace() error = %v", err)
	}
	_, err = graphService.IndexWorkspace(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "does not match a target graph") {
		t.Fatalf("IndexWorkspace() error = %v, want unknown repository error", err)
	}
}

func TestIndexWorkspaceRejectsConflictingNormalizedServiceHosts(t *testing.T) {
	t.Parallel()
	workspace, _, _ := newWorkspaceRepositories(t)
	writeServiceFile(t, filepath.Join(workspace, "codebase-graph.services.json"), `{
  "services": [
    {"repository": "api", "hosts": ["API.example.test:8080"]},
    {"repository": "web", "hosts": ["api.example.test:8080"]}
  ]
}
`)

	graphService, err := NewWorkspace(workspace)
	if err != nil {
		t.Fatalf("NewWorkspace() error = %v", err)
	}
	_, err = graphService.IndexWorkspace(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "assigned to multiple repositories") {
		t.Fatalf("IndexWorkspace() error = %v, want conflicting host assignment error", err)
	}
}

func TestIndexWorkspaceMapsExplicitServiceHostToRepository(t *testing.T) {
	t.Parallel()
	workspace, _, web := newWorkspaceRepositories(t)
	writeServiceFile(t, filepath.Join(workspace, "codebase-graph.services.json"), `{
  "services": [{"repository": "api", "hosts": ["api.example.test:8080"]}]
}
`)
	writeServiceFile(t, filepath.Join(web, "client.ts"), `export async function load() {
  return fetch("https://api.example.test:8080/v1/items");
}
`)

	graphService, err := NewWorkspace(workspace)
	if err != nil {
		t.Fatalf("NewWorkspace() error = %v", err)
	}
	if _, err := graphService.IndexWorkspace(context.Background(), ""); err != nil {
		t.Fatalf("IndexWorkspace() error = %v", err)
	}
	dependencies, err := graphService.QueryWorkspaceGraph("", "web", graph.EdgeDependsOn, "api", 10)
	if err != nil {
		t.Fatalf("QueryWorkspaceGraph() error = %v", err)
	}
	if dependencies.Total != 1 {
		t.Fatalf("dependency edges = %#v, want one web-to-api edge", dependencies)
	}
}

func newWorkspaceRepositories(t *testing.T) (workspace, api, web string) {
	t.Helper()
	workspace = t.TempDir()
	api = filepath.Join(workspace, "api")
	web = filepath.Join(workspace, "web")
	for _, repo := range []string{api, web} {
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeServiceFile(t, filepath.Join(api, "go.mod"), "module example.com/api\n")
	writeServiceFile(t, filepath.Join(api, "api.go"), "package api\nfunc Serve() {}\n")
	writeServiceFile(t, filepath.Join(web, "package.json"), `{"name":"example-web"}`)
	return workspace, api, web
}

func traceContains(result TraceResult, qualified string) bool {
	for _, node := range result.Nodes {
		if node.QualifiedName == qualified {
			return true
		}
	}
	return false
}

func traceContainsName(result TraceResult, name string) bool {
	for _, node := range result.Nodes {
		if node.Name == name {
			return true
		}
	}
	return false
}

func writeServiceFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
