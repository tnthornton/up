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
	"path/filepath"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/spf13/afero"
)

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
		err = afero.WriteFile(toFS, path, fileData, info.Mode())
		if err != nil {
			return err
		}

		return nil
	})

	return err
}

func FSToTar(f afero.Fs, prefix string) ([]byte, error) {
	// Copied from tar.AddFS but prepend the prefix.
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
		// TODO(#49580): Handle symlinks when fs.ReadLinkFS is available.
		if !info.Mode().IsRegular() {
			return errors.New("tar: cannot add non-regular file")
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.Join(prefix, name)
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		f, err := f.Open(name)
		if err != nil {
			return err
		}
		defer f.Close() //nolint:errcheck // Copied from upstream.
		_, err = io.Copy(tw, f)
		return err
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
