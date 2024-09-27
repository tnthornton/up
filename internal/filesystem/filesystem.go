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
	"io/fs"
	"os"
	"path/filepath"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/spf13/afero"
)

// writeFile writing with error checking
func WriteFile(path string, data []byte, perm os.FileMode) error {
	err := os.MkdirAll(filepath.Dir(path), 0o755) // nolint:gosec
	if err != nil {
		return errors.Wrapf(err, "failed to create directory %q", filepath.Dir(path))
	}
	return errors.Wrapf(os.WriteFile(path, data, perm), "failed to write file %q", path)
}

// CopyGeneratedFiles to the target file system
func CopyGeneratedFiles(baseFolder, schemaFolder string, toFs afero.Fs) error {
	wDir := createFileAndDirCopy(toFs, filepath.Join(baseFolder, schemaFolder), "")
	return filepath.WalkDir(filepath.Join(baseFolder, schemaFolder), wDir)
}

// createFileAndDirCopy returns a fs.WalkDirFunc function that copies files and directories
// from a source directory to a destination directory using the provided afero filesystem.
func createFileAndDirCopy(afs afero.Fs, sourceRoot, destRoot string) fs.WalkDirFunc {
	cleanRoot := filepath.Clean(sourceRoot)

	return func(path string, di fs.DirEntry, err error) error {
		if err != nil {
			return err // Handle any errors encountered during traversal
		}

		// Compute relative path from the source root
		relPath, err := filepath.Rel(cleanRoot, path)
		if err != nil {
			return err
		}

		// Construct the output path relative to the destination root
		outFilePath := filepath.Join(destRoot, relPath)

		// If it's a directory, use MkdirAll to create the structure
		if di.IsDir() {
			err := afs.MkdirAll(outFilePath, 0755)
			if err != nil {
				return err
			}
		} else {
			// If it's a file, read the content and write it to the destination
			content, err := os.ReadFile(filepath.Clean(path))
			if err != nil {
				return err
			}

			err = afero.WriteFile(afs, outFilePath, content, 0644)
			if err != nil {
				return err
			}
		}

		return nil
	}
}
