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
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/spf13/afero"
)

var ErrFsNotEmpty = errors.New("filesystem is not empty")

// CopyFilesBetweenFs copies all files from the source filesystem (fromFS) to the destination filesystem (toFS).
// It traverses through the fromFS filesystem, skipping directories and copying only files.
// File contents and permissions are preserved when writing to toFS.
// Returns an error if any file read, write, or traversal operation fails.
func CopyFilesBetweenFs(fromFS, toFS afero.Fs) error {
	err := afero.Walk(fromFS, ".", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil // Skip directories
		}

		// Ensure the parent directories exist on the destination filesystem
		dir := filepath.Dir(path)
		err = toFS.MkdirAll(dir, 0755) // Use appropriate permissions for the directories
		if err != nil {
			return err
		}

		// Copy the file contents
		fileData, err := afero.ReadFile(fromFS, path)
		if err != nil {
			return err
		}
		err = afero.WriteFile(toFS, path, fileData, 0644)
		if err != nil {
			return err
		}

		return nil
	})

	return err
}

func FSToTar(f afero.Fs, prefix string, osBasePath *string) ([]byte, error) { // nolint:gocyclo
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := tw.WriteHeader(&tar.Header{
		Name:     prefix,
		Typeflag: tar.TypeDir,
		Mode:     0777,
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create prefix directory in tar archive")
	}
	err = afero.Walk(f, ".", func(name string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		if info.Mode()&os.ModeSymlink != 0 {
			// Handle symlink by using afero.OsFs to resolve it
			osFs := afero.NewOsFs()
			symlinkBasePath := filepath.Join(*osBasePath, name)

			// Since symlink points outside BasePathFs, use osFs to resolve it
			targetPath, err := filepath.EvalSymlinks(symlinkBasePath)
			if err != nil {
				return err
			}

			// Ensure the symlink target exists in the real filesystem (OsFs)
			exists, err := afero.Exists(osFs, targetPath)
			if err != nil || !exists {
				return err
			}

			// Walk the external target path using OsFs
			return afero.Walk(osFs, targetPath, func(symlinkedFile string, symlinkedInfo fs.FileInfo, err error) error {
				if err != nil {
					return err
				}

				if symlinkedInfo.IsDir() {
					return nil
				}

				// Add files from the external symlinked target to the tar
				targetHeader, err := tar.FileInfoHeader(symlinkedInfo, "")
				if err != nil {
					return err
				}

				// Adjust the header name to place it under the symlink's directory
				relativePath, err := filepath.Rel(targetPath, symlinkedFile)
				if err != nil {
					return err
				}
				targetHeader.Name = filepath.Join(prefix, name, relativePath)

				if err := tw.WriteHeader(targetHeader); err != nil {
					return err
				}

				targetFile, err := osFs.Open(symlinkedFile)
				if err != nil {
					return err
				}

				_, err = io.Copy(tw, targetFile)
				return err
			})
		}
		if info.Mode().IsRegular() {
			h, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			h.Name = filepath.Join(prefix, name)
			if err := tw.WriteHeader(h); err != nil {
				return err
			}

			file, err := f.Open(name)
			if err != nil {
				return err
			}

			_, err = io.Copy(tw, file)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to populate tar archive")
	}
	err = tw.Close()
	if err != nil {
		return nil, errors.Wrap(err, "failed to close tar archive")
	}

	return buf.Bytes(), nil
}

func CreateSymlink(targetFS *afero.BasePathFs, targetPath string, sourceFS *afero.BasePathFs, sourcePath string) error {
	// Check if the source path exists in sourceFS
	sourceExists, err := afero.Exists(sourceFS, sourcePath)
	if err != nil {
		return errors.Wrapf(err, "failed to check existence of source path: %s", sourcePath)
	}
	if !sourceExists {
		return errors.Errorf("source directory does not exist: %s", sourcePath)
	}

	// Get the real path for targetPath inside targetFS
	realTargetPath, err := targetFS.RealPath(targetPath)
	if err != nil {
		return errors.Wrapf(err, "failed to get real path for targetPath: %s", targetPath)
	}

	// Get the real path for sourcePath inside sourceFS
	realSourcePath, err := sourceFS.RealPath(sourcePath)
	if err != nil {
		return errors.Wrapf(err, "failed to get real path for sourcePath: %s", sourcePath)
	}

	realBasePath := strings.TrimSuffix(realSourcePath, sourcePath)

	// Calculate the relative path from the targetPath's parent directory to the sourcePath
	symlinkParentDir := filepath.Dir(realTargetPath)
	relativeSymlinkPath, err := filepath.Rel(symlinkParentDir, realSourcePath)
	if err != nil {
		return errors.Wrapf(err, "failed to calculate relative symlink path from %s to %s", symlinkParentDir, realSourcePath)
	}

	// Clean the paths to normalize them
	relativeSymlinkPath = filepath.Clean(relativeSymlinkPath)
	realBasePath = filepath.Clean(realBasePath)

	resultRelativeSymlinkPath := relativeSymlinkPath
	if strings.Contains(relativeSymlinkPath, realBasePath) {
		resultRelativeSymlinkPath = strings.Replace(relativeSymlinkPath, realBasePath, "", 1)
	}

	// Join the real base path and target path to get the full symlink target path
	symlinkPath := filepath.Join(realBasePath, realTargetPath)

	// Check if the symlink or file already exists
	if _, err := os.Lstat(symlinkPath); err == nil {
		// If it exists, remove it
		if err := os.Remove(symlinkPath); err != nil {
			return errors.Wrapf(err, "failed to remove existing symlink or file at %s", symlinkPath)
		}
	}

	// Use os.Symlink to create the symlink with the calculated relative path
	if err := os.Symlink(resultRelativeSymlinkPath, symlinkPath); err != nil {
		return errors.Wrapf(err, "failed to create symlink from %s to %s", resultRelativeSymlinkPath, symlinkPath)
	}

	return nil
}

// IsFsEmptycheck if the filesystem is empty
func IsFsEmpty(fs afero.Fs) (bool, error) {
	// Check if the root directory (".") exists
	_, err := fs.Stat(".")
	if err != nil {
		if os.IsNotExist(err) {
			// If the directory doesn't exist, consider it as empty
			return true, nil
		}
		return false, err
	}

	isEmpty, err := afero.IsEmpty(fs, ".")
	if err != nil {
		return false, err
	}

	return isEmpty, nil
}

// FindAllBaseFolders in filesystem from the rootPath and returns a list of base-level directories within the root folder.
func FindAllBaseFolders(fs afero.Fs, rootPath string) ([]string, error) {
	var folders []string

	infos, err := afero.ReadDir(fs, rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Return nil, nil if the directory does not exist
			return nil, nil
		}
		return nil, errors.Wrap(err, "failed to read directory")
	}
	for _, info := range infos {
		if info.IsDir() {
			folders = append(folders, info.Name())
		}
	}

	return folders, nil
}
