//
//  fs.go
//  ropa
//
//  Created by p2k on 23.04.2023
//  Copyright (c) Patrick "p2k" Schneider
//
//  Licensed under the EUPL, Version 1.2 or – as soon they will be approved by
//  the European Commission - subsequent versions of the EUPL (the "Licence");
//  You may not use this work except in compliance with the Licence.
//  You may obtain a copy of the Licence at:
//
//  https://joinup.ec.europa.eu/collection/eupl/eupl-text-eupl-12
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the Licence is distributed on an "AS IS" basis,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the Licence for the specific language governing permissions and
//  limitations under the Licence.
//

package grf

import (
	"bytes"
	"compress/zlib"
	"hash"
	"io"
	"io/fs"
	"path"
	"sort"
	"time"

	"golang.org/x/crypto/blake2b"
)

// ArchivedFileInfo implements the FileInfo interface for files within a GRF.
type ArchivedFileInfo struct {
	h       *FileHeader
	dirname string
}

func (fi *ArchivedFileInfo) Name() string {
	if fi.h != nil {
		return path.Base(fi.h.Name)
	}
	return fi.dirname
}

func (fi *ArchivedFileInfo) Size() int64 {
	if fi.h != nil {
		return int64(fi.h.Size)
	}
	return 0
}

func (fi *ArchivedFileInfo) Mode() fs.FileMode {
	if fi.h != nil {
		var perm fs.FileMode = 0644
		if fi.h.Linkname != "" {
			return perm | fs.ModeSymlink
		}
		return perm
	}
	return fs.ModeDir | 0755
}

func (fi *ArchivedFileInfo) ModTime() time.Time {
	return time.Time{}
}

func (fi *ArchivedFileInfo) IsDir() bool {
	return fi.h == nil
}

func (fi *ArchivedFileInfo) Sys() any {
	return nil
}

// ArchivedFile implements the File and ReadDirFile interface for files within a GRF.
type ArchivedFile struct {
	a       *Archive
	h       *FileHeader
	dirname string

	lr *io.LimitedReader
	zr io.ReadCloser
	hs hash.Hash
	tr io.Reader

	entries []fs.DirEntry
}

func (f *ArchivedFile) Stat() (fs.FileInfo, error) {
	if f.h != nil {
		return &ArchivedFileInfo{h: f.h}, nil
	}
	return &ArchivedFileInfo{dirname: f.dirname}, nil
}

func (f *ArchivedFile) Read(p []byte) (int, error) {
	if f.lr == nil {
		return 0, io.EOF
	}

	f.a.m.Lock()
	defer f.a.m.Unlock()

	if f.a.lf != f {
		_, err := f.a.file.Seek(int64(f.h.Offset+f.h.CompressedSize)-f.lr.N+46, io.SeekStart)
		if err != nil {
			return 0, err
		}
		f.a.lf = f
	}

	n, err := f.tr.Read(p)
	if err == io.EOF {
		if f.hs != nil {
			copy(f.h.Hash[:], f.hs.Sum(nil))
			f.hs = nil
			if f.zr != nil {
				f.tr = f.zr
			} else {
				f.tr = f.lr
			}
		}
	}

	return n, err
}

func (f *ArchivedFile) Close() error {
	if f.lr == nil {
		return nil
	}

	f.lr = nil
	f.hs = nil
	f.tr = nil

	if f.zr != nil {
		err := f.zr.Close()
		f.zr = nil
		if err != nil {
			return err
		}
	}

	return nil
}

func (f *ArchivedFile) ReadDir(n int) ([]fs.DirEntry, error) {
	if f.h != nil {
		return nil, fs.ErrInvalid
	}
	if f.entries == nil {
		return nil, io.EOF
	}
	l := len(f.entries)
	if n <= 0 || n > l {
		n = l
	}
	result := f.entries[:n]
	if n == l {
		f.entries = nil
	} else {
		f.entries = f.entries[n:]
	}
	return result, nil
}

