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
	bestEffort  bool
}

// NewCompositeFS creates a new CompositeFS with the given filesystems.
// Filesystems will be checked in the order they are provided.
func NewCompositeFS(filesystems ...fs.FS) *CompositeFS {
	return newCompositeFS(false, filesystems...)
}

// NewCompositeFSBestEffort creates a CompositeFS that keeps searching
// other filesystems even when a filesystem returns non-ErrNotExist errors.
func NewCompositeFSBestEffort(filesystems ...fs.FS) *CompositeFS {
	return newCompositeFS(true, filesystems...)
}

func newCompositeFS(bestEffort bool, filesystems ...fs.FS) *CompositeFS {
	fsList := make([]fs.FS, len(filesystems))
	copy(fsList, filesystems)
	return &CompositeFS{
		filesystems: fsList,
		bestEffort:  bestEffort,
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

		if errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("filesystem %d: %w", i, err))
			continue
		}

		allNotExist = false
		wrapped := fmt.Errorf("filesystem %d: %w", i, err)
		if !cfs.bestEffort {
			return nil, wrapped
		}
		errs = append(errs, wrapped)
	}

	return nil, notFoundError("file", name, errs, allNotExist)
}

// ReadDir returns the contents of the named directory from the
// first filesystem that successfully opens it.
// This implements a custom directory listing capability.
func (cfs *CompositeFS) ReadDir(name string) ([]fs.DirEntry, error) {
	name = path.Clean(name)

	// we merge directory entries from all filesystems
	var allEntries = make(map[string]fs.DirEntry)
	var foundAny bool
	var errs []error
	allNotExist := true

	for i, fsys := range cfs.filesystems {
		entries, err := ReadDir(fsys, name)
		if err == nil {
			foundAny = true
			allNotExist = false
			// later filesystems dont override earlier ones
			for _, entry := range entries {
				if _, exists := allEntries[entry.Name()]; !exists {
					allEntries[entry.Name()] = entry
				}
			}
			continue
		}

		if errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("filesystem %d: %w", i, err))
			continue
		}

		allNotExist = false
		wrapped := fmt.Errorf("filesystem %d: %w", i, err)
		if !cfs.bestEffort {
			return nil, wrapped
		}
		errs = append(errs, wrapped)
	}

	if !foundAny {
		return nil, notFoundError("directory", name, errs, allNotExist)
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

	var errs []error
	allNotExist := true

	for i, fsys := range cfs.filesystems {
		// fs implements StatFS
		if statFS, ok := fsys.(fs.StatFS); ok {
			info, err := statFS.Stat(name)
			if err == nil {
				return info, nil
			}

			if errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("filesystem %d: %w", i, err))
				continue
			}

			allNotExist = false
			wrapped := fmt.Errorf("filesystem %d: %w", i, err)
			if !cfs.bestEffort {
				return nil, wrapped
			}
			errs = append(errs, wrapped)
			continue
		} else {
			// fallback to Open + Stat
			file, err := fsys.Open(name)
			if err == nil {
				info, err := file.Stat()
				file.Close()
				if err == nil {
					return info, nil
				}

				if errors.Is(err, fs.ErrNotExist) {
					errs = append(errs, fmt.Errorf("filesystem %d: %w", i, err))
					continue
				}

				allNotExist = false
				wrapped := fmt.Errorf("filesystem %d: %w", i, err)
				if !cfs.bestEffort {
					return nil, wrapped
				}
				errs = append(errs, wrapped)
				continue
			}

			if errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("filesystem %d: %w", i, err))
				continue
			}

			allNotExist = false
			wrapped := fmt.Errorf("filesystem %d: %w", i, err)
			if !cfs.bestEffort {
				return nil, wrapped
			}
			errs = append(errs, wrapped)
		}
	}

	return nil, notFoundError("file", name, errs, allNotExist)
}

// Sub returns a new CompositeFS rooted at dir in each of the
// underlying filesystems
func (cfs *CompositeFS) Sub(dir string) (fs.FS, error) {
	dir = path.Clean(dir)

	subFSList := make([]fs.FS, 0, len(cfs.filesystems))
	var errs []error
	allNotExist := true

	for i, fsys := range cfs.filesystems {
		// fs implements SubFS
		if subber, ok := fsys.(interface {
			Sub(dir string) (fs.FS, error)
		}); ok {
			subFS, err := subber.Sub(dir)
			if err == nil {
				subFSList = append(subFSList, subFS)
				allNotExist = false
				continue
			}

			if errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("filesystem %d: %w", i, err))
				continue
			}

			allNotExist = false
			wrapped := fmt.Errorf("filesystem %d: %w", i, err)
			if !cfs.bestEffort {
				return nil, wrapped
			}
			errs = append(errs, wrapped)
		}
	}

	if len(subFSList) == 0 {
		return nil, notFoundError("directory", dir, errs, allNotExist)
	}

	return newCompositeFS(cfs.bestEffort, subFSList...), nil
}

// ReadFile reads the named file from the first filesystem that
// successfully opens it
func (cfs *CompositeFS) ReadFile(name string) ([]byte, error) {
	name = path.Clean(name)

	var errs []error
	allNotExist := true

	for i, fsys := range cfs.filesystems {
		// fs implements ReadFileFS
		if rfFS, ok := fsys.(interface {
			ReadFile(name string) ([]byte, error)
		}); ok {
			data, err := rfFS.ReadFile(name)
			if err == nil {
				return data, nil
			}

			if errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("filesystem %d: %w", i, err))
				continue
			}

			allNotExist = false
			wrapped := fmt.Errorf("filesystem %d: %w", i, err)
			if !cfs.bestEffort {
				return nil, wrapped
			}
			errs = append(errs, wrapped)
			continue
		}

		// fallback to manual file reading
		file, err := fsys.Open(name)
		if err == nil {
			data, err := io.ReadAll(file)
			file.Close()
			if err == nil {
				return data, nil
			}

			if errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("filesystem %d: %w", i, err))
				continue
			}

			allNotExist = false
			wrapped := fmt.Errorf("filesystem %d: %w", i, err)
			if !cfs.bestEffort {
				return nil, wrapped
			}
			errs = append(errs, wrapped)
			continue
		}

		if errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("filesystem %d: %w", i, err))
			continue
		}

		allNotExist = false
		wrapped := fmt.Errorf("filesystem %d: %w", i, err)
		if !cfs.bestEffort {
			return nil, wrapped
		}
		errs = append(errs, wrapped)
	}

	return nil, notFoundError("file", name, errs, allNotExist)
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

func notFoundError(kind, name string, errs []error, allNotExist bool) error {
	message := fmt.Sprintf("%s %q not found in any filesystem", kind, name)
	if len(errs) > 0 {
		message = fmt.Sprintf("%s: %v", message, errors.Join(errs...))
	}
	if allNotExist {
		return fmt.Errorf("%w: %s", fs.ErrNotExist, message)
	}
	return errors.New(message)
}
