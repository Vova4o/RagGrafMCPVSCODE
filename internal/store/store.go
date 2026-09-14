// Package store persists one graph inside each indexed repository.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

const (
	dataDirectory = ".codebase-graph"
	ignorePattern = ".codebase-graph/"
	graphFile     = "graph.json"
	workspaceFile = "workspace.json"
	lockFile      = "graph.lock"
)

// Store reads and writes repository-local graph indexes.
type Store struct{}

// New returns a repository-local graph store.
func New() *Store {
	return &Store{}
}

// Save atomically persists a graph in its repository.
func (s *Store) Save(ctx context.Context, value *graph.Graph) error {
	return s.save(ctx, value, graphFile)
}

// SaveWorkspace atomically persists an aggregate graph in its workspace.
func (s *Store) SaveWorkspace(ctx context.Context, value *graph.Graph) error {
	return s.save(ctx, value, workspaceFile)
}

func (s *Store) save(ctx context.Context, value *graph.Graph, filename string) error {
	if value == nil {
		return fmt.Errorf("graph is required")
	}
	root, err := canonicalRoot(value.Root)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, dataDirectory)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create graph directory %s: %w", dir, err)
	}

	return withLock(ctx, filepath.Join(dir, lockFile), func() error {
		if err := ensureLocallyIgnored(root); err != nil {
			return err
		}
		persisted := *value
		persisted.Root = root
		payload, err := json.Marshal(&persisted)
		if err != nil {
			return fmt.Errorf("encode graph for %s: %w", root, err)
		}
		temporary, err := os.CreateTemp(dir, ".graph-*.json")
		if err != nil {
			return fmt.Errorf("create temporary graph in %s: %w", dir, err)
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)

		if err := temporary.Chmod(0o600); err != nil {
			temporary.Close()
			return fmt.Errorf("set graph permissions %s: %w", temporaryPath, err)
		}
		if _, err := temporary.Write(payload); err != nil {
			temporary.Close()
			return fmt.Errorf("write graph %s: %w", temporaryPath, err)
		}
		if err := temporary.Sync(); err != nil {
			temporary.Close()
			return fmt.Errorf("sync graph %s: %w", temporaryPath, err)
		}
		if err := temporary.Close(); err != nil {
			return fmt.Errorf("close graph %s: %w", temporaryPath, err)
		}
		if err := os.Rename(temporaryPath, filepath.Join(dir, filename)); err != nil {
			return fmt.Errorf("replace graph for %s: %w", root, err)
		}
		return nil
	})
}

