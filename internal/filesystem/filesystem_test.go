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
		expectErr     bool
		expectedFiles map[string]int64 // File paths and their expected modes
	}{
		{
			name: "SimpleFileTarWithPrefix",
			setupFs: func(fs afero.Fs) {
				// Create a file in the in-memory file system.
				afero.WriteFile(fs, "file.txt", []byte("test content"), os.ModePerm)
			},
			prefix: "my-prefix/",
			expectedFiles: map[string]int64{
				"my-prefix/":         0,
				"my-prefix/file.txt": 0777,
			},
		},
		{
			name: "NonRegularFileDirectory",
			setupFs: func(fs afero.Fs) {
				// Create a directory, which should be ignored by FSToTar.
				fs.Mkdir("dir", os.ModePerm)
			},
			prefix: "my-prefix/",
			expectedFiles: map[string]int64{
				"my-prefix/": 0, // Only prefix should exist, no dir should be included.
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
			expectedFiles: map[string]int64{
				"another-prefix/":          0,
				"another-prefix/file1.txt": 0777,
				"another-prefix/file2.txt": 0777,
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
			tarData, err := FSToTar(fs, tt.prefix)

			// Validate errors if expected.
			if tt.expectErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Read the TAR contents.
			files := readTar(tarData)

			// Validate that the correct files were included.
			for expectedFile, expectedMode := range tt.expectedFiles {
				file, ok := files[expectedFile]
				require.True(t, ok, "%s not found in tar", expectedFile)

				// Only validate the mode if it's a file (non-directory).
				if expectedMode != 0 {
					require.Equal(t, expectedMode, file.Mode, "Incorrect file mode for %s", expectedFile)
				}
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
