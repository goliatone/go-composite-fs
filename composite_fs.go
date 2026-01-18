package cfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"time"
)

// CompositeFS implements fs.FS by checking multiple underlying filesystems in order.
// When a file is requested, it tries each filesystem in the order they were provided
// until the file is found or all filesystems have been checked.
type CompositeFS struct {
	filesystems []fs.FS
	bestEffort  bool
	mergeDirs   bool
}

// NewCompositeFS creates a new CompositeFS with the given filesystems.
// Filesystems will be checked in the order they are provided.
func NewCompositeFS(filesystems ...fs.FS) *CompositeFS {
	return newCompositeFS(false, false, filesystems...)
}

// NewCompositeFSBestEffort creates a CompositeFS that keeps searching
// other filesystems even when a filesystem returns non-ErrNotExist errors.
func NewCompositeFSBestEffort(filesystems ...fs.FS) *CompositeFS {
	return newCompositeFS(true, false, filesystems...)
}

// NewOverlayFS creates a CompositeFS that merges directory entries
// across all filesystems when opening a directory.
func NewOverlayFS(filesystems ...fs.FS) *CompositeFS {
	return newCompositeFS(false, true, filesystems...)
}

func newCompositeFS(bestEffort bool, mergeDirs bool, filesystems ...fs.FS) *CompositeFS {
	fsList := make([]fs.FS, len(filesystems))
	copy(fsList, filesystems)
	return &CompositeFS{
		filesystems: fsList,
		bestEffort:  bestEffort,
		mergeDirs:   mergeDirs,
	}
}

// Open implements fs.FS.Open by trying each underlying filesystem in order.
func (cfs *CompositeFS) Open(name string) (fs.File, error) {
	name = path.Clean(name)

	if cfs.mergeDirs {
		return cfs.openOverlay(name)
	}

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

func (cfs *CompositeFS) openOverlay(name string) (fs.File, error) {
	var errs []error
	allNotExist := true
	var foundDir bool
	var dirInfo fs.FileInfo
	var entries []fs.DirEntry
	var seen map[string]struct{}
	var foundAnyDirRead bool

	for i, fsys := range cfs.filesystems {
		file, err := fsys.Open(name)
		if err == nil {
			info, statErr := file.Stat()
			if statErr != nil {
				file.Close()
				if errors.Is(statErr, fs.ErrNotExist) {
					errs = append(errs, fmt.Errorf("filesystem %d: %w", i, statErr))
					continue
				}

				allNotExist = false
				wrapped := fmt.Errorf("filesystem %d: %w", i, statErr)
				if !cfs.bestEffort {
					return nil, wrapped
				}
				errs = append(errs, wrapped)
				continue
			}

			if info.IsDir() {
				foundDir = true
				if dirInfo == nil {
					dirInfo = info
				}
				file.Close()

				dirEntries, err := ReadDir(fsys, name)
				if err == nil {
					foundAnyDirRead = true
					allNotExist = false
					if seen == nil {
						seen = make(map[string]struct{})
					}
					for _, entry := range dirEntries {
						if _, exists := seen[entry.Name()]; exists {
							continue
						}
						seen[entry.Name()] = struct{}{}
						entries = append(entries, entry)
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
				continue
			}

			if foundDir {
				allNotExist = false
				file.Close()
				continue
			}

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

	if foundAnyDirRead {
		return &overlayDirFile{
			name:    name,
			info:    dirInfo,
			entries: entries,
		}, nil
	}

	if foundDir {
		return nil, notFoundError("directory", name, errs, allNotExist)
	}

	return nil, notFoundError("file", name, errs, allNotExist)
}

// ReadDir returns the merged contents of the named directory across all filesystems.
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

	return newCompositeFS(cfs.bestEffort, cfs.mergeDirs, subFSList...), nil
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

type overlayDirFile struct {
	name    string
	info    fs.FileInfo
	entries []fs.DirEntry
	pos     int
}

func (f *overlayDirFile) Stat() (fs.FileInfo, error) {
	if f.info != nil {
		return f.info, nil
	}
	return dirInfo{name: path.Base(f.name)}, nil
}

func (f *overlayDirFile) Read(b []byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: f.name, Err: fs.ErrInvalid}
}

func (f *overlayDirFile) Close() error {
	return nil
}

func (f *overlayDirFile) ReadDir(n int) ([]fs.DirEntry, error) {
	if n <= 0 {
		if f.pos >= len(f.entries) {
			return nil, nil
		}
		entries := f.entries[f.pos:]
		f.pos = len(f.entries)
		return entries, nil
	}

	if f.pos >= len(f.entries) {
		return nil, io.EOF
	}

	end := f.pos + n
	if end > len(f.entries) {
		end = len(f.entries)
	}
	entries := f.entries[f.pos:end]
	f.pos = end
	if f.pos >= len(f.entries) {
		return entries, io.EOF
	}
	return entries, nil
}

type dirInfo struct {
	name string
}

func (d dirInfo) Name() string       { return d.name }
func (d dirInfo) Size() int64        { return 0 }
func (d dirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o555 }
func (d dirInfo) ModTime() time.Time { return time.Time{} }
func (d dirInfo) IsDir() bool        { return true }
func (d dirInfo) Sys() interface{}   { return nil }
