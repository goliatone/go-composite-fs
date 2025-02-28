package cfs_test

import (
	"embed"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	cfs "github.com/goliatone/go-composite-fs"
)

func TestCompositeFS(t *testing.T) {
	// Create first in-memory filesystem with some files
	fs1 := fstest.MapFS{
		"file1.txt": &fstest.MapFile{
			Data: []byte("content from fs1"),
		},
		"common.txt": &fstest.MapFile{
			Data: []byte("common file from fs1"),
		},
		"dir/nested.txt": &fstest.MapFile{
			Data: []byte("nested file from fs1"),
		},
	}

	// Create second in-memory filesystem with other files
	fs2 := fstest.MapFS{
		"file2.txt": &fstest.MapFile{
			Data: []byte("content from fs2"),
		},
		"common.txt": &fstest.MapFile{
			Data: []byte("common file from fs2 (should be shadowed)"),
		},
		"dir/nested2.txt": &fstest.MapFile{
			Data: []byte("nested file 2 from fs2"),
		},
	}

	// Create the composite filesystem
	composite := cfs.NewCompositeFS(fs1, fs2)

	// Test file from first filesystem
	testReadFile(t, composite, "file1.txt", "content from fs1")

	// Test file from second filesystem
	testReadFile(t, composite, "file2.txt", "content from fs2")

	// Test common file (should come from first filesystem)
	testReadFile(t, composite, "common.txt", "common file from fs1")

	// Test nested file from first filesystem
	testReadFile(t, composite, "dir/nested.txt", "nested file from fs1")

	// Test nested file from second filesystem
	testReadFile(t, composite, "dir/nested2.txt", "nested file 2 from fs2")

	// Test non-existent file
	_, err := composite.Open("nonexistent.txt")
	if err == nil {
		t.Error("Expected error for nonexistent file, got nil")
	}

	// Test directory listing
	entries, err := cfs.ReadDir(composite, "dir")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	// Should have both nested files
	fileNames := make(map[string]bool)
	for _, entry := range entries {
		fileNames[entry.Name()] = true
	}

	if !fileNames["nested.txt"] {
		t.Error("Expected dir listing to contain nested.txt")
	}
	if !fileNames["nested2.txt"] {
		t.Error("Expected dir listing to contain nested2.txt")
	}
}

