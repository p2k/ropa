//
//  symlink.go
//  ropa
//
//  Created by p2k on 17.04.2023
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
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// SymlinksFile simplifies handling symbolic links outside of GRF files.
//
// A special file "SYMLINKS" is written while extracting or read while authoring
// GRFs to handle repeating content.
type SymlinksFile struct {
	// Name of the file on disk (absolute path).
	Name string

	linkMap map[string]string
	folders map[string]map[string]bool

	append bool
	file   *os.File
}

func (s *SymlinksFile) insertFolderEntry(name string) {
	if s.folders == nil {
		s.folders = make(map[string]map[string]bool)
	}
	var entry string
	for name != "." {
		name, entry = path.Split(name)
		if name != "" {
			name = name[:len(name)-1]
		} else {
			name = "."
		}
		folder := s.folders[name]
		if folder == nil {
			folder = make(map[string]bool)
			s.folders[name] = folder
		}
		folder[entry] = true
	}
}

func (s *SymlinksFile) deleteFolderEntry(name string) {
	if s.folders == nil {
		return
	}
	var entry string
	for name != "." {
		name, entry = path.Split(name)
		if name != "" {
			name = name[:len(name)-1]
		} else {
			name = "."
		}
		folder := s.folders[name]
		if folder != nil {
			delete(folder, entry)
			if len(folder) > 0 {
				return
			}
			delete(s.folders, name)
		}
	}
}

// OpenSymlinks file at the given path or folder path (if it exists).
//
// The file or folder at the given path must exist. The symlinks file is put
// into append mode until you do a destructive operation.
func OpenSymlinks(path string) (*SymlinksFile, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		path = filepath.Join(path, "SYMLINKS")
		fi, err = os.Stat(path)
		if err == nil && fi.IsDir() {
			return nil, errors.New("the symlinks file cannot be a directory")
		}
	}
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		return &SymlinksFile{Name: path, append: true}, nil
	}

	r, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	s := &SymlinksFile{Name: path, linkMap: make(map[string]string), folders: make(map[string]map[string]bool), append: true}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		spl := strings.SplitN(scanner.Text(), " -> ", 2)
		if len(spl) == 2 {
			s.linkMap[spl[0]] = spl[1]
			s.insertFolderEntry(spl[0])
		}
	}
	r.Close()
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return s, nil
}

// NewSymlinks creates a new symlinks file for the given folder path on demand.
//
// This will put the returned struct into append mode until you do a destructive
// operation. If a symlink file already exists, this will delete it first. In
// all cases it is created as soon as the first symlink is added.
func NewSymlinks(path string) (*SymlinksFile, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, errors.New("path must point to a directory")
	}
	path = filepath.Join(path, "SYMLINKS")
	_, err = os.Stat(path)
	if err == nil {
		err = os.Remove(path)
		if err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	return &SymlinksFile{Name: path, append: true}, nil
}

// Get the target path of a symlink.
func (s *SymlinksFile) Get(name string) string {
	if s.linkMap != nil {
		return s.linkMap[name]
	}
	return ""
}

// GetAll symlinks.
func (s *SymlinksFile) GetAll() map[string]string {
	r := make(map[string]string)
	if s.linkMap != nil {
		maps.Copy(r, s.linkMap)
	}
	return r
}

// Add a new symlink to the symlinks file.
//
// If the symlink file is in append mode, this will create the file if it does
// not exist and append the symlink right away. If a symlink of the same name
// but not the same target already exists, it will end append mode and go into
// regular mode. If an error is encountered while writing, also goes to regular
// mode.
func (s *SymlinksFile) Add(name, target string) {
	if s.linkMap == nil {
		s.linkMap = make(map[string]string)
	}

	e := s.linkMap[name]
	if e == target {
		return
	}
	if e != "" {
		s.append = false
		if s.file != nil {
			s.file.Close()
			s.file = nil
		}
	} else if s.append && s.file == nil {
		f, err := os.OpenFile(s.Name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			s.file = f
		} else {
			s.append = false
		}
	}

	s.linkMap[name] = target
	s.insertFolderEntry(name)

	if s.file != nil {
		s.file.WriteString(fmt.Sprintln(name, "->", target))
	}
}

// Remove a symlink from the symlink file.
//
// If it exists and the symlink file is in append mode, will switch to regular
// mode.
func (s *SymlinksFile) Remove(name string) {
	s.append = false
	if s.file != nil {
		s.file.Close()
		s.file = nil
	}

	delete(s.linkMap, name)
	s.deleteFolderEntry(name)
}

