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
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"sync"
	"time"

	"lukechampine.com/blake3"
)

var grffsCommonModTime = time.Date(2002, time.August, 31, 12, 0, 0, 0, time.UTC)

// ArchivedFileInfo implements the FileInfo interface for files within a GRF.
type ArchivedFileInfo struct {
	h       *FileHeader
	dirname string
	rwsize  int64
}

func (fi *ArchivedFileInfo) Name() string {
	if fi.h != nil {
		return path.Base(fi.h.Name)
	}
	return fi.dirname
}

func (fi *ArchivedFileInfo) Size() int64 {
	if fi.h != nil {
		if fi.rwsize == -1 {
			return int64(fi.h.Size)
		}
		return fi.rwsize
	}
	return 0
}

func (fi *ArchivedFileInfo) CompressedSize() int64 {
	if fi.h != nil {
		return int64(fi.h.CompressedSize)
	}
	return 0
}

func (fi *ArchivedFileInfo) Mode() fs.FileMode {
	if fi.dirname != "" {
		return fs.ModeDir | 0755
	}
	var perm fs.FileMode = 0644
	if fi.h.Linkname != "" {
		perm |= fs.ModeSymlink
	}
	return perm
}

func (fi *ArchivedFileInfo) ModTime() time.Time {
	return grffsCommonModTime
}

func (fi *ArchivedFileInfo) IsDir() bool {
	return fi.dirname != ""
}

func (fi *ArchivedFileInfo) Sys() any {
	return nil
}

// ArchivedFile implements various file interfaces for files within a GRF.
//
// Namely fs.ReadDirFile and http.File are supported. Files opened for writing
// use an in-memory buffer while the file is open and write back changes when
// closing; opening the same file twice this way will result in two independent
// snapshots.
type ArchivedFile struct {
	a       *Archive
	h       *FileHeader
	raw     bool
	dirname string
	mu      sync.Mutex

	lr *io.LimitedReader // limited reader
	zr io.ReadCloser     // zlib reader
	ch hash.Hash         // content hasher
	sh hash.Hash         // stream hasher
	tr io.Reader         // tee reader
	rp int               // read position

	rw  []byte // read-write buffer
	rwp int    // read-write position mode
	rwc int    // compression setting for read-write mode

	entries []fs.DirEntry
}

func (f *ArchivedFile) doReset() error {
	if f.h == nil {
		return nil
	}

	err := f.doClose()
	if err != nil {
		return err
	}

	f.a.m.RLock()
	defer f.a.m.RUnlock()

	f.rp = 0
	f.a.rfile.Seek(int64(f.h.Offset)+LogicalStartOffset, io.SeekStart)
	var lr io.Reader
	if f.h.Flags&(MixcryptFlag|DESFlag) != 0 && !f.raw {
		lr = io.LimitReader(f.a.rfile, int64(f.h.AlignedLength))
		f.lr = lr.(*io.LimitedReader)
		lr = newGRFCryptReader(lr, f.h.Flags&MixcryptFlag != 0, f.h.CompressedSize)
	} else {
		lr = io.LimitReader(f.a.rfile, int64(f.h.CompressedSize))
		f.lr = lr.(*io.LimitedReader)
	}
	f.tr = lr
	f.a.lf = f

	if f.h.Flags&FileFlag != 0 {
		if f.h.StreamHash.IsZero() {
			f.sh = blake3.New(32, nil)
			lr = io.TeeReader(lr, f.sh)
		}
		if !f.raw {
			var err error
			f.zr, err = newDecodingReader(lr, f.h, f.a.grfEditorKey)
			if err != nil {
				return err
			}
			if f.h.Hash.IsZero() {
				f.ch = blake3.New(32, nil)
				f.tr = io.TeeReader(f.zr, f.ch)
			} else {
				f.tr = f.zr
			}
		}
	} else if f.h.Hash.IsZero() || f.h.StreamHash.IsZero() {
		f.ch = blake3.New(32, nil)
		if f.h.StreamHash.IsZero() {
			f.sh = f.ch
		}
		f.tr = io.TeeReader(lr, f.ch)
	}

	return nil
}

// Reset will rewind the file to the start.
//
// Effectively closes and opens the file again; for read-write files simply
// sets the current position back to zero.
func (f *ArchivedFile) Reset() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.rw != nil {
		f.rwp = 0
		return nil
	}

	return f.doReset()
}

func (f *ArchivedFile) Stat() (fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	fi := &ArchivedFileInfo{h: f.h, dirname: f.dirname, rwsize: -1}
	if f.rw != nil {
		fi.rwsize = int64(len(f.rw))
	}
	return fi, nil
}

func (f *ArchivedFile) switchContextsIfNeeded() error {
	if f.a.lf != f {
		_, err := f.a.rfile.Seek(int64(f.h.Offset+f.h.CompressedSize)-f.lr.N+LogicalStartOffset, io.SeekStart)
		if err != nil {
			return err
		}
		f.a.lf = f
	}
	return nil
}

