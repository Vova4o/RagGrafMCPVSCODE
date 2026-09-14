package repository

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverNestedRepositories(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	want := []string{filepath.Join(workspace, "api"), filepath.Join(workspace, "web")}
	for _, path := range want {
		if err := os.MkdirAll(filepath.Join(path, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".worktrees", "ignored", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(context.Background(), workspace)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	for index := range want {
		want[index], err = filepath.EvalSymlinks(want[index])
		if err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(got.Repositories, want) {
		t.Fatalf("repositories = %#v, want %#v", got.Repositories, want)
	}
}

func TestDiscoverDirectRepositoryDoesNotDescend(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	for _, path := range []string{filepath.Join(workspace, ".git"), filepath.Join(workspace, "nested", ".git")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Discover(context.Background(), workspace)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Repositories, []string{workspace}) {
		t.Fatalf("repositories = %#v", got.Repositories)
	}
}
