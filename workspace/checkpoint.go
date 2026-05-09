package workspace

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const CheckpointDir = ".kkode/checkpoints"
const MaxCheckpointSnapshotBytes = 16 << 20

type FileCheckpoint struct {
	Version   int                   `json:"version"`
	ID        string                `json:"id"`
	CreatedAt time.Time             `json:"created_at"`
	Entries   []FileCheckpointEntry `json:"entries"`
}

type FileCheckpointEntry struct {
	Path          string `json:"path"`
	Exists        bool   `json:"exists"`
	Kind          string `json:"kind,omitempty"`
	Mode          uint32 `json:"mode,omitempty"`
	Size          int64  `json:"size,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
}

type FileCheckpointSummary struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Entries   int       `json:"entries"`
	Paths     []string  `json:"paths"`
}

func (w *Workspace) CreateCheckpoint(paths []string) (FileCheckpoint, error) {
	cp, err := w.SnapshotPaths(paths)
	if err != nil {
		return FileCheckpoint{}, err
	}
	if err := w.SaveCheckpoint(cp); err != nil {
		return FileCheckpoint{}, err
	}
	return cp, nil
}

func (w *Workspace) SnapshotPaths(paths []string) (FileCheckpoint, error) {
	paths = uniqueNonEmptyPaths(paths)
	if len(paths) == 0 {
		return FileCheckpoint{}, errors.New("checkpoint paths are required")
	}
	cp := FileCheckpoint{Version: 1, ID: newCheckpointID(), CreatedAt: time.Now().UTC()}
	seen := map[string]bool{}
	totalBytes := 0
	for _, rel := range paths {
		if err := w.snapshotPath(rel, &cp, seen, &totalBytes); err != nil {
			return FileCheckpoint{}, err
		}
	}
	sort.SliceStable(cp.Entries, func(i, j int) bool {
		return cp.Entries[i].Path < cp.Entries[j].Path
	})
	return cp, nil
}

func (w *Workspace) SaveCheckpoint(cp FileCheckpoint) error {
	if cp.ID == "" {
		return errors.New("checkpoint id is required")
	}
	if !safeCheckpointID(cp.ID) {
		return fmt.Errorf("invalid checkpoint id: %s", cp.ID)
	}
	dir, err := w.Resolve(CheckpointDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > MaxCheckpointSnapshotBytes {
		return fmt.Errorf("checkpoint payload must be <= %d bytes", MaxCheckpointSnapshotBytes)
	}
	return os.WriteFile(filepath.Join(dir, cp.ID+".json"), data, 0o644)
}

func (w *Workspace) LoadCheckpoint(id string) (FileCheckpoint, error) {
	id = strings.TrimSpace(id)
	if !safeCheckpointID(id) {
		return FileCheckpoint{}, fmt.Errorf("invalid checkpoint id: %s", id)
	}
	path, err := w.Resolve(filepath.ToSlash(filepath.Join(CheckpointDir, id+".json")))
	if err != nil {
		return FileCheckpoint{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return FileCheckpoint{}, err
	}
	var cp FileCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return FileCheckpoint{}, err
	}
	if cp.ID != id {
		return FileCheckpoint{}, fmt.Errorf("checkpoint id mismatch: %s", cp.ID)
	}
	if cp.Version != 1 {
		return FileCheckpoint{}, fmt.Errorf("unsupported checkpoint version: %d", cp.Version)
	}
	return cp, nil
}

func (w *Workspace) ListCheckpoints() ([]FileCheckpointSummary, error) {
	dir, err := w.Resolve(CheckpointDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]FileCheckpointSummary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		cp, err := w.LoadCheckpoint(id)
		if err != nil {
			continue
		}
		out = append(out, checkpointSummary(cp))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (w *Workspace) DeleteCheckpoint(id string) error {
	id = strings.TrimSpace(id)
	if !safeCheckpointID(id) {
		return fmt.Errorf("invalid checkpoint id: %s", id)
	}
	path, err := w.Resolve(filepath.ToSlash(filepath.Join(CheckpointDir, id+".json")))
	if err != nil {
		return err
	}
	return os.Remove(path)
}

func (w *Workspace) RestoreCheckpoint(id string) (FileCheckpoint, error) {
	cp, err := w.LoadCheckpoint(id)
	if err != nil {
		return FileCheckpoint{}, err
	}
	for _, entry := range cp.Entries {
		if err := w.restoreCheckpointEntry(entry); err != nil {
			return FileCheckpoint{}, err
		}
	}
	return cp, nil
}

func checkpointSummary(cp FileCheckpoint) FileCheckpointSummary {
	paths := make([]string, 0, len(cp.Entries))
	for _, entry := range cp.Entries {
		paths = append(paths, entry.Path)
	}
	return FileCheckpointSummary{ID: cp.ID, CreatedAt: cp.CreatedAt, Entries: len(cp.Entries), Paths: paths}
}

func (w *Workspace) snapshotPath(rel string, cp *FileCheckpoint, seen map[string]bool, totalBytes *int) error {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return errors.New("checkpoint path is required")
	}
	abs, err := w.Resolve(rel)
	if err != nil {
		return err
	}
	root := filepath.Clean(w.Root)
	if filepath.Clean(abs) == root {
		return errors.New("workspace root cannot be checkpointed")
	}
	info, err := os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		normalized, relErr := w.relativePath(abs)
		if relErr != nil {
			return relErr
		}
		addCheckpointEntry(cp, seen, FileCheckpointEntry{Path: normalized, Exists: false})
		return nil
	}
	if err != nil {
		return err
	}
	return w.snapshotExistingPath(abs, info, cp, seen, totalBytes)
}

func (w *Workspace) snapshotExistingPath(abs string, info os.FileInfo, cp *FileCheckpoint, seen map[string]bool, totalBytes *int) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("checkpoint does not support symlink: %s", abs)
	}
	if info.IsDir() {
		return filepath.WalkDir(abs, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel := mustRelSlash(w.Root, path)
			if path != abs && shouldSkipWalkDir(rel, entry.Name()) {
				return filepath.SkipDir
			}
			childInfo, err := entry.Info()
			if err != nil {
				return err
			}
			if childInfo.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("checkpoint does not support symlink: %s", rel)
			}
			if childInfo.IsDir() {
				addCheckpointEntry(cp, seen, FileCheckpointEntry{Path: rel, Exists: true, Kind: "directory", Mode: uint32(childInfo.Mode().Perm())})
				return nil
			}
			return w.snapshotExistingPath(path, childInfo, cp, seen, totalBytes)
		})
	}
	rel, err := w.relativePath(abs)
	if err != nil {
		return err
	}
	if strings.HasPrefix(rel, CheckpointDir+"/") {
		return nil
	}
	if info.Size() > int64(MaxFileReadBytes) {
		return fmt.Errorf("checkpoint file must be <= %d bytes: %s", MaxFileReadBytes, rel)
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	*totalBytes += len(content)
	if *totalBytes > MaxCheckpointSnapshotBytes {
		return fmt.Errorf("checkpoint snapshot must be <= %d bytes", MaxCheckpointSnapshotBytes)
	}
	addCheckpointEntry(cp, seen, FileCheckpointEntry{Path: rel, Exists: true, Kind: "file", Mode: uint32(info.Mode().Perm()), Size: info.Size(), ContentBase64: base64.StdEncoding.EncodeToString(content)})
	return nil
}

func (w *Workspace) restoreCheckpointEntry(entry FileCheckpointEntry) error {
	rel := strings.TrimSpace(entry.Path)
	if rel == "" {
		return errors.New("checkpoint entry path is required")
	}
	abs, err := w.Resolve(rel)
	if err != nil {
		return err
	}
	root := filepath.Clean(w.Root)
	if filepath.Clean(abs) == root {
		return errors.New("workspace root cannot be restored")
	}
	if !entry.Exists {
		return removeIfExists(abs)
	}
	switch entry.Kind {
	case "directory":
		mode := os.FileMode(entry.Mode)
		if mode == 0 {
			mode = 0o755
		}
		return os.MkdirAll(abs, mode.Perm())
	case "file":
		content, err := base64.StdEncoding.DecodeString(entry.ContentBase64)
		if err != nil {
			return err
		}
		if len(content) > MaxFileWriteBytes {
			return fmt.Errorf("restored content must be <= %d bytes: %s", MaxFileWriteBytes, rel)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(entry.Mode)
		if mode == 0 {
			mode = 0o644
		}
		return os.WriteFile(abs, content, mode.Perm())
	default:
		return fmt.Errorf("unsupported checkpoint entry kind: %s", entry.Kind)
	}
}

func removeIfExists(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func addCheckpointEntry(cp *FileCheckpoint, seen map[string]bool, entry FileCheckpointEntry) {
	if seen[entry.Path] {
		return
	}
	seen[entry.Path] = true
	cp.Entries = append(cp.Entries, entry)
}

func uniqueNonEmptyPaths(paths []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(filepath.ToSlash(path))
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

func (w *Workspace) relativePath(abs string) (string, error) {
	rel, err := filepath.Rel(w.Root, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func mustRelSlash(root string, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func newCheckpointID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ws_%d", time.Now().UnixNano())
	}
	return "ws_" + time.Now().UTC().Format("20060102T150405Z") + "_" + hex.EncodeToString(b[:])
}

func safeCheckpointID(id string) bool {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return false
	}
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}