func ensureLocallyIgnored(root string) error {
	excludePath, exists, err := gitExcludePath(root)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	payload, err := os.ReadFile(excludePath)
	mode := os.FileMode(0o644)
	if err == nil {
		info, statErr := os.Stat(excludePath)
		if statErr != nil {
			return fmt.Errorf("stat git exclude file %s: %w", excludePath, statErr)
		}
		mode = info.Mode().Perm()
		for _, line := range strings.Split(string(payload), "\n") {
			switch strings.TrimSpace(line) {
			case ".codebase-graph/", "/.codebase-graph/", ".codebase-graph", "/.codebase-graph":
				return nil
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read git exclude file %s: %w", excludePath, err)
	}

	if len(payload) > 0 && payload[len(payload)-1] != '\n' {
		payload = append(payload, '\n')
	}
	payload = append(payload, []byte(ignorePattern+"\n")...)
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("create git info directory %s: %w", filepath.Dir(excludePath), err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(excludePath), ".exclude-*")
	if err != nil {
		return fmt.Errorf("create temporary git exclude file in %s: %w", filepath.Dir(excludePath), err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return fmt.Errorf("set git exclude permissions %s: %w", temporaryPath, err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write git exclude file %s: %w", temporaryPath, err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync git exclude file %s: %w", temporaryPath, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close git exclude file %s: %w", temporaryPath, err)
	}
	if err := os.Rename(temporaryPath, excludePath); err != nil {
		return fmt.Errorf("replace git exclude file %s: %w", excludePath, err)
	}
	return nil
}

func gitExcludePath(root string) (string, bool, error) {
	gitPath := filepath.Join(root, ".git")
	info, err := os.Stat(gitPath)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("stat git metadata %s: %w", gitPath, err)
	}
	gitDir := gitPath
	if !info.IsDir() {
		payload, readErr := os.ReadFile(gitPath)
		if readErr != nil {
			return "", false, fmt.Errorf("read git metadata pointer %s: %w", gitPath, readErr)
		}
		line := strings.TrimSpace(string(payload))
		if !strings.HasPrefix(line, "gitdir:") {
			return "", false, fmt.Errorf("git metadata pointer %s is invalid", gitPath)
		}
		gitDir = strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
	}
	commonDirPath := filepath.Join(gitDir, "commondir")
	if payload, readErr := os.ReadFile(commonDirPath); readErr == nil {
		commonDir := strings.TrimSpace(string(payload))
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
		gitDir = filepath.Clean(commonDir)
	} else if !os.IsNotExist(readErr) {
		return "", false, fmt.Errorf("read git common directory %s: %w", commonDirPath, readErr)
	}
	return filepath.Join(filepath.Clean(gitDir), "info", "exclude"), true, nil
}

// LoadWorkspace reads the aggregate graph stored in workspacePath.
func (s *Store) LoadWorkspace(workspacePath string) (*graph.Graph, error) {
	return s.load(workspacePath, workspaceFile, "workspace")
}

// Load reads the graph stored in repoPath.
func (s *Store) Load(repoPath string) (*graph.Graph, error) {
	return s.load(repoPath, graphFile, "repository")
}

func (s *Store) load(repoPath, filename, scope string) (*graph.Graph, error) {
	root, err := canonicalRoot(repoPath)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, dataDirectory, filename)
	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s %s has not been indexed", scope, root)
		}
		return nil, fmt.Errorf("read graph %s: %w", path, err)
	}

	var value graph.Graph
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, fmt.Errorf("decode graph %s: %w", path, err)
	}
	if value.SchemaVersion != graph.SchemaVersion {
		return nil, fmt.Errorf("graph %s uses schema version %d but this server supports %d; restart the client to load the installed MCP version and rebuild only if the error remains", path, value.SchemaVersion, graph.SchemaVersion)
	}
	if filepath.Clean(value.Root) != root {
		return nil, fmt.Errorf("graph %s belongs to repository %s", path, value.Root)
	}
	return &value, nil
}

// Delete removes the repository-local graph index.
func (s *Store) Delete(ctx context.Context, repoPath string) error {
	root, err := canonicalRoot(repoPath)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, dataDirectory)
	return withLock(ctx, filepath.Join(dir, lockFile), func() error {
		path := filepath.Join(dir, graphFile)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete graph %s: %w", path, err)
		}
		return nil
	})
}

// Path returns the expected graph path for a repository.
func (s *Store) Path(repoPath string) (string, error) {
	root, err := canonicalRoot(repoPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, dataDirectory, graphFile), nil
}

// WorkspacePath returns the expected aggregate graph path for a workspace.
func (s *Store) WorkspacePath(workspacePath string) (string, error) {
	root, err := canonicalRoot(workspacePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, dataDirectory, workspaceFile), nil
}

func canonicalRoot(repoPath string) (string, error) {
	if repoPath == "" {
		return "", fmt.Errorf("repository path is required")
	}
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("resolve repository path %s: %w", repoPath, err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository symlinks %s: %w", root, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("stat repository %s: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository path %s is not a directory", root)
	}
	return filepath.Clean(root), nil
}

func withLock(ctx context.Context, path string, action func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create lock directory %s: %w", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open graph lock %s: %w", path, err)
	}
	defer file.Close()

	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return fmt.Errorf("lock graph %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for graph lock %s: %w", path, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("use graph lock %s: %w", path, err)
	}
	return action()
}
