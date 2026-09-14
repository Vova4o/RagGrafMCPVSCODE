package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

func TestStoreSaveLoadDelete(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	value := testGraph(root, time.Unix(100, 0).UTC())
	graphStore := New()

	if err := graphStore.Save(context.Background(), value); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := graphStore.Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.ProjectID != value.ProjectID || len(loaded.Nodes) != 1 {
		t.Fatalf("Load() = %#v, want project %q with one node", loaded, value.ProjectID)
	}

	if err := graphStore.Delete(context.Background(), root); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, dataDirectory, graphFile)); !os.IsNotExist(err) {
		t.Fatalf("graph file still exists after Delete(): %v", err)
	}
}

func TestStoreConcurrentWritesRemainValid(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	graphStore := New()
	var wait sync.WaitGroup
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			value := testGraph(root, time.Unix(int64(index), 0).UTC())
			if err := graphStore.Save(context.Background(), value); err != nil {
				t.Errorf("Save(%d) error = %v", index, err)
			}
		}(index)
	}
	wait.Wait()
	loaded, err := graphStore.Load(root)
	if err != nil {
		t.Fatalf("Load() after concurrent writes error = %v", err)
	}
	if loaded.SchemaVersion != graph.SchemaVersion || len(loaded.Nodes) != 1 {
		t.Fatalf("loaded graph is invalid: %#v", loaded)
	}
}

func TestSaveAddsGraphDirectoryToLocalGitExclude(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	excludePath := filepath.Join(root, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(excludePath, []byte("# local patterns\n*.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	graphStore := New()
	if err := graphStore.Save(context.Background(), testGraph(root, time.Now())); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}
	if err := graphStore.Save(context.Background(), testGraph(root, time.Now())); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}
	payload, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "# local patterns\n*.tmp\n.codebase-graph/\n" {
		t.Fatalf("exclude content = %q", payload)
	}
}

func TestSaveUsesCommonGitDirectoryForWorktree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	common := filepath.Join(t.TempDir(), "common")
	worktreeGitDir := filepath.Join(common, "worktrees", "feature")
	if err := os.MkdirAll(worktreeGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+worktreeGitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeGitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := New().Save(context.Background(), testGraph(root, time.Now())); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	payload, err := os.ReadFile(filepath.Join(common, "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != ".codebase-graph/\n" {
		t.Fatalf("exclude content = %q", payload)
	}
}

func testGraph(root string, indexedAt time.Time) *graph.Graph {
	return &graph.Graph{
		SchemaVersion: graph.SchemaVersion,
		ProjectID:     "project",
		Name:          "repo",
		Root:          root,
		Module:        "example.com/repo",
		IndexedAt:     indexedAt,
		Nodes:         []graph.Node{{ID: "project:1", Kind: graph.KindProject, Name: "repo", QualifiedName: root}},
	}
}