// Write the symlink file.
//
// If the file is in append mode, this simply closes the file if it is open.
// In regular mode, this writes the entire file if there are symlinks or deletes
// the file if there are no symlinks. After this, append mode is enabled again.
func (s *SymlinksFile) Write() error {
	if s.append {
		if s.file != nil {
			// Under this condition, we must have written something
			err := s.file.Close()
			s.file = nil
			return err
		}
	}

	if len(s.linkMap) == 0 {
		_, err := os.Stat(s.Name)
		if err == nil {
			err = os.Remove(s.Name)
			if err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	} else if !s.append {
		f, err := os.Create(s.Name)
		if err != nil {
			return err
		}
		for name, target := range s.linkMap {
			f.WriteString(fmt.Sprintln(name, "->", target))
		}
		err = f.Close()
		if err != nil {
			return err
		}
	}

	s.append = true

	return nil
}

// WriteSorted writes the symlink file sorted by paths.
//
// This should be run separately since it costs some time depending on the
// number of entries and forces a rewrite of the entire file.
func (s *SymlinksFile) WriteSorted() error {
	s.append = false
	if s.file != nil {
		s.file.Close()
		s.file = nil
	}

	if len(s.linkMap) == 0 {
		_, err := os.Stat(s.Name)
		if err == nil {
			err = os.Remove(s.Name)
			if err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	} else if !s.append {
		f, err := os.Create(s.Name)
		if err != nil {
			return err
		}
		var paths []string
		for path := range s.linkMap {
			paths = append(paths, path)
		}
		slices.SortStableFunc(paths, func(a, b string) int {
			if a == b {
				return 0
			}
			aSpl := strings.Split(a, "/")
			aSplN := len(aSpl)
			bSpl := strings.Split(b, "/")
			bSplN := len(bSpl)
			i := 0
			for i < aSplN && i < bSplN && aSpl[i] == bSpl[i] {
				i++
			}
			if aSplN-i == 1 && bSplN-i > 1 {
				return -1
			}
			if aSplN-i > 1 && bSplN-i == 1 {
				return 1
			}
			return strings.Compare(aSpl[i], bSpl[i])
		})
		for _, path := range paths {
			f.WriteString(fmt.Sprintln(path, "->", s.linkMap[path]))
		}
		err = f.Close()
		if err != nil {
			return err
		}
	}

	s.append = true

	return nil
}

// Glob returns the names of all files matching pattern.
func (s *SymlinksFile) Glob(pattern string) ([]string, error) {
	var result []string
	if s.folders != nil {
		for n := range s.folders {
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
	if s.linkMap != nil {
		for n := range s.linkMap {
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

type symlinkDirEntry struct {
	name string
	dir  bool

	children []fs.DirEntry
}

func (d *symlinkDirEntry) Name() string {
	return d.name
}

func (d *symlinkDirEntry) IsDir() bool {
	return d.dir
}

func (d *symlinkDirEntry) Mode() fs.FileMode {
	if d.dir {
		return fs.ModeDir | 0755
	}
	return fs.ModeSymlink | 0644
}

func (d *symlinkDirEntry) Type() fs.FileMode {
	if d.dir {
		return fs.ModeDir
	}
	return 0
}

func (d *symlinkDirEntry) Size() int64 {
	return 0
}

func (d *symlinkDirEntry) ModTime() time.Time {
	return time.Time{}
}

func (d *symlinkDirEntry) Sys() any {
	return nil
}

func (d *symlinkDirEntry) Info() (fs.FileInfo, error) {
	return d, nil
}

func (d *symlinkDirEntry) Stat() (fs.FileInfo, error) {
	return d, nil
}

func (d *symlinkDirEntry) Read([]byte) (int, error) {
	return 0, fs.ErrInvalid
}

func (d *symlinkDirEntry) Close() error {
	return nil
}

func (d *symlinkDirEntry) ReadDir(n int) ([]fs.DirEntry, error) {
	if d.children == nil {
		return nil, io.EOF
	}
	l := len(d.children)
	if n <= 0 || n > l {
		n = l
	}
	result := d.children[:n]
	if n == l {
		d.children = nil
	} else {
		d.children = d.children[n:]
	}
	return result, nil
}

// Open implements the FS interface in a very limited way.
//
// This can be used to walk paths recursively. Only folders can be opened.
func (s *SymlinksFile) Open(name string) (fs.File, error) {
	if s.folders == nil {
		if name == "." {
			return &symlinkDirEntry{name: ".", dir: true}, nil
		}
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	f := s.folders[name]
	if f == nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	var entries []fs.DirEntry
	for n := range f {
		nn := path.Join(name, n)
		ff := s.folders[nn]
		if ff != nil {
			entries = append(entries, &symlinkDirEntry{name: n, dir: true})
		} else {
			if s.linkMap[nn] != "" {
				entries = append(entries, &symlinkDirEntry{name: n})
			}
		}
	}
	return &symlinkDirEntry{name: path.Base(name), children: entries, dir: true}, nil
}

// Stat implements the StatFS interface in a very limited way.
//
// This can be used to walk paths recursively.
func (s *SymlinksFile) Stat(name string) (fs.FileInfo, error) {
	if s.folders != nil {
		f := s.folders[name]
		if f != nil {
			return &symlinkDirEntry{name: path.Base(name), dir: true}, nil
		}
	} else if name == "." {
		return &symlinkDirEntry{name: ".", dir: true}, nil
	}
	if s.linkMap != nil {
		l := s.linkMap[name]
		if l != "" {
			return &symlinkDirEntry{name: path.Base(name)}, nil
		}
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}
