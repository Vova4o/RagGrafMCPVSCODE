// Package repository discovers actual source repositories inside a workspace.
package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var skippedDirectories = map[string]struct{}{
	".agents": {}, ".claude": {}, ".codebase-graph": {}, ".idea": {}, ".vscode": {},
	".worktrees": {}, "node_modules": {}, "vendor": {}, "dist": {}, "build": {},
}

var projectMarkers = []string{
	"go.mod", "package.json", "pyproject.toml", "requirements.txt", "Cargo.toml",
	"pom.xml", "build.gradle", "build.gradle.kts", "composer.json", "Gemfile",
}

// Discovery describes one workspace and the repositories found below it.
type Discovery struct {
	Workspace    string   `json:"workspace"`
	Repositories []string `json:"repositories"`
}

// Discover finds independent Git repositories. A Git repository opened directly
// is one repository; an umbrella folder yields each nested repository. A folder
// without Git metadata is treated as a repository when it has a project marker.
func Discover(ctx context.Context, workspacePath string) (Discovery, error) {
	workspace, err := canonicalDirectory(workspacePath)
	if err != nil {
		return Discovery{}, err
	}
	if isGitRepository(workspace) {
		return Discovery{Workspace: workspace, Repositories: []string{workspace}}, nil
	}

	var repositories []string
	err = filepath.WalkDir(workspace, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk workspace entry %s: %w", path, walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if path != workspace {
			if _, skip := skippedDirectories[entry.Name()]; skip || strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
		}
		if path != workspace && isGitRepository(path) {
			repositories = append(repositories, path)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return Discovery{}, fmt.Errorf("discover repositories in %s: %w", workspace, err)
	}
	if len(repositories) == 0 && hasProjectMarker(workspace) {
		repositories = append(repositories, workspace)
	}
	sort.Strings(repositories)
	return Discovery{Workspace: workspace, Repositories: repositories}, nil
}

func isGitRepository(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

func hasProjectMarker(path string) bool {
	for _, marker := range projectMarkers {
		if _, err := os.Stat(filepath.Join(path, marker)); err == nil {
			return true
		}
	}
	return false
}

func canonicalDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("workspace path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path %s: %w", path, err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve workspace symlinks %s: %w", abs, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat workspace %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path %s is not a directory", abs)
	}
	return filepath.Clean(abs), nil
}
