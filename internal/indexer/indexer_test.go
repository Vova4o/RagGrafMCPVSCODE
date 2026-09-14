package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

func TestIndexBuildsGoCallGraph(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(root, "main.go"), `package demo

import "fmt"

type Store struct{}

func (Store) Save() {}

func Create() {
	helper()
	Store{}.Save()
	fmt.Println("created")
}

func helper() {}
`)

	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if value.Module != "example.com/demo" {
		t.Fatalf("Module = %q, want example.com/demo", value.Module)
	}
	if value.Coverage.IndexedFiles != 1 {
		t.Fatalf("IndexedFiles = %d, want 1", value.Coverage.IndexedFiles)
	}

	ids := make(map[string]string)
	for _, node := range value.Nodes {
		ids[node.ID] = node.QualifiedName
	}
	wanted := map[string]bool{
		"example.com/demo.Create->example.com/demo.helper":     false,
		"example.com/demo.Create->example.com/demo.Store.Save": false,
		"example.com/demo.Create->fmt.Println":                 false,
	}
	for _, edge := range value.Edges {
		if edge.Kind != graph.EdgeCalls {
			continue
		}
		key := ids[edge.From] + "->" + ids[edge.To]
		if _, exists := wanted[key]; exists {
			wanted[key] = true
		}
	}
	for relation, found := range wanted {
		if !found {
			t.Errorf("missing call edge %s", relation)
		}
	}
}

func TestIndexRecordsParseFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/broken\n")
	writeTestFile(t, filepath.Join(root, "broken.go"), "package broken\nfunc {\n")

	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if len(value.Coverage.SkippedFiles) != 1 {
		t.Fatalf("SkippedFiles = %d, want 1", len(value.Coverage.SkippedFiles))
	}
	if value.Coverage.SkippedFiles[0].File != "broken.go" {
		t.Fatalf("skipped file = %q, want broken.go", value.Coverage.SkippedFiles[0].File)
	}
}

func TestIndexBuildsMultiLanguageGraph(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "package.json"), `{"name":"@example/web"}`)
	writeTestFile(t, filepath.Join(root, "src", "app.tsx"), `import { client } from "@example/api/client";
export function App() { return client(); }
export const load = async () => client();
`)
	writeTestFile(t, filepath.Join(root, "tools", "build.py"), "import requests\n\ndef build():\n    requests.get('x')\n")
	writeTestFile(t, filepath.Join(root, "migrations", "001.sql"), "CREATE TABLE users (id bigint);\nCREATE VIEW active_users AS SELECT * FROM users;\n")

	value, err := New().Index(context.Background(), root)
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if value.Module != "@example/web" {
		t.Fatalf("Module = %q", value.Module)
	}
	for language, count := range map[string]int{"typescript": 1, "python": 1, "sql": 1} {
		if value.Coverage.IndexedByLanguage[language] != count {
			t.Errorf("IndexedByLanguage[%q] = %d, want %d", language, value.Coverage.IndexedByLanguage[language], count)
		}
	}
	if !containsString(value.Dependencies, "@example/api/client") || !containsString(value.Dependencies, "requests") {
		t.Fatalf("Dependencies = %#v", value.Dependencies)
	}
	foundApp := false
	for _, node := range value.Nodes {
		if node.Name == "App" && node.Kind == graph.KindFunction && node.Language == "typescript" {
			foundApp = true
		}
	}
	if !foundApp {
		t.Fatal("typescript App function was not indexed")
	}
}

func TestServiceReferencesRequireExplicitURL(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "config.go")
	writeTestFile(t, path, "package config\n// mmemo-gateway is the caller, not a dependency\nconst upstream = \"http://mmemo-chat:8080\"\n")

	references, err := serviceReferences(context.Background(), []sourceFile{{Path: path, Language: "go"}})
	if err != nil {
		t.Fatalf("serviceReferences() error = %v", err)
	}
	if len(references) != 1 || references[0] != "mmemo-chat" {
		t.Fatalf("references = %#v, want mmemo-chat only", references)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
