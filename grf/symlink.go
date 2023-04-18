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
	"os"
	"path/filepath"
	"strings"
)

// SymlinkFile simplifies handling symbolic links outside of GRF files.
//
// A special file "SYMLINKS" is written while extracting or read while authoring
// GRFs to handle repeating content.
type SymlinksFile struct {
	// Name of the file on disk (absolute path).
	Name string

	linkMap map[string]string

	append bool
	file   *os.File
}

// OpenSymlinks file at the given folder path (if it exists).
//
// The folder at the given path must exist. The symlinks file is put into append
// mode until you do a destructive operation.
func OpenSymlinks(path string) (*SymlinksFile, error) {
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
	fi, err = os.Stat(path)
	if err == nil && fi.IsDir() {
		return nil, errors.New("the symlinks file cannot be a directory")
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

	s := &SymlinksFile{Name: path, linkMap: make(map[string]string), append: true}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		spl := strings.SplitN(scanner.Text(), " -> ", 2)
		if len(spl) == 2 {
			s.linkMap[spl[0]] = spl[1]
		}
	}
	r.Close()

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
		for k, v := range s.linkMap {
			r[k] = v
		}
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
}

// Write the symlink file.
//
// If the file is in append mode, this simply closes the file if it is open.
// In regular mode, this writes the entire file if there are symlinks or deletes
// the file if there are no symlinks.
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
		return f.Close()
	}

	return nil
}