func (f *ArchivedFile) updateHashesIfNeeded() {
	if f.ch != nil {
		f.h.Hash.SetWithHasher(f.ch)
		f.ch = nil
	}
	if f.sh != nil {
		f.h.StreamHash.SetWithHasher(f.sh)
		f.sh = nil
	}
}

func (f *ArchivedFile) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.rw != nil {
		if f.rwp >= len(f.rw) {
			return 0, io.EOF
		}
		n := copy(p, f.rw[f.rwp:])
		f.rwp += n
		return n, nil
	}

	if f.lr == nil {
		return 0, io.EOF
	}

	f.a.m.Lock()
	defer f.a.m.Unlock()

	err := f.switchContextsIfNeeded()
	if err != nil {
		return 0, err
	}

	n, err := f.tr.Read(p)
	f.rp += n
	if err == io.EOF {
		f.updateHashesIfNeeded()
	}

	return n, err
}

func (f *ArchivedFile) Write(p []byte) (n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.rw == nil {
		return 0, fs.ErrInvalid
	}

	l := len(p)
	if f.rwp < len(f.rw) {
		n := copy(f.rw[f.rwp:], p)
		f.rwp += n
		p = p[n:]
	} else if f.rwp > len(f.rw) {
		if f.rwp <= cap(f.rw) {
			oldLen := len(f.rw)
			f.rw = f.rw[:f.rwp]
			hole := f.rw[oldLen:]
			for i := range hole {
				hole[i] = 0
			}
		} else {
			d := make([]byte, f.rwp, f.rwp+len(p))
			copy(d, f.rw)
			f.rw = d
		}
	}

	if len(p) > 0 {
		f.rw = append(f.rw, p...)
		f.rwp = len(f.rw)
	}

	return l, nil
}

func (f *ArchivedFile) doSeek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		if offset < 0 {
			return 0, fs.ErrInvalid
		}
		ioffset := int(offset)
		if ioffset == f.rp {
			return offset, nil
		}
		if ioffset > f.rp {
			return f.doSeek(offset-int64(f.rp), io.SeekCurrent)
		}
		if err := f.doReset(); err != nil {
			return 0, err
		}
		return f.doSeek(offset, io.SeekCurrent)
	case io.SeekCurrent:
		if offset == 0 {
			return int64(f.rp), nil
		}
		if offset < 0 {
			return f.doSeek(int64(f.rp)+offset, io.SeekStart)
		}

		if f.lr == nil {
			return 0, io.EOF
		}

		f.a.m.Lock()
		defer f.a.m.Unlock()

		err := f.switchContextsIfNeeded()
		if err != nil {
			return 0, err
		}

		n, err := io.CopyN(io.Discard, f.tr, offset)
		f.rp += int(n)
		if err == io.EOF {
			f.updateHashesIfNeeded()
		}

		return int64(f.rp), err
	case io.SeekEnd:
		if offset > 0 {
			return 0, fs.ErrInvalid
		}
		if offset == 0 {
			if err := f.doClose(); err != nil {
				return 0, err
			}
			f.rp = int(f.h.Size)
			return int64(f.rp), nil
		}
		return f.doSeek(int64(f.h.Size)+offset, io.SeekStart)
	default:
		return 0, fs.ErrInvalid
	}
}

func (f *ArchivedFile) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.rw != nil {
		npos := f.rwp
		switch whence {
		case io.SeekStart:
			npos = int(offset)
		case io.SeekCurrent:
			npos += int(offset)
		case io.SeekEnd:
			npos = len(f.rw) + int(offset)
		default:
			npos = -1
		}
		if npos < 0 {
			return 0, fs.ErrInvalid
		}
		f.rwp = npos
		return int64(f.rwp), nil
	}

	if f.h == nil {
		return 0, fs.ErrInvalid
	}

	return f.doSeek(offset, whence)
}

