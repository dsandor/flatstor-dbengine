package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DiskStorage implements Storage using the local filesystem.
type DiskStorage struct {
	basePath string
}

// NewDiskStorage creates a new DiskStorage with the given base path.
func NewDiskStorage(basePath string) (*DiskStorage, error) {
	absPath, err := filepath.Abs(basePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base path: %w", err)
	}

	if err := os.MkdirAll(absPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}

	return &DiskStorage{basePath: absPath}, nil
}

// BasePath returns the base path of the storage.
func (d *DiskStorage) BasePath() string {
	return d.basePath
}

// fullPath returns the full path for a given relative path.
func (d *DiskStorage) fullPath(path string) string {
	return filepath.Join(d.basePath, path)
}

// ReadJSON reads a JSON file and unmarshals it into v.
func (d *DiskStorage) ReadJSON(ctx context.Context, path string, v any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	fullPath := d.fullPath(path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", path, err)
	}

	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("failed to unmarshal JSON from %s: %w", path, err)
	}

	return nil
}

// WriteJSON marshals v to JSON and writes it to path.
func (d *DiskStorage) WriteJSON(ctx context.Context, path string, v any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	fullPath := d.fullPath(path)

	// Ensure parent directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write file %s: %w", path, err)
	}

	return nil
}

// Delete removes a file at path.
func (d *DiskStorage) Delete(ctx context.Context, path string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	fullPath := d.fullPath(path)
	if err := os.Remove(fullPath); err != nil {
		if os.IsNotExist(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete %s: %w", path, err)
	}

	return nil
}

// DeleteDir removes a directory and all its contents.
func (d *DiskStorage) DeleteDir(ctx context.Context, path string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	fullPath := d.fullPath(path)
	if err := os.RemoveAll(fullPath); err != nil {
		return fmt.Errorf("failed to delete directory %s: %w", path, err)
	}

	return nil
}

// ListDirs returns all directory names under the given path.
func (d *DiskStorage) ListDirs(ctx context.Context, path string) ([]string, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	fullPath := d.fullPath(path)
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to list directories in %s: %w", path, err)
	}

	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() && !isHiddenOrMeta(entry.Name()) {
			dirs = append(dirs, entry.Name())
		}
	}

	return dirs, nil
}

// ListFiles returns all file names under the given path.
func (d *DiskStorage) ListFiles(ctx context.Context, path string) ([]string, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	fullPath := d.fullPath(path)
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to list files in %s: %w", path, err)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, entry.Name())
		}
	}

	return files, nil
}

// Exists checks if a file or directory exists at path.
func (d *DiskStorage) Exists(ctx context.Context, path string) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	fullPath := d.fullPath(path)
	_, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check existence of %s: %w", path, err)
	}

	return true, nil
}

// MkdirAll creates a directory along with any necessary parents.
func (d *DiskStorage) MkdirAll(ctx context.Context, path string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	fullPath := d.fullPath(path)
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", path, err)
	}

	return nil
}

// isHiddenOrMeta returns true for hidden files or metadata files.
func isHiddenOrMeta(name string) bool {
	if len(name) == 0 {
		return false
	}
	return name[0] == '.' || name[0] == '_'
}

// Ensure DiskStorage implements Storage interface.
var _ Storage = (*DiskStorage)(nil)
