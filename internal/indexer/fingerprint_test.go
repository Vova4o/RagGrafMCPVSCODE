package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFingerprintStableForUnchangedTree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFingerprintFile(t, filepath.Join(root, "go.mod"), "module example.com/fp\n")
	writeFingerprintFile(t, filepath.Join(root, "main.go"), "package fp\nfunc Run() {}\n")

	first, err := Fingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	second, err := Fingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if first != second {
		t.Fatalf("Fingerprint() = %q, then %q, want stable hash for an unchanged tree", first, second)
	}
}

func TestFingerprintChangesOnContentEditWithSameSizeAndDifferentModTime(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFingerprintFile(t, filepath.Join(root, "go.mod"), "module example.com/fp\n")
	target := filepath.Join(root, "main.go")
	writeFingerprintFile(t, target, "package fp\nfunc RunA() {}\n")

	before, err := Fingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}

	writeFingerprintFile(t, target, "package fp\nfunc RunB() {}\n")
	newTime := time.Now().Add(time.Hour)
	if err := os.Chtimes(target, newTime, newTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	after, err := Fingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if before == after {
		t.Fatalf("Fingerprint() unchanged after editing content with a different modification time")
	}
}

func TestFingerprintChangesOnAddFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFingerprintFile(t, filepath.Join(root, "go.mod"), "module example.com/fp\n")
	writeFingerprintFile(t, filepath.Join(root, "main.go"), "package fp\nfunc Run() {}\n")

	before, err := Fingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}

	writeFingerprintFile(t, filepath.Join(root, "extra.go"), "package fp\nfunc Extra() {}\n")

	after, err := Fingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if before == after {
		t.Fatalf("Fingerprint() unchanged after adding a source file")
	}
}

func TestFingerprintChangesOnDeleteFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFingerprintFile(t, filepath.Join(root, "go.mod"), "module example.com/fp\n")
	writeFingerprintFile(t, filepath.Join(root, "main.go"), "package fp\nfunc Run() {}\n")
	extra := filepath.Join(root, "extra.go")
	writeFingerprintFile(t, extra, "package fp\nfunc Extra() {}\n")

	before, err := Fingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}

	if err := os.Remove(extra); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	after, err := Fingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if before == after {
		t.Fatalf("Fingerprint() unchanged after deleting a source file")
	}
}

func TestFingerprintChangesOnCreatingRootGoMod(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFingerprintFile(t, filepath.Join(root, "main.go"), "package fp\nfunc Run() {}\n")

	before, err := Fingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}

	writeFingerprintFile(t, filepath.Join(root, "go.mod"), "module example.com/fp\n")

	after, err := Fingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if before == after {
		t.Fatalf("Fingerprint() unchanged after creating root go.mod")
	}
}

func TestFingerprintChangesOnEditingNestedGoMod(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFingerprintFile(t, filepath.Join(root, "go.mod"), "module example.com/fp\n")
	writeFingerprintFile(t, filepath.Join(root, "main.go"), "package fp\nfunc Run() {}\n")
	nestedGoMod := filepath.Join(root, "nested", "go.mod")
	writeFingerprintFile(t, nestedGoMod, "module example.com/fp/nested\n\ngo 1.24\n")

	before, err := Fingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}

	writeFingerprintFile(t, nestedGoMod, "module example.com/fp/nested\n\ngo 1.23\n")
	newTime := time.Now().Add(time.Hour)
	if err := os.Chtimes(nestedGoMod, newTime, newTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	after, err := Fingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if before == after {
		t.Fatalf("Fingerprint() unchanged after editing a nested go.mod")
	}
}

func TestFingerprintUnaffectedBySkippedDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFingerprintFile(t, filepath.Join(root, "go.mod"), "module example.com/fp\n")
	writeFingerprintFile(t, filepath.Join(root, "main.go"), "package fp\nfunc Run() {}\n")

	before, err := Fingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}

	writeFingerprintFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "module.exports = {};\n")
	writeFingerprintFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFingerprintFile(t, filepath.Join(root, ".hidden", "file.go"), "package hidden\n")
	writeFingerprintFile(t, filepath.Join(root, "node_modules", "pkg", "go.mod"), "module ignored\n")

	after, err := Fingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if before != after {
		t.Fatalf("Fingerprint() changed after adding files inside skipped directories")
	}

	sources, err := sourceFiles(context.Background(), root)
	if err != nil {
		t.Fatalf("sourceFiles() error = %v", err)
	}
	sep := string(os.PathSeparator)
	for _, source := range sources {
		if strings.Contains(source.Path, "node_modules") || strings.Contains(source.Path, sep+".git"+sep) || strings.Contains(source.Path, sep+".hidden"+sep) {
			t.Fatalf("sourceFiles() returned a file inside a skipped directory: %s", source.Path)
		}
	}
}

func TestFingerprintContextCancelled(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFingerprintFile(t, filepath.Join(root, "go.mod"), "module example.com/fp\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Fingerprint(ctx, root); err == nil {
		t.Fatal("Fingerprint() error = nil, want error for a cancelled context")
	}
}

func writeFingerprintFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
