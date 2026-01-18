# CompFS: Composite Filesystem for Go

**CompFS** provides a composite filesystem implementation for Go's `io/fs` interface. It allows you to combine multiple filesystems into a single one, with cascading priority.

## Features

- Combine multiple filesystems into a single `fs.FS` implementation
- Prioritize files from different sources (first match wins)
- Merge directory listings from all sources
- Overlay mode that merges directories when opening them
- Thread-safe for concurrent read operations
- Compatible with standard library's `io/fs` interfaces
- Helper functions for common operations (`ReadDir`, `Sub`)
- Works with both embedded and OS filesystems

## Use Cases

- Template engines with theme/override support
- `http.FileSystem` from multiple embedded resources
- Development overrides for embedded resources
- Application resources from multiple sources
- Layered configurations
- Plugin systems

## Installation

```bash
go get github.com/goliatone/go-composite-fs
```

## Basic Usage

```go
package main

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/goliatone/go-composite-fs"
)

//go:embed base/*
var baseFS embed.FS

//go:embed theme/*
var themeFS embed.FS

func main() {
	// Create a composite filesystem with multiple sources
	// Files will be searched in order (first match wins)
	composite := cfs.NewCompositeFS(
		os.DirFS("./dev"),  // First check local dev directory
		themeFS,            // Then check theme files
		baseFS,             // Finally check base files
	)

	// Open a file (will check each filesystem in order)
	file, err := composite.Open("config.json")
	if err != nil {
		fmt.Println("File not found:", err)
		return
	}
	defer file.Close()

	// Read file content
	content, err := io.ReadAll(file)
	if err != nil {
		fmt.Println("Error reading file:", err)
		return
	}

	fmt.Println(string(content))

	// List directory contents (merged from all filesystems)
	entries, err := cfs.ReadDir(composite, "templates")
	if err != nil {
		fmt.Println("Error reading directory:", err)
		return
	}

	fmt.Println("Templates:")
	for _, entry := range entries {
		fmt.Printf("- %s\n", entry.Name())
	}
}
```

## Template Engine Integration

**CompFS** is perfect for template engines that need to support themes and overrides:

```go
package main

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/template/django"
	"github.com/goliatone/go-composite-fs"
)

//go:embed templates/*
var baseTemplates embed.FS

//go:embed theme/templates/*
var themeTemplates embed.FS

func main() {
	// Create a sub-filesystem for each source to normalize paths
	baseFS, _ := fs.Sub(baseTemplates, "templates")
	themeFS, _ := fs.Sub(themeTemplates, "theme/templates")

	// Create composite filesystem with theme overriding base templates
	templateFS := cfs.NewCompositeFS(
		os.DirFS("./dev/templates"), // Development overrides (highest priority)
		themeFS,                     // Theme templates (higher priority)
		baseFS,                      // Base templates (lower priority)
	)

	// Initialize the template engine with the composite filesystem
	viewEngine := django.NewPathForwardingFileSystem(
		http.FS(templateFS),
		"",   // Root directory (already handled by fs.Sub)
		".html",
	)

	// Configure Fiber
	app := fiber.New(fiber.Config{
		Views: viewEngine,
	})

	// Define routes
	app.Get("/", func(c *fiber.Ctx) error {
		return c.Render("index", fiber.Map{
			"Title": "Welcome",
		})
	})

	app.Listen(":3000")
}
```

## API Reference

### Types

#### CompositeFS

```go
type CompositeFS struct {
	// unexported fields
}
```

`CompositeFS` implements fs.FS by checking multiple underlying filesystems in order. When a file is requested, it tries each filesystem in the order they were provided until the file is found or all filesystems have been checked.

### Functions

#### `NewCompositeFS`

```go
func NewCompositeFS(filesystems ...fs.FS) *CompositeFS
```

`NewCompositeFS` creates a new `CompositeFS` with the given filesystems. Filesystems will be checked in the order they are provided. Non-`fs.ErrNotExist` errors shortcircuit.

#### `NewCompositeFSBestEffort`

```go
func NewCompositeFSBestEffort(filesystems ...fs.FS) *CompositeFS
```

`NewCompositeFSBestEffort` creates a `CompositeFS` that keeps searching other filesystems even when a filesystem returns non-`fs.ErrNotExist` errors.

#### `NewOverlayFS`

```go
func NewOverlayFS(filesystems ...fs.FS) *CompositeFS
```

`NewOverlayFS` creates a `CompositeFS` that merges directory entries across all filesystems when opening a directory, while keeping file lookups first-wins.

#### ReadDir

```go
func ReadDir(fsys fs.FS, name string) ([]fs.DirEntry, error)
```

`ReadDir` is a helper function that reads a directory's contents from an `fs.FS`. It supports both `fs.ReadDirFS` implementations and regular `fs.FS`.

#### Sub

```go
func Sub(fsys fs.FS, dir string) (fs.FS, error)
```

`Sub` is a helper function to get a sub-filesystem from an `fs.FS`.

### Methods

#### Open

```go
func (cfs *CompositeFS) Open(name string) (fs.File, error)
```

`Open` implements `fs.FS.Open` by trying each underlying filesystem in order.

#### ReadDir

```go
func (cfs *CompositeFS) ReadDir(name string) ([]fs.DirEntry, error)
```

`ReadDir` returns the contents of the named directory, merging entries from all filesystems.

#### Stat

```go
func (cfs *CompositeFS) Stat(name string) (fs.FileInfo, error)
```

`Stat` returns file info for the named file from the first filesystem that successfully opens it.

#### Sub

```go
func (cfs *CompositeFS) Sub(dir string) (fs.FS, error)
```

`Sub` returns a new `CompositeFS` rooted at dir in each of the underlying filesystems.

#### ReadFile

```go
func (cfs *CompositeFS) ReadFile(name string) ([]byte, error)
```

`ReadFile` reads the named file from the first filesystem that successfully opens it.

## Thread Safety

**CompFS** is thread-safe for concurrent read operations. The implementation contains no mutable state that would be affected by concurrent access.

## Error Handling

When a file cannot be found in any of the sources, **CompFS** returns a detailed error message that includes errors from each filesystem. Errors are classified as `fs.ErrNotExist` only when every layer reports not-exist. By default, non-`fs.ErrNotExist` errors shortcircuit; use `NewCompositeFSBestEffort` to continue searching in lower-priority layers.

## Performance Considerations

- **CompFS** shortcircuits on the first successful file open, minimizing filesystem checks
- Directory operations merge results from all filesystems
- For best performance, put frequently accessed files in the first filesystem

## License

[MIT License](LICENSE)

<!--
## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request -->
