// Copyright 2024 Upbound Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package filesystem

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

type fileInfo struct {
	mode int64
	uid  int
	gid  int
}

func TestFSToTar(t *testing.T) {
	// Helper function to read the contents of a TAR file.
	readTar := func(tarData []byte) map[string]*tar.Header {
		tr := tar.NewReader(bytes.NewReader(tarData))
		files := map[string]*tar.Header{}
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			files[hdr.Name] = hdr
		}
		return files
	}

	tests := []struct {
		name          string
		setupFs       func(fs afero.Fs)
		prefix        string
		opts          []FSToTarOption
		expectErr     bool
		expectedFiles map[string]fileInfo
	}{
		{
			name: "SimpleFileTarWithPrefix",
			setupFs: func(fs afero.Fs) {
				// Create a file in the in-memory file system.
				afero.WriteFile(fs, "file.txt", []byte("test content"), os.ModePerm)
			},
			prefix: "my-prefix/",
			expectedFiles: map[string]fileInfo{
				"my-prefix/":         {mode: 0777},
				"my-prefix/file.txt": {mode: 0777},
			},
		},
		{
			name: "NonRegularFileDirectory",
			setupFs: func(fs afero.Fs) {
				// Create a directory, which should be ignored by FSToTar.
				fs.Mkdir("dir", os.ModePerm)
			},
			prefix: "my-prefix/",
			expectedFiles: map[string]fileInfo{
				"my-prefix/": {mode: 0777}, // Only prefix should exist, no dir should be included.
			},
		},
		{
			name: "FilesystemWithMultipleFiles",
			setupFs: func(fs afero.Fs) {
				// Create multiple files in the in-memory file system.
				afero.WriteFile(fs, "file1.txt", []byte("test content 1"), os.ModePerm)
				afero.WriteFile(fs, "file2.txt", []byte("test content 2"), os.ModePerm)
			},
			prefix: "another-prefix/",
			expectedFiles: map[string]fileInfo{
				"another-prefix/":          {mode: 0777},
				"another-prefix/file1.txt": {mode: 0777},
				"another-prefix/file2.txt": {mode: 0777},
			},
		},
		{
			name: "UIDOverride",
			setupFs: func(fs afero.Fs) {
				// Create multiple files in the in-memory file system.
				afero.WriteFile(fs, "file1.txt", []byte("test content 1"), os.ModePerm)
				afero.WriteFile(fs, "file2.txt", []byte("test content 2"), os.ModePerm)
			},
			prefix: "my-prefix/",
			opts: []FSToTarOption{
				WithUIDOverride(2345),
			},
			expectedFiles: map[string]fileInfo{
				"my-prefix/":          {mode: 0777, uid: 2345},
				"my-prefix/file1.txt": {mode: 0777, uid: 2345},
				"my-prefix/file2.txt": {mode: 0777, uid: 2345},
			},
		},
		{
			name: "GIDOverride",
			setupFs: func(fs afero.Fs) {
				// Create multiple files in the in-memory file system.
				afero.WriteFile(fs, "file1.txt", []byte("test content 1"), os.ModePerm)
				afero.WriteFile(fs, "file2.txt", []byte("test content 2"), os.ModePerm)
			},
			prefix: "my-prefix/",
			opts: []FSToTarOption{
				WithGIDOverride(2345),
			},
			expectedFiles: map[string]fileInfo{
				"my-prefix/":          {mode: 0777, gid: 2345},
				"my-prefix/file1.txt": {mode: 0777, gid: 2345},
				"my-prefix/file2.txt": {mode: 0777, gid: 2345},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup in-memory file system.
			fs := afero.NewMemMapFs()

			// Apply the setup function for the file system.
			tt.setupFs(fs)

			// Run the FSToTar function.
			tarData, err := FSToTar(fs, tt.prefix, tt.opts...)

			// Validate errors if expected.
			if tt.expectErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Read the TAR contents.
			files := readTar(tarData)

			// Validate that the correct files were included.
			for expectedFile, expectedInfo := range tt.expectedFiles {
				file, ok := files[expectedFile]
				require.True(t, ok, "%s not found in tar", expectedFile)

				require.Equal(t, expectedInfo.mode, file.Mode, "Incorrect file mode for %s", expectedFile)
				require.Equal(t, expectedInfo.uid, file.Uid, "Incorrect UID for %s", expectedFile)
				require.Equal(t, expectedInfo.gid, file.Gid, "Incorrect GID for %s", expectedFile)
			}
		})
	}
}