func (a *Archive) doReadDir(name string) ([]fs.DirEntry, error) {
	if a.folders == nil {
		if name == "." {
			return nil, nil
		}
		return nil, fs.ErrNotExist
	}
	f := a.folders[name]
	if f == nil {
		return nil, fs.ErrNotExist
	}
	var entries []fs.DirEntry
	for n := range f {
		nn := path.Join(name, n)
		ff := a.folders[nn]
		if ff != nil {
			entries = append(entries, fs.FileInfoToDirEntry(&ArchivedFileInfo{dirname: n}))
		} else {
			hh := a.entriesByName[nn]
			if hh != nil && !hh.Deleted {
				entries = append(entries, fs.FileInfoToDirEntry(&ArchivedFileInfo{h: hh}))
			}
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// Open opens the named file inside the GRF.
//
// This implements the FS interface. Opening more than one file can be
// detrimental to the overall speed since a seek has to be performed for each
// context switch. Also updates the file's header to include a hash at the end.
func (a *Archive) Open(name string) (fs.File, error) {
	a.m.RLock()
	defer a.m.RUnlock()

	if a.entriesByName == nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	h := a.entriesByName[name]
	if h != nil && h.Deleted {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if h == nil {
		// Maybe a folder
		entries, err := a.doReadDir(name)
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		return &ArchivedFile{dirname: path.Base(name), entries: entries}, nil
	}

	a.file.Seek(int64(h.Offset)+46, io.SeekStart)
	lr := io.LimitReader(a.file, int64(h.CompressedSize))
	f := &ArchivedFile{a: a, h: h, lr: lr.(*io.LimitedReader)}
	a.lf = f

	var err error
	var r io.Reader
	if h.Flags&FileFlag != 0 {
		f.zr, err = zlib.NewReader(f.lr)
		if err != nil {
			return nil, err
		}
		r = f.zr
	} else {
		r = f.lr
	}

	if h.Hash.IsZero() {
		f.hs, err = blake2b.New256(nil)
		if err != nil {
			f.hs = nil
		}
		f.tr = io.TeeReader(r, f.hs)
	} else {
		f.tr = r
	}

	return f, nil
}

// OpenRaw opens the named file inside the GRF without decompressing.
//
// Opening more than one file can be detrimental to the overall speed since a
// seek has to be performed for each context switch. This will not update the
// file's hash.
func (a *Archive) OpenRaw(name string) (fs.File, error) {
	a.m.RLock()
	defer a.m.RUnlock()

	if a.entriesByName == nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	h := a.entriesByName[name]
	if h != nil && h.Deleted {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if h == nil {
		// Maybe a folder
		entries, err := a.doReadDir(name)
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		return &ArchivedFile{dirname: path.Base(name), entries: entries}, nil
	}

	a.file.Seek(int64(h.Offset)+46, io.SeekStart)
	lr := io.LimitReader(a.file, int64(h.CompressedSize))
	f := &ArchivedFile{a: a, h: h, lr: lr.(*io.LimitedReader), tr: lr}
	a.lf = f

	return f, nil
}

// ReadDir reads the named virtual directory and returns a list of directory
// entries sorted by filename.
//
// This implements the ReadDirFS interface.
func (a *Archive) ReadDir(name string) ([]fs.DirEntry, error) {
	a.m.RLock()
	defer a.m.RUnlock()

	entries, err := a.doReadDir(name)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	return entries, nil
}

// ReadFile reads the named file in the GRF and returns its contents.
//
// This implements the ReadFileFS interface.
func (a *Archive) ReadFile(name string) ([]byte, error) {
	buf := &bytes.Buffer{}
	_, err := a.Get(name, buf)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Stat returns a FileInfo describing the file.
//
// This implements the StatFS interface.
func (a *Archive) Stat(name string) (fs.FileInfo, error) {
	a.m.RLock()
	defer a.m.RUnlock()

	if a.entriesByName == nil || a.folders == nil {
		if name == "." {
			return &ArchivedFileInfo{dirname: "."}, nil
		}
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	h := a.entriesByName[name]
	if h != nil {
		if h.Deleted {
			return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
		}
		return &ArchivedFileInfo{h: h}, nil
	}

	f := a.folders[name]
	if f == nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return &ArchivedFileInfo{dirname: path.Base(name)}, nil
}

// Glob returns the names of all files matching pattern
//
// This implements the GlobFS interface.
func (a *Archive) Glob(pattern string) ([]string, error) {
	a.m.RLock()
	defer a.m.RUnlock()

	var result []string
	if a.folders != nil {
		for n := range a.folders {
			if n == "" {
				continue
			}
			m, err := path.Match(pattern, n)
			if err != nil {
				return nil, err
			}
			if m {
				result = append(result, n)
			}
		}
	}
	if a.entriesByName != nil {
		for n := range a.entriesByName {
			m, err := path.Match(pattern, n)
			if err != nil {
				return nil, err
			}
			if m {
				result = append(result, n)
			}
		}
	}
	return result, nil
}
