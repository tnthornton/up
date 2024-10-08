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

package schema

import (
	"archive/tar"
	"bytes"
	"io"
	"os"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/spf13/afero"
)

// Parser is a Parser implementation for generic schema.
type Parser struct {
	Filesystem afero.Fs
	rootPath   string
	tw         *tar.Writer
	mode       os.FileMode
	tarBuf     *bytes.Buffer
}

// New creates a new Parser instance.
func New(filesystem afero.Fs, rootPath string, mode os.FileMode) *Parser {
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)
	return &Parser{
		Filesystem: filesystem,
		rootPath:   rootPath,
		tw:         tw,
		mode:       mode,
		tarBuf:     tarBuf,
	}
}

// Generate walks through the filesystem, collects files, and generates a tarball in a buffer.
func (p *Parser) Generate() ([]byte, error) {
	// Only proceed if the filesystem is not nil
	if p.Filesystem == nil {
		// Return nil, nil because it's allowed to be nil and no generation is required
		return nil, nil
	}

	err := afero.Walk(p.Filesystem, p.rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return errors.Wrapf(err, "failed to access %s", path)
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Open the file to read its contents
		file, err := p.Filesystem.Open(path)
		if err != nil {
			return errors.Wrapf(err, "failed to open file %s", path)
		}
		defer func() {
			if cerr := file.Close(); cerr != nil && err == nil {
				err = errors.Wrapf(cerr, "failed to close file %s", path)
			}
		}()

		// Read the file content into a bytes.Buffer
		buf := new(bytes.Buffer)
		if _, err := io.Copy(buf, file); err != nil {
			return errors.Wrapf(err, "failed to read file content %s", path)
		}

		// Add the file directly to the tar archive
		if err := p.addFileToTar(path, buf); err != nil {
			return errors.Wrapf(err, "failed to add file %s to tar", path)
		}

		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to walk through filesystem")
	}

	return p.finalizeTar()
}

// addFileToTar writes a file's contents to the tarball.
func (p *Parser) addFileToTar(path string, buf *bytes.Buffer) error {
	hdr := &tar.Header{
		Name: path,
		Mode: int64(p.mode),
		Size: int64(buf.Len()),
	}

	if err := p.tw.WriteHeader(hdr); err != nil {
		return errors.Wrap(err, "failed to write tar header")
	}

	if _, err := io.Copy(p.tw, buf); err != nil {
		return errors.Wrap(err, "failed to write file to tar")
	}

	return nil
}

// finalizeTar closes the tar writer and returns the tarball []byte.
func (p *Parser) finalizeTar() ([]byte, error) {
	if err := p.tw.Close(); err != nil {
		return nil, errors.Wrap(err, "failed to close tar writer")
	}

	return p.tarBuf.Bytes(), nil
}