func TestCopyFilesBetweenFs(t *testing.T) {
	tests := []struct {
		name          string
		setupFromFs   func(fromFS afero.Fs)
		setupToFs     func(toFS afero.Fs)
		expectedFiles map[string]string // Map of file paths to their expected content in destination filesystem
		expectErr     bool
	}{
		{
			name: "CopySingleFile",
			setupFromFs: func(fromFS afero.Fs) {
				// Setup source filesystem with a single file.
				afero.WriteFile(fromFS, "file.txt", []byte("file content"), os.ModePerm)
			},
			setupToFs: func(toFS afero.Fs) {
				// No setup needed for destination filesystem.
			},
			expectedFiles: map[string]string{
				"file.txt": "file content", // File content should be the same
			},
		},
		{
			name: "SkipDirectories",
			setupFromFs: func(fromFS afero.Fs) {
				// Setup source filesystem with a file inside a directory.
				fromFS.Mkdir("dir", os.ModePerm)
				afero.WriteFile(fromFS, "dir/file.txt", []byte("nested file content"), os.ModePerm)
			},
			setupToFs: func(toFS afero.Fs) {
				// No setup needed for destination filesystem.
			},
			expectedFiles: map[string]string{
				"dir/file.txt": "nested file content", // Only the file inside the directory should be copied.
			},
		},
		{
			name: "MultipleFilesInRoot",
			setupFromFs: func(fromFS afero.Fs) {
				// Setup source filesystem with multiple files.
				afero.WriteFile(fromFS, "file1.txt", []byte("file 1 content"), os.ModePerm)
				afero.WriteFile(fromFS, "file2.txt", []byte("file 2 content"), os.ModePerm)
			},
			setupToFs: func(toFS afero.Fs) {
				// No setup needed for destination filesystem.
			},
			expectedFiles: map[string]string{
				"file1.txt": "file 1 content",
				"file2.txt": "file 2 content",
			},
		},
		{
			name: "FileOverwriteInDestination",
			setupFromFs: func(fromFS afero.Fs) {
				// Setup source filesystem with a file.
				afero.WriteFile(fromFS, "file.txt", []byte("new file content"), os.ModePerm)
			},
			setupToFs: func(toFS afero.Fs) {
				// Setup destination filesystem with an existing file.
				afero.WriteFile(toFS, "file.txt", []byte("old file content"), os.ModePerm)
			},
			expectedFiles: map[string]string{
				"file.txt": "new file content", // The content should be overwritten in the destination.
			},
		},
		{
			name: "CopyFileInNestedDirectory",
			setupFromFs: func(fromFS afero.Fs) {
				// Setup source filesystem with a file deep inside nested directories.
				fromFS.MkdirAll("dir1/dir2", os.ModePerm)
				afero.WriteFile(fromFS, "dir1/dir2/file.txt", []byte("deep nested file content"), os.ModePerm)
			},
			setupToFs: func(toFS afero.Fs) {
				// No setup needed for destination filesystem.
			},
			expectedFiles: map[string]string{
				"dir1/dir2/file.txt": "deep nested file content", // Ensure nested directories are created and file copied.
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup in-memory filesystems.
			fromFS := afero.NewMemMapFs()
			toFS := afero.NewMemMapFs()

			// Apply file system setup for the test case.
			tt.setupFromFs(fromFS)
			tt.setupToFs(toFS)

			// Run the CopyFilesBetweenFs function.
			err := CopyFilesBetweenFs(fromFS, toFS)

			// Validate errors if expected.
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Validate that the expected files exist in the destination filesystem.
			for filePath, expectedContent := range tt.expectedFiles {
				data, err := afero.ReadFile(toFS, filePath)
				require.NoError(t, err, "Expected file %s not found in destination filesystem", filePath)
				require.Equal(t, expectedContent, string(data), "Content mismatch for file %s", filePath)
			}
		})
	}
}