func testReadFile(t *testing.T, fsys fs.FS, name, expectedContent string) {
	t.Helper()

	file, err := fsys.Open(name)
	if err != nil {
		t.Fatalf("Failed to open %s: %v", name, err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("Failed to read %s: %v", name, err)
	}

	if string(content) != expectedContent {
		t.Errorf("Expected content %q, got %q", expectedContent, string(content))
	}
}

func TestWithOSFS(t *testing.T) {

	if testing.Short() {
		t.Skip("Skipping test that uses OS filesystem")
	}

	tempDir, err := os.MkdirTemp("", "compfs-test")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	osFilePath := filepath.Join(tempDir, "os-file.txt")
	err = os.WriteFile(osFilePath, []byte("content from OS"), 0644)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	memFS := fstest.MapFS{
		"mem-file.txt": &fstest.MapFile{
			Data: []byte("content from memory"),
		},
	}

	osFS := os.DirFS(tempDir)
	composite := cfs.NewCompositeFS(memFS, osFS)

	testReadFile(t, composite, "mem-file.txt", "content from memory")

	testReadFile(t, composite, "os-file.txt", "content from OS")
}

func TestSubdirectory(t *testing.T) {
	fs1 := fstest.MapFS{
		"subdir/file1.txt": &fstest.MapFile{
			Data: []byte("file1 in subdir"),
		},
	}

	fs2 := fstest.MapFS{
		"subdir/file2.txt": &fstest.MapFile{
			Data: []byte("file2 in subdir"),
		},
	}

	composite := cfs.NewCompositeFS(fs1, fs2)

	subFS, err := cfs.Sub(composite, "subdir")
	if err != nil {
		t.Fatalf("Sub() failed: %v", err)
	}

	testReadFile(t, subFS, "file1.txt", "file1 in subdir")
	testReadFile(t, subFS, "file2.txt", "file2 in subdir")
}

type TestFs struct {
	files map[string]string
}

func (f *TestFs) Open(name string) (fs.File, error) {
	content, ok := f.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &TestFile{name: name, content: content, offset: 0}, nil
}

type TestFile struct {
	name    string
	content string
	offset  int64
}

func (f *TestFile) Stat() (fs.FileInfo, error) {
	return &TestFileInfo{name: f.name, size: int64(len(f.content))}, nil
}

func (f *TestFile) Read(b []byte) (int, error) {
	if f.offset >= int64(len(f.content)) {
		return 0, io.EOF
	}

	n := copy(b, f.content[f.offset:])
	f.offset += int64(n)
	return n, nil
}

func (f *TestFile) Close() error {
	return nil
}

type TestFileInfo struct {
	name string
	size int64
}

func (fi *TestFileInfo) Name() string       { return fi.name }
func (fi *TestFileInfo) Size() int64        { return fi.size }
func (fi *TestFileInfo) Mode() fs.FileMode  { return 0644 }
func (fi *TestFileInfo) ModTime() time.Time { return time.Now() }
func (fi *TestFileInfo) IsDir() bool        { return false }
func (fi *TestFileInfo) Sys() interface{}   { return nil }

func TestWithCustomFS(t *testing.T) {
	customFS := &TestFs{
		files: map[string]string{
			"custom.txt": "content from custom FS",
		},
	}

	memFS := fstest.MapFS{
		"mem-file.txt": &fstest.MapFile{
			Data: []byte("content from memory"),
		},
	}

	composite := cfs.NewCompositeFS(memFS, customFS)

	testReadFile(t, composite, "custom.txt", "content from custom FS")

	testReadFile(t, composite, "mem-file.txt", "content from memory")
}

//go:embed testdata/theme/*
var themeFS embed.FS

//go:embed testdata/embedded/*
var embeddedFS embed.FS

func TestRealWorldViewEngine(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "view-engine-test")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	devTemplateDir := filepath.Join(tempDir, "views")
	err = os.MkdirAll(devTemplateDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create dev template directory: %v", err)
	}

	devTemplatePath := filepath.Join(devTemplateDir, "home.html")
	err = os.WriteFile(devTemplatePath, []byte("DEV OVERRIDE: Home Template"), 0644)
	if err != nil {
		t.Fatalf("Failed to write dev template: %v", err)
	}

	devOnlyPath := filepath.Join(devTemplateDir, "dev-only.html")
	err = os.WriteFile(devOnlyPath, []byte("DEV ONLY: Development Template"), 0644)
	if err != nil {
		t.Fatalf("Failed to write dev-only template: %v", err)
	}

	embeddedSubFS, err := fs.Sub(embeddedFS, "testdata/embedded")
	if err != nil {
		t.Fatalf("Failed to create embedded sub FS: %v", err)
	}

	themeSubFS, err := fs.Sub(themeFS, "testdata/theme")
	if err != nil {
		t.Fatalf("Failed to create theme sub FS: %v", err)
	}

	templateFS := cfs.NewCompositeFS(
		os.DirFS(tempDir), // Development overrides (highest priority)
		themeSubFS,        // Theme templates (medium priority)
		embeddedSubFS,     // Base templates (lowest priority)
	)

	// Test cases
	tests := []struct {
		name     string
		path     string
		expected string
		exists   bool
	}{
		{
			name:     "Base template",
			path:     "views/about.html",
			expected: "BASE: About Template\n",
			exists:   true,
		},
		{
			name:     "Theme override",
			path:     "views/contact.html",
			expected: "THEME OVERRIDE: Contact Template\n",
			exists:   true,
		},
		{
			name:     "Dev override",
			path:     "views/home.html",
			expected: "DEV OVERRIDE: Home Template",
			exists:   true,
		},
		{
			name:     "Dev-only template",
			path:     "views/dev-only.html",
			expected: "DEV ONLY: Development Template",
			exists:   true,
		},
		{
			name:   "Non-existent template",
			path:   "views/nonexistent.html",
			exists: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := templateFS.Open(tt.path)
			if !tt.exists {
				if err == nil {
					t.Errorf("Expected file %s not to exist but it does", tt.path)
				}
				return
			}

			if err != nil {
				t.Fatalf("Failed to open %s: %v", tt.path, err)
			}
			defer file.Close()

			content, err := io.ReadAll(file)
			if err != nil {
				t.Fatalf("Failed to read %s: %v", tt.path, err)
			}

			if string(content) != tt.expected {
				t.Errorf("Expected content %q, got %q", tt.expected, string(content))
			}
		})
	}

	entries, err := cfs.ReadDir(templateFS, "views")
	if err != nil {
		t.Fatalf("Failed to read directory: %v", err)
	}

	fileNames := make(map[string]bool)
	for _, entry := range entries {
		fileNames[entry.Name()] = true
	}

	expectedFiles := []string{"about.html", "contact.html", "home.html", "dev-only.html"}
	for _, expectedFile := range expectedFiles {
		if !fileNames[expectedFile] {
			t.Errorf("Expected directory listing to contain %s but it doesn't", expectedFile)
		}
	}
}

func TestPartialOverride(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping test that creates actual files")
	}

	tempDir, err := os.MkdirTemp("", "partial-test")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	partialsDir := filepath.Join(tempDir, "views", "partials")
	err = os.MkdirAll(partialsDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create partials directory: %v", err)
	}

	err = os.WriteFile(
		filepath.Join(partialsDir, "header.html"),
		[]byte("DEV HEADER PARTIAL"),
		0644,
	)
	if err != nil {
		t.Fatalf("Failed to write header partial: %v", err)
	}

	embeddedSubFS, err := fs.Sub(embeddedFS, "testdata/embedded")
	if err != nil {
		t.Fatalf("Failed to create embedded sub FS: %v", err)
	}

	compositeFS := cfs.NewCompositeFS(
		os.DirFS(tempDir), // Dev files first
		embeddedSubFS,     // Then embedded files
	)

	file, err := compositeFS.Open("views/partials/header.html")
	if err != nil {
		t.Fatalf("Failed to open partial: %v", err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("Failed to read partial: %v", err)
	}

	if string(content) != "DEV HEADER PARTIAL" {
		t.Errorf("Expected dev partial content, got %q", string(content))
	}

	file, err = compositeFS.Open("views/partials/footer.html")
	if err != nil {
		t.Fatalf("Failed to open footer partial: %v", err)
	}
	defer file.Close()

	content, err = io.ReadAll(file)
	if err != nil {
		t.Fatalf("Failed to read footer partial: %v", err)
	}

	if !strings.Contains(string(content), "BASE: Footer Partial") {
		t.Errorf("Expected base footer content, got %q", string(content))
	}
}
