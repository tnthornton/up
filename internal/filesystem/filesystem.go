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

type fsToTarConfig struct {
	symlinkBasePath *string
	uidOverride     *int
	gidOverride     *int
}

type FSToTarOption func(*fsToTarConfig)

func WithSymlinkBasePath(bp string) FSToTarOption {
	return func(opts *fsToTarConfig) {
		opts.symlinkBasePath = &bp
	}
}

func WithUIDOverride(uid int) FSToTarOption {
	return func(opts *fsToTarConfig) {
		opts.uidOverride = &uid
	}
}

func WithGIDOverride(gid int) FSToTarOption {
	return func(opts *fsToTarConfig) {
		opts.gidOverride = &gid
	}
}

func FSToTar(f afero.Fs, prefix string, opts ...FSToTarOption) ([]byte, error) { // nolint:gocyclo
	cfg := &fsToTarConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	prefixHdr := &tar.Header{
		Name:     prefix,
		Typeflag: tar.TypeDir,
		Mode:     0777,
	}
	if cfg.uidOverride != nil {
		prefixHdr.Uid = *cfg.uidOverride
	}
	if cfg.gidOverride != nil {
		prefixHdr.Gid = *cfg.gidOverride
	}

	err := tw.WriteHeader(prefixHdr)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create prefix directory in tar archive")
	}
	err = afero.Walk(f, ".", func(name string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			h, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			h.Name = filepath.Join(prefix, name)
			if cfg.uidOverride != nil {
				h.Uid = *cfg.uidOverride
			}
			if cfg.gidOverride != nil {
				h.Gid = *cfg.gidOverride
			}
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			return nil
		}

		if info.Mode()&os.ModeSymlink != 0 {
			if cfg.symlinkBasePath == nil {
				return errors.New("cannot follow symlinks unless base path is configured")
			}

			// Handle symlink by using afero.OsFs to resolve it
			osFs := afero.NewOsFs()
			symlinkBasePath := filepath.Join(*cfg.symlinkBasePath, name)

			// Since symlink points outside BasePathFs, use osFs to resolve it
			targetPath, err := filepath.EvalSymlinks(symlinkBasePath)
			if err != nil {
				// The symlink target may be missing, which can occur when dependencies are only referenced without schemas.
				// It's safe to skip these symlinks by returning nil, allowing the packaging to continue without interruption.
				return nil // nolint:nilerr
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
				if cfg.uidOverride != nil {
					targetHeader.Uid = *cfg.uidOverride
				}
				if cfg.gidOverride != nil {
					targetHeader.Gid = *cfg.gidOverride
				}

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
			if cfg.uidOverride != nil {
				h.Uid = *cfg.uidOverride
			}
			if cfg.gidOverride != nil {
				h.Gid = *cfg.gidOverride
			}
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

// CopyFolder recursively copies directory and all its contents from sourceDir to targetDir.
func CopyFolder(fs afero.Fs, sourceDir, targetDir string) error {
	return afero.Walk(fs, sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return errors.Wrapf(err, "failed to determine relative path for %s", path)
		}

		// Define the target path by joining targetDir with the relative path
		destPath := filepath.Join(targetDir, relPath)

		if info.IsDir() {
			return fs.MkdirAll(destPath, 0755)
		}

		srcFile, err := fs.Open(path)
		if err != nil {
			return errors.Wrapf(err, "failed to open source file %s", path)
		}

		destFile, err := fs.Create(destPath)
		if err != nil {
			return errors.Wrapf(err, "failed to create destination file %s", destPath)
		}

		_, err = io.Copy(destFile, srcFile)
		if err != nil {
			return errors.Wrapf(err, "failed to copy file from %s to %s", path, destPath)
		}

		return nil
	})
}

// CopyFileIfExists copies a file from src to dst if the src file exists.
func CopyFileIfExists(fs afero.Fs, src, dst string) error {
	exists, err := afero.Exists(fs, src)
	if err != nil {
		return err
	}

	if !exists {
		return nil // Skip if the file does not exist
	}

	// Copy the file
	srcFile, err := fs.Open(src)
	if err != nil {
		return errors.Wrapf(err, "failed to open source file %s", src)
	}

	destFile, err := fs.Create(dst)
	if err != nil {
		return errors.Wrapf(err, "failed to create destination file %s", dst)
	}

	_, err = io.Copy(destFile, srcFile)
	if err != nil {
		return errors.Wrapf(err, "failed to copy file from %s to %s", src, dst)
	}

	return nil
}

// FindNestedFoldersWithPattern finds nested folders containing files that match a specified pattern.
func FindNestedFoldersWithPattern(fs afero.Fs, root string, pattern string) ([]string, error) {
	var foldersWithFiles []string

	err := afero.Walk(fs, root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Only process directories
		if !info.IsDir() {
			return nil
		}

		// Check if this directory contains any files matching the pattern
		files, err := afero.ReadDir(fs, path)
		if err != nil {
			return err
		}

		for _, f := range files {
			if f.IsDir() {
				continue
			}

			// Perform the pattern match check
			match, _ := filepath.Match(pattern, f.Name())
			if match {
				// Only add the directory path (not the file path)
				foldersWithFiles = append(foldersWithFiles, path)
				break
			}
		}

		return nil
	})

	return foldersWithFiles, err
}
