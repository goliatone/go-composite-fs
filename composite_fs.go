package cfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
)

// CompositeFS implements fs.FS by checking multiple underlying filesystems in order.
// When a file is requested, it tries each filesystem in the order they were provided
// until the file is found or all filesystems have been checked.
type CompositeFS struct {
	filesystems []fs.FS
}

// NewCompositeFS creates a new CompositeFS with the given filesystems.
// Filesystems will be checked in the order they are provided.
func NewCompositeFS(filesystems ...fs.FS) *CompositeFS {
	fsList := make([]fs.FS, len(filesystems))
	copy(fsList, filesystems)
	return &CompositeFS{
		filesystems: fsList,
	}
}

// Open implements fs.FS.Open by trying each underlying filesystem in order.
func (cfs *CompositeFS) Open(name string) (fs.File, error) {
	name = path.Clean(name)

	var errs []error
	allNotExist := true

	for i, fsys := range cfs.filesystems {
		file, err := fsys.Open(name)
		if err == nil {
			return file, nil
		}

		if !errors.Is(err, fs.ErrNotExist) {
			allNotExist = false
		}
		errs = append(errs, fmt.Errorf("filesystem %d: %w", i, err))
	}

	joined := errors.Join(errs...)
	if allNotExist {
		return nil, fmt.Errorf("%w: file %q not found in any filesystem: %v", fs.ErrNotExist, name, joined)
	}

	return nil, fmt.Errorf("file %q not found in any filesystem: %v", name, joined)
}

// ReadDir returns the contents of the named directory from the
// first filesystem that successfully opens it.
// This implements a custom directory listing capability.
func (cfs *CompositeFS) ReadDir(name string) ([]fs.DirEntry, error) {
	name = path.Clean(name)

	// we merge directory entries from all filesystems
	var allEntries = make(map[string]fs.DirEntry)
	var foundAny bool

	for _, fsys := range cfs.filesystems {
		if rdfs, ok := fsys.(fs.ReadDirFS); ok {
			entries, err := rdfs.ReadDir(name)
			if err == nil {
				foundAny = true
				// later filesystems dont override earlier ones
				for _, entry := range entries {
					if _, exists := allEntries[entry.Name()]; !exists {
						allEntries[entry.Name()] = entry
					}
				}
			}
		} else {
			// fallback to manual directory opening
			dir, err := fsys.Open(name)
			if err == nil {
				foundAny = true
				if dirFile, ok := dir.(fs.ReadDirFile); ok {
					entries, err := dirFile.ReadDir(-1)
					dir.Close()
					if err == nil {
						for _, entry := range entries {
							if _, exists := allEntries[entry.Name()]; !exists {
								allEntries[entry.Name()] = entry
							}
						}
					}
				} else {
					dir.Close()
				}
			}
		}
	}

	if !foundAny {
		return nil, fmt.Errorf("directory %q not found in any filesystem", name)
	}

	result := make([]fs.DirEntry, 0, len(allEntries))
	for _, entry := range allEntries {
		result = append(result, entry)
	}

	return result, nil
}

// Stat returns file info for the named file from the first
// filesystem that successfully opens it
func (cfs *CompositeFS) Stat(name string) (fs.FileInfo, error) {
	name = path.Clean(name)

	for _, fsys := range cfs.filesystems {
		// fs implements StatFS
		if statFS, ok := fsys.(fs.StatFS); ok {
			info, err := statFS.Stat(name)
			if err == nil {
				return info, nil
			}
		} else {
			// fallback to Open + Stat
			file, err := fsys.Open(name)
			if err == nil {
				info, err := file.Stat()
				file.Close()
				if err == nil {
					return info, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("file %q not found in any filesystem", name)
}

// Sub returns a new CompositeFS rooted at dir in each of the
// underlying filesystems
func (cfs *CompositeFS) Sub(dir string) (fs.FS, error) {
	dir = path.Clean(dir)

	subFSList := make([]fs.FS, 0, len(cfs.filesystems))

	for _, fsys := range cfs.filesystems {
		// fs implements SubFS
		if subber, ok := fsys.(interface {
			Sub(dir string) (fs.FS, error)
		}); ok {
			subFS, err := subber.Sub(dir)
			if err == nil {
				subFSList = append(subFSList, subFS)
			}
		}
	}

	if len(subFSList) == 0 {
		return nil, fmt.Errorf("directory %q not found in any filesystem", dir)
	}

	return NewCompositeFS(subFSList...), nil
}

// ReadFile reads the named file from the first filesystem that
// successfully opens it
func (cfs *CompositeFS) ReadFile(name string) ([]byte, error) {
	name = path.Clean(name)

	for _, fsys := range cfs.filesystems {
		// fs implements ReadFileFS
		if rfFS, ok := fsys.(interface {
			ReadFile(name string) ([]byte, error)
		}); ok {
			data, err := rfFS.ReadFile(name)
			if err == nil {
				return data, nil
			}
		} else {
			// fallback to manual file reading
			file, err := fsys.Open(name)
			if err == nil {
				data, err := io.ReadAll(file)
				file.Close()
				if err == nil {
					return data, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("file %q not found in any filesystem", name)
}

// ReadDir is a helper function to read a directory's contents from an fs.FS
// It supports both fs.ReadDirFS implementations and regular fs.FS
func ReadDir(fsys fs.FS, name string) ([]fs.DirEntry, error) {
	if rdfs, ok := fsys.(fs.ReadDirFS); ok {
		return rdfs.ReadDir(name)
	}

	dir, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer dir.Close()

	if dirFile, ok := dir.(fs.ReadDirFile); ok {
		return dirFile.ReadDir(-1)
	}

	return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
}

// Sub is a helper function to get a sub-filesystem
func Sub(fsys fs.FS, dir string) (fs.FS, error) {
	// If the filesystem implements SubFS, use that
	if subber, ok := fsys.(interface {
		Sub(dir string) (fs.FS, error)
	}); ok {
		return subber.Sub(dir)
	}

	return nil, &fs.PathError{Op: "sub", Path: dir, Err: fs.ErrInvalid}
}