// TestIsFsEmpty tests the IsFsEmpty function.
func TestIsFsEmpty(t *testing.T) {
	tests := []struct {
		name          string
		setupFs       func(fs afero.Fs)
		expectedEmpty bool
		expectErr     bool
	}{
		{
			name: "EmptyFileSystem",
			setupFs: func(fs afero.Fs) {
			},
			expectedEmpty: true,
			expectErr:     false,
		},
		{
			name: "SingleFileInRoot",
			setupFs: func(fs afero.Fs) {
				afero.WriteFile(fs, "file.txt", []byte("content"), os.ModePerm)
			},
			expectedEmpty: false,
			expectErr:     false,
		},
		{
			name: "NestedDirectoryWithFile",
			setupFs: func(fs afero.Fs) {
				fs.MkdirAll("dir", os.ModePerm)
				afero.WriteFile(fs, "dir/file.txt", []byte("content"), os.ModePerm)
			},
			expectedEmpty: false,
			expectErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()

			tt.setupFs(fs)

			isEmpty, err := IsFsEmpty(fs)

			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tt.expectedEmpty, isEmpty)
		})
	}
}

func TestCopyFolder(t *testing.T) {
	tests := []struct {
		name        string
		setupFs     func(fs afero.Fs)
		sourceDir   string
		targetDir   string
		expectedErr bool
		verifyFs    func(fs afero.Fs, t *testing.T)
	}{
		{
			name:      "CopyEmptyDirectory",
			sourceDir: "/source",
			targetDir: "/target",
			setupFs: func(fs afero.Fs) {
				fs.MkdirAll("/source", os.ModePerm)
			},
			expectedErr: false,
			verifyFs: func(fs afero.Fs, t *testing.T) {
				exists, err := afero.DirExists(fs, "/target")
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !exists {
					t.Errorf("expected target directory to exist, but it does not")
				}
			},
		},
		{
			name:      "CopySingleFile",
			sourceDir: "/source",
			targetDir: "/target",
			setupFs: func(fs afero.Fs) {
				fs.MkdirAll("/source", os.ModePerm)
				afero.WriteFile(fs, "/source/file1.txt", []byte("content"), 0644)
			},
			expectedErr: false,
			verifyFs: func(fs afero.Fs, t *testing.T) {
				exists, err := afero.Exists(fs, "/target/file1.txt")
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !exists {
					t.Errorf("expected file to be copied to target, but it does not exist")
				}

				content, err := afero.ReadFile(fs, "/target/file1.txt")
				if err != nil {
					t.Errorf("unexpected error reading file: %v", err)
				}
				if string(content) != "content" {
					t.Errorf("expected file content 'content', got '%s'", string(content))
				}
			},
		},
		{
			name:      "CopyNestedDirectories",
			sourceDir: "/source",
			targetDir: "/target",
			setupFs: func(fs afero.Fs) {
				fs.MkdirAll("/source/dir1/dir2", os.ModePerm)
				afero.WriteFile(fs, "/source/dir1/file1.txt", []byte("file1 content"), 0644)
				afero.WriteFile(fs, "/source/dir1/dir2/file2.txt", []byte("file2 content"), 0644)
			},
			expectedErr: false,
			verifyFs: func(fs afero.Fs, t *testing.T) {
				exists, err := afero.Exists(fs, "/target/dir1/file1.txt")
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !exists {
					t.Errorf("expected file1 to be copied to target, but it does not exist")
				}

				exists, err = afero.Exists(fs, "/target/dir1/dir2/file2.txt")
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !exists {
					t.Errorf("expected file2 to be copied to target, but it does not exist")
				}

				content1, err := afero.ReadFile(fs, "/target/dir1/file1.txt")
				if err != nil {
					t.Errorf("unexpected error reading file1: %v", err)
				}
				if string(content1) != "file1 content" {
					t.Errorf("expected file1 content 'file1 content', got '%s'", string(content1))
				}

				content2, err := afero.ReadFile(fs, "/target/dir1/dir2/file2.txt")
				if err != nil {
					t.Errorf("unexpected error reading file2: %v", err)
				}
				if string(content2) != "file2 content" {
					t.Errorf("expected file2 content 'file2 content', got '%s'", string(content2))
				}
			},
		},
		{
			name:        "SourceDirDoesNotExist",
			sourceDir:   "/nonexistent",
			targetDir:   "/target",
			setupFs:     func(fs afero.Fs) {},
			expectedErr: true,
			verifyFs: func(fs afero.Fs, t *testing.T) {
				exists, err := afero.DirExists(fs, "/target")
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if exists {
					t.Errorf("expected target directory not to exist when source does not exist, but it does")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			tt.setupFs(fs)

			err := CopyFolder(fs, tt.sourceDir, tt.targetDir)
			if tt.expectedErr && err == nil {
				t.Errorf("expected an error, but got none")
			} else if !tt.expectedErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			tt.verifyFs(fs, t)
		})
	}
}

func TestCopyFileIfExists(t *testing.T) {
	tests := []struct {
		name        string
		setupFs     func(fs afero.Fs)
		src         string
		dst         string
		expectedErr bool
		verifyFs    func(fs afero.Fs, t *testing.T)
	}{
		{
			name: "SourceFileExists",
			src:  "/source/file.txt",
			dst:  "/destination/file.txt",
			setupFs: func(fs afero.Fs) {
				fs.MkdirAll("/source", os.ModePerm)
				afero.WriteFile(fs, "/source/file.txt", []byte("file content"), 0644)
			},
			expectedErr: false,
			verifyFs: func(fs afero.Fs, t *testing.T) {
				exists, err := afero.Exists(fs, "/destination/file.txt")
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !exists {
					t.Errorf("expected destination file to exist, but it does not")
				}

				content, err := afero.ReadFile(fs, "/destination/file.txt")
				if err != nil {
					t.Errorf("unexpected error reading file: %v", err)
				}
				if string(content) != "file content" {
					t.Errorf("expected file content 'file content', got '%s'", string(content))
				}
			},
		},
		{
			name: "SourceFileDoesNotExist",
			src:  "/source/nonexistent.txt",
			dst:  "/destination/file.txt",
			setupFs: func(fs afero.Fs) {
				fs.MkdirAll("/source", os.ModePerm)
			},
			expectedErr: false,
			verifyFs: func(fs afero.Fs, t *testing.T) {
				exists, err := afero.Exists(fs, "/destination/file.txt")
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if exists {
					t.Errorf("expected destination file not to exist when source does not exist")
				}
			},
		},
		{
			name: "OverwriteDestinationFile",
			src:  "/source/file.txt",
			dst:  "/destination/file.txt",
			setupFs: func(fs afero.Fs) {
				fs.MkdirAll("/source", os.ModePerm)
				fs.MkdirAll("/destination", os.ModePerm)
				afero.WriteFile(fs, "/source/file.txt", []byte("new content"), 0644)
				afero.WriteFile(fs, "/destination/file.txt", []byte("old content"), 0644)
			},
			expectedErr: false,
			verifyFs: func(fs afero.Fs, t *testing.T) {
				content, err := afero.ReadFile(fs, "/destination/file.txt")
				if err != nil {
					t.Errorf("unexpected error reading file: %v", err)
				}
				if string(content) != "new content" {
					t.Errorf("expected file content 'new content', got '%s'", string(content))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			tt.setupFs(fs)

			err := CopyFileIfExists(fs, tt.src, tt.dst)
			if tt.expectedErr && err == nil {
				t.Errorf("expected an error, but got none")
			} else if !tt.expectedErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			tt.verifyFs(fs, t)
		})
	}
}
