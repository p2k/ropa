//
//  verify.go
//  ropa
//
//  Created by p2k on 23.03.2023
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
	"compress/zlib"
	"context"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// ArchiveIntegrityError is returned on verification if there are problems with
// individual files.
type ArchiveIntegrityError struct {
	FileErrors map[string]error
}

func (e *ArchiveIntegrityError) Error() string {
	keys := make([]string, 0, len(e.FileErrors))
	for k := range e.FileErrors {
		keys = append(keys, k)
	}
	return fmt.Sprintf("Found files with errors: %s", strings.Join(keys, ", "))
}

func (h *FileHeader) seekAndHash(file io.ReadSeekCloser, hasher hash.Hash) error {
	_, err := file.Seek(int64(h.Offset)+46, io.SeekStart)
	if err != nil {
		return err
	}
	lr := io.LimitReader(file, int64(h.CompressedSize))
	var zr io.ReadCloser
	if h.Flags&FileFlag != 0 {
		zr, err = zlib.NewReader(lr)
		if err != nil {
			return err
		}
	} else {
		zr = io.NopCloser(lr)
	}
	_, err = io.Copy(hasher, zr)
	zr.Close()
	copy(h.Hash[:], hasher.Sum(nil))
	hasher.Reset()
	if err != nil {
		h.Hash.SetZero()
	}
	return err
}

func (h *FileHeader) seekAndHashIfNeeded(file io.ReadSeekCloser, hasher hash.Hash) error {
	if h.Hash.IsZero() {
		return h.seekAndHash(file, hasher)
	}
	return nil
}

// Hash returns the blake2b hash of a file within the GRF.
//
// If the hash already has been computed, this will return the cached value.
func (a *Archive) Hash(name string) (FileHeaderHash, error) {
	a.m.Lock()
	defer a.m.Unlock()

	if a.entriesByName == nil {
		return FileHeaderHash{}, fs.ErrNotExist
	}
	h := a.entriesByName[name]
	if h == nil || h.Deleted {
		return FileHeaderHash{}, fs.ErrNotExist
	}

	if !h.Hash.IsZero() {
		return h.Hash, nil
	}

	hasher, err := blake2b.New256(nil)
	if err != nil {
		return FileHeaderHash{}, err
	}

	err = h.seekAndHash(a.file, hasher)
	if err != nil {
		return FileHeaderHash{}, err
	}

	return h.Hash, nil
}

// HashAll will return a mapping of all file names to their hashes.
//
// The operation can be cancelled using the context; progress in bytes can be
// tracked through the channel. If there is no severe error but one or more
// files could not be read properly, the function will return both a result and
// an ArchiveIntegrityError.
func (a *Archive) HashAll(ctx context.Context, progress chan<- int64) (map[string]FileHeaderHash, error) {
	hasher, err := blake2b.New256(nil)
	if err != nil {
		return nil, err
	}

	a.m.Lock()
	defer a.m.Unlock()

	var totalProcessed int64
	all := make(map[string]FileHeaderHash)
	byOffset := make(map[uint32]*FileHeader)
	fileErrors := make(map[string]error)
	for _, h := range a.entriesInSequence {
		if h.Deleted {
			continue
		}

		e := byOffset[h.Offset]
		if e != nil {
			if h.Linkname != e.Name {
				h.Linkname = e.Name
			}
			if h.Hash != e.Hash {
				h.Hash = e.Hash
			}
			all[h.Name] = h.Hash
			continue
		} else if h.Linkname != "" {
			h.Linkname = ""
		}

		err = h.seekAndHashIfNeeded(a.file, hasher)
		if err != nil {
			fileErrors[h.Name] = err
		} else {
			all[h.Name] = h.Hash
			byOffset[h.Offset] = h
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			totalProcessed += int64(h.Size)
			if progress != nil {
				progress <- totalProcessed
			}
		}
	}

	if len(fileErrors) > 0 {
		return all, &ArchiveIntegrityError{FileErrors: fileErrors}
	}
	return all, nil
}

// VerifyAll will return a set of file names for which the hash is incorrect.
//
// Missing files or extra files are treated as if the hash value was incorrect.
// The operation can be cancelled using the context; progress in bytes can be
// tracked through the channel. If there is no severe error but one or more
// files could not be read properly, the function will return both a result and
// an ArchiveIntegrityError.
func (a *Archive) VerifyAll(ctx context.Context, progress chan<- int64, hashMap map[string]FileHeaderHash) ([]string, error) {
	hasher, err := blake2b.New256(nil)
	if err != nil {
		return nil, err
	}

	a.m.Lock()
	defer a.m.Unlock()

	var totalProcessed int64
	var fileNames []string
	byOffset := make(map[uint32]*FileHeader)
	fileErrors := make(map[string]error)
	for _, h := range a.entriesInSequence {
		if !h.Deleted {
			e := byOffset[h.Offset]
			if e != nil {
				if h.Linkname != e.Name {
					h.Linkname = e.Name
				}
				if h.Hash != e.Hash {
					h.Hash = e.Hash
				}
				continue
			} else if h.Linkname != "" {
				h.Linkname = ""
			}

			x := hashMap[h.Name]
			if !x.IsZero() {
				err = h.seekAndHashIfNeeded(a.file, hasher)
				if err != nil {
					fileErrors[h.Name] = err
				}
				if x != h.Hash {
					fileNames = append(fileNames, h.Name)
				}
			} else {
				fileNames = append(fileNames, h.Name)
			}

			select {
			case <-ctx.Done():
				return fileNames, ctx.Err()
			default:
				totalProcessed += int64(h.Size)
				if progress != nil {
					progress <- totalProcessed
				}
			}
		} else {
			fileNames = append(fileNames, h.Name)
		}
	}

	for fileName := range hashMap {
		if a.entriesByName == nil || a.entriesByName[fileName] == nil {
			fileNames = append(fileNames, fileName)
		}
	}

	if len(fileErrors) > 0 {
		return fileNames, &ArchiveIntegrityError{FileErrors: fileErrors}
	}
	return fileNames, nil
}
