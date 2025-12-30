package storage

import "context"

// Storage defines the interface for data persistence.
// Implementations can support disk, S3, or other backends.
type Storage interface {
	// ReadJSON reads a JSON file and unmarshals it into v.
	ReadJSON(ctx context.Context, path string, v any) error

	// WriteJSON marshals v to JSON and writes it to path.
	WriteJSON(ctx context.Context, path string, v any) error

	// Delete removes a file or directory at path.
	Delete(ctx context.Context, path string) error

	// DeleteDir removes a directory and all its contents.
	DeleteDir(ctx context.Context, path string) error

	// ListDirs returns all directory names under the given path.
	ListDirs(ctx context.Context, path string) ([]string, error)

	// ListFiles returns all file names under the given path.
	ListFiles(ctx context.Context, path string) ([]string, error)

	// Exists checks if a file or directory exists at path.
	Exists(ctx context.Context, path string) (bool, error)

	// MkdirAll creates a directory along with any necessary parents.
	MkdirAll(ctx context.Context, path string) error
}

// RowData represents a single row's data across all column groups.
type RowData map[string]map[string]any

// ColumnGroupData represents data for a single column group.
type ColumnGroupData map[string]any