func (f *ArchivedFile) doClose() error {
	if f.lr == nil {
		return nil
	}

	f.lr = nil
	f.ch = nil
	f.sh = nil
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

func (f *ArchivedFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.rw != nil {
		h, err := f.a.PutBytes(f.h.Name, f.rw, f.rwc)
		if err != nil {
			f.h = h
		}
		return err
	}

	return f.doClose()
}

func (f *ArchivedFile) ReadDir(n int) ([]fs.DirEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

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

func (f *ArchivedFile) Readdir(count int) ([]fs.FileInfo, error) {
	entries, err := f.ReadDir(count)
	if err != nil && err != io.EOF {
		return nil, err
	}
	infos := make([]fs.FileInfo, len(entries))
	for i, entry := range entries {
		infos[i], _ = entry.Info()
	}
	return infos, nil
}

func (a *Archive) Mkdir(name string) error {
	a.m.Lock()
	defer a.m.Unlock()

	if (a.entriesByName != nil && a.entriesByName[name] != nil) || (a.folders != nil && a.folders[name] != nil) {
		return fs.ErrExist
	}

	a.insertFolderEntry(name)
	a.folders[name] = make(map[string]bool)
	return nil
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
		if name == "." {
			return nil, nil
		}
		return nil, fs.ErrNotExist
	}
	var entries []fs.DirEntry
	hasEntriesByName := a.entriesByName != nil
	for n := range f {
		nn := path.Join(name, n)
		ff := a.folders[nn]
		if ff != nil {
			entries = append(entries, fs.FileInfoToDirEntry(&ArchivedFileInfo{dirname: n, rwsize: -1}))
		} else if hasEntriesByName {
			hh := a.entriesByName[nn]
			if hh != nil && !hh.Deleted {
				entries = append(entries, fs.FileInfoToDirEntry(&ArchivedFileInfo{h: hh, rwsize: -1}))
			}
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func (a *Archive) doOpen(name string, raw bool) (*ArchivedFile, error) {
	a.m.RLock()

	var h *FileHeader
	if a.entriesByName != nil {
		h = a.entriesByName[name]
		if h != nil && h.Deleted {
			h = nil
		}
	}

	if h == nil || a.AllowHybridFolderEntries {
		// Maybe a folder
		entries, err := a.doReadDir(name)
		if err == nil {
			f := &ArchivedFile{dirname: path.Base(name), entries: entries}
			a.m.RUnlock()
			if h != nil {
				// Hybrid
				f.a = a
				f.h = h
				f.raw = raw
				return f, f.Reset()
			}
			return f, nil
		} else if h == nil {
			a.m.RUnlock()
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
	}

	f := &ArchivedFile{a: a, h: h, raw: raw}
	a.m.RUnlock()

	return f, f.Reset()
}

// Open opens the named file inside the GRF.
//
// This implements the FS interface. Opening more than one file can be
// detrimental to the overall speed since a seek has to be performed for each
// context switch. Also updates the file's header to include a hash at the end.
func (a *Archive) Open(name string) (fs.File, error) {
	return a.doOpen(name, false)
}

// OpenRaw opens the named file inside the GRF without decompressing or
// decrypting.
//
// Opening more than one file can be detrimental to the overall speed since a
// seek has to be performed for each context switch. This will not update the
// file's hash, but possibly its stream hash.
func (a *Archive) OpenRaw(name string) (fs.File, error) {
	return a.doOpen(name, true)
}

// OpenFile opens the named file inside the GRF.
//
// This allows read-write mode. In read-only the compress parameter is ignored.
func (a *Archive) OpenFile(name string, flag, compress int) (*ArchivedFile, error) {
	isRW := flag&(os.O_APPEND|os.O_RDWR|os.O_WRONLY) != 0
	if a.wfile == nil {
		return nil, fs.ErrPermission
	}

	f, err := a.doOpen(name, false)
	if isRW {
		if pe, ok := err.(*fs.PathError); ok && pe.Err == fs.ErrNotExist {
			if flag&os.O_CREATE == 0 {
				return nil, err
			}
			var hflags uint8 = 0
			if compress != 0 {
				hflags |= FileFlag
			}
			f = &ArchivedFile{a: a, h: &FileHeader{Name: name, Flags: hflags}, rw: make([]byte, 0), rwc: compress}
			err = nil
		} else if err == nil && f.h != nil {
			if flag&os.O_TRUNC == 0 {
				rw := make([]byte, f.h.Size)
				_, err = f.Read(rw)
				f.Close()
				if err == nil {
					f.rw = rw
					f.rwc = compress
					if flag&os.O_APPEND == 0 {
						f.rwp = len(rw)
					}
				}
			} else {
				f.Close()
				f.rw = make([]byte, 0)
				f.rwc = compress
			}
		}
	}
	return f, err
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
			return &ArchivedFileInfo{dirname: ".", rwsize: -1}, nil
		}
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	h := a.entriesByName[name]
	if h != nil && h.Deleted {
		h = nil
	}

	if h == nil || a.AllowHybridFolderEntries {
		// Maybe a folder
		f := a.folders[name]
		if f != nil {
			fi := &ArchivedFileInfo{dirname: path.Base(name), rwsize: -1}
			if h != nil {
				// Hybrid
				fi.h = h
			}
			return fi, nil
		} else if h == nil {
			if name == "." {
				return &ArchivedFileInfo{dirname: ".", rwsize: -1}, nil
			}
			return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
		}
	}

	return &ArchivedFileInfo{h: h, rwsize: -1}, nil
}

// Glob returns the names of all files matching pattern.
//
// This implements the GlobFS interface.
func (a *Archive) Glob(pattern string) ([]string, error) {
	a.m.RLock()
	defer a.m.RUnlock()

	var result []string
	resultSet := make(map[string]bool)
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
				resultSet[n] = true
			}
		}
	}
	if a.entriesByName != nil {
		for n := range a.entriesByName {
			m, err := path.Match(pattern, n)
			if err != nil {
				return nil, err
			}
			if m && !resultSet[n] {
				result = append(result, n)
			}
		}
	}
	return result, nil
}
