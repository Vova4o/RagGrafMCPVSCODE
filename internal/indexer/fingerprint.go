package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
)

// fingerprintManifestNames lists non-source files, other than the fixed
// root-level inputs handled separately, that the indexer's module and
// dependency detection reads from anywhere in the repository tree.
var fingerprintManifestNames = map[string]struct{}{
	"go.mod": {}, "go.sum": {}, "go.work": {}, "go.work.sum": {},
}

// fingerprintRootOptional lists root-level inputs that change indexing
// behaviour purely by existing, so their absence must also affect the hash.
var fingerprintRootOptional = []string{"go.mod", "go.work", "go.work.sum"}

// Fingerprint hashes every source file and manifest input that Index reads
// for root, so callers can detect on-disk changes without a full re-index.
// The result changes whenever SchemaVersion or IndexerVersion changes, so
// graphs built by an older algorithm are treated as stale even when no
// source file changed.
func Fingerprint(ctx context.Context, root string) (string, error) {
	root, err := canonicalRoot(root)
	if err != nil {
		return "", err
	}

	sources, err := sourceFiles(ctx, root)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(sources)+8)
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("compute fingerprint for %s: %w", root, err)
		}
		line, err := EntryLine(root, source.Path)
		if err != nil {
			return "", err
		}
		lines = append(lines, line)
	}

	manifestLines, err := fingerprintManifests(ctx, root)
	if err != nil {
		return "", err
	}
	lines = append(lines, manifestLines...)

	present := make(map[string]struct{}, len(manifestLines))
	for _, line := range manifestLines {
		present[strings.SplitN(line, "\t", 2)[0]] = struct{}{}
	}
	for _, name := range fingerprintRootOptional {
		if _, ok := present[name]; ok {
			continue
		}
		line, err := EntryLine(root, filepath.Join(root, name))
		if err != nil {
			return "", err
		}
		lines = append(lines, line)
	}

	vendorLine, err := EntryLine(root, filepath.Join(root, "vendor", "modules.txt"))
	if err != nil {
		return "", err
	}
	lines = append(lines, vendorLine)

	sort.Strings(lines)
	hasher := sha256.New()
	fmt.Fprintf(hasher, "schema=%d indexer=%d\n", graph.SchemaVersion, graph.IndexerVersion)
	for _, line := range lines {
		hasher.Write([]byte(line))
		hasher.Write([]byte("\n"))
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// fingerprintManifests walks root for manifest files anywhere in the tree,
// applying the exact directory-skipping rules sourceFiles uses so the same
// files are hidden from both discovery passes.
func fingerprintManifests(ctx context.Context, root string) ([]string, error) {
	var lines []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %s: %w", path, walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			if _, skip := skippedDirectories[entry.Name()]; skip || strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if _, isManifest := fingerprintManifestNames[entry.Name()]; !isManifest {
			return nil
		}
		line, lineErr := EntryLine(root, path)
		if lineErr != nil {
			return lineErr
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover fingerprint manifests in %s: %w", root, err)
	}
	return lines, nil
}

// EntryLine returns a deterministic "relpath\tsize\tmtime_ns\tctime_ns" line
// describing path relative to root, or "relpath\tabsent" when path does not
// exist. It never opens file contents, only stats metadata, so callers can
// combine these lines into their own fingerprint hash.
func EntryLine(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("resolve relative fingerprint path %s: %w", path, err)
	}
	rel = filepath.ToSlash(rel)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return rel + "\tabsent", nil
		}
		return "", fmt.Errorf("stat fingerprint input %s: %w", path, err)
	}
	return fmt.Sprintf("%s\t%d\t%d\t%d", rel, info.Size(), info.ModTime().UnixNano(), fingerprintCtimeNanos(info)), nil
}
