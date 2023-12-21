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
	"sort"
	"strings"

	"lukechampine.com/blake3"
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

func (h *FileHeader) seekAndHash(file io.ReadSeekCloser, hasher, streamHasher hash.Hash) error {
	if hasher == nil && streamHasher == nil {
		return nil
	}
	_, err := file.Seek(int64(h.Offset)+46, io.SeekStart)
	if err != nil {
		return err
	}
	lr := io.LimitReader(file, int64(h.CompressedSize))
	if hasher == nil {
		_, err = io.Copy(streamHasher, lr)
		if err != nil {
			h.Hash.SetZero()
			if h.Flags&FileFlag == 0 {
				h.StreamHash.SetZero()
			}
		} else {
			h.StreamHash.SetWithHasher(streamHasher)
			if h.Flags&FileFlag == 0 {
				h.Hash.SetWithHasher(streamHasher)
			}
		}
		streamHasher.Reset()
		return err
	}

	var zr io.ReadCloser
	if h.Flags&FileFlag != 0 {
		if streamHasher != nil {
			lr = io.TeeReader(lr, streamHasher)
		}
		zr, err = zlib.NewReader(lr)
		if err != nil {
			if streamHasher != nil {
				streamHasher.Reset()
			}
			return err
		}
	} else {
		zr = io.NopCloser(lr)
	}
	_, err = io.Copy(hasher, zr)
	zr.Close()
	if err != nil {
		h.Hash.SetZero()
		if h.Flags&FileFlag == 0 || streamHasher != nil {
			h.StreamHash.SetZero()
		}
	} else {
		h.Hash.SetWithHasher(hasher)
		if h.Flags&FileFlag != 0 {
			if streamHasher != nil {
				h.StreamHash.SetWithHasher(streamHasher)
			}
		} else {
			h.StreamHash = h.Hash
		}
	}
	hasher.Reset()
	if h.Flags&FileFlag != 0 && streamHasher != nil {
		streamHasher.Reset()
	}
	return err
}

func (h *FileHeader) seekAndHashIfNeeded(file io.ReadSeekCloser, hasher, streamHasher hash.Hash) error {
	if h.Hash.IsZero() {
		if h.StreamHash.IsZero() {
			return h.seekAndHash(file, hasher, streamHasher)
		} else {
			return h.seekAndHash(file, hasher, nil)
		}
	} else if h.StreamHash.IsZero() {
		return h.seekAndHash(file, nil, streamHasher)
	}
	return nil
}

// Hash returns the BLAKE3-256 hash of a file's content within the GRF.
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

	hasher := blake3.New(32, nil)
	streamHasher := blake3.New(32, nil)
	err := h.seekAndHash(a.file, hasher, streamHasher)
	if err != nil {
		return FileHeaderHash{}, err
	}

	return h.Hash, nil
}

// StreamHash returns the BLAKE3-256 hash of a file's stream (i.e. raw) data
// within the GRF.
//
// If the hash already has been computed, this will return the cached value.
func (a *Archive) StreamHash(name string) (FileHeaderHash, error) {
	a.m.Lock()
	defer a.m.Unlock()

	if a.entriesByName == nil {
		return FileHeaderHash{}, fs.ErrNotExist
	}
	h := a.entriesByName[name]
	if h == nil || h.Deleted {
		return FileHeaderHash{}, fs.ErrNotExist
	}

	if !h.StreamHash.IsZero() {
		return h.StreamHash, nil
	}

	hasher := blake3.New(32, nil)
	streamHasher := blake3.New(32, nil)
	err := h.seekAndHash(a.file, hasher, streamHasher)
	if err != nil {
		return FileHeaderHash{}, err
	}

	return h.StreamHash, nil
}

// HashAll will return a mapping of all file names to their hashes.
//
// If the first parameter is false, returns content hashes. If true, returns
// stream hashes. Stream hashes will be faster to calculate since no
// decompression has to take place.
//
// The operation can be cancelled using the context; progress in bytes can be
// tracked through the channel. If there is no severe error but one or more
// files could not be read properly, the function will return both a result and
// an ArchiveIntegrityError.
func (a *Archive) HashAll(streamHash bool, ctx context.Context, progress chan<- int64) (map[string]FileHeaderHash, error) {
	var hasher hash.Hash
	if !streamHash {
		hasher = blake3.New(32, nil)
	}
	streamHasher := blake3.New(32, nil)

	a.m.Lock()
	defer a.m.Unlock()

	consecutiveEntries := a.entriesInSequence
	sorter := func(i, j int) bool {
		if consecutiveEntries[i].Linkname != "" || consecutiveEntries[j].Linkname != "" {
			return i < j
		}
		return consecutiveEntries[i].Offset < consecutiveEntries[j].Offset
	}
	if !sort.SliceIsSorted(consecutiveEntries, sorter) {
		consecutiveEntries = make([]*FileHeader, len(a.entriesInSequence))
		copy(consecutiveEntries, a.entriesInSequence)
		sort.Slice(consecutiveEntries, sorter)
	}

	var totalProcessed int64
	all := make(map[string]FileHeaderHash)
	byOffset := make(map[uint32]*FileHeader)
	fileErrors := make(map[string]error)
	for _, h := range consecutiveEntries {
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
			if h.StreamHash != e.StreamHash {
				h.StreamHash = e.StreamHash
			}
			if !streamHash {
				all[h.Name] = h.Hash
			} else {
				all[h.Name] = h.StreamHash
			}
			continue
		} else if h.Linkname != "" {
			h.Linkname = ""
		}

		err := h.seekAndHashIfNeeded(a.file, hasher, streamHasher)
		if err != nil {
			fileErrors[h.Name] = err
		} else {
			if !streamHash {
				all[h.Name] = h.Hash
			} else {
				all[h.Name] = h.StreamHash
			}
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
// The first parameter controls if the given hashes are content hashes or stream
// hashes.
//
// Missing files or extra files are treated as if the hash value was incorrect.
// The operation can be cancelled using the context; progress in bytes can be
// tracked through the channel. If there is no severe error but one or more
// files could not be read properly, the function will return both a result and
// an ArchiveIntegrityError.
func (a *Archive) VerifyAll(streamHash bool, hashMap map[string]FileHeaderHash, ctx context.Context, progress chan<- int64) ([]string, error) {
	var hasher hash.Hash
	if !streamHash {
		hasher = blake3.New(32, nil)
	}
	streamHasher := blake3.New(32, nil)

	a.m.Lock()
	defer a.m.Unlock()

	consecutiveEntries := a.entriesInSequence
	sorter := func(i, j int) bool {
		if consecutiveEntries[i].Linkname != "" || consecutiveEntries[j].Linkname != "" {
			return i < j
		}
		return consecutiveEntries[i].Offset < consecutiveEntries[j].Offset
	}
	if !sort.SliceIsSorted(consecutiveEntries, sorter) {
		consecutiveEntries = make([]*FileHeader, len(a.entriesInSequence))
		copy(consecutiveEntries, a.entriesInSequence)
		sort.Slice(consecutiveEntries, sorter)
	}

	var totalProcessed int64
	var fileNames []string
	byOffset := make(map[uint32]*FileHeader)
	fileErrors := make(map[string]error)
	for _, h := range consecutiveEntries {
		if !h.Deleted {
			e := byOffset[h.Offset]
			if e != nil {
				if h.Linkname != e.Name {
					h.Linkname = e.Name
				}
				if h.Hash != e.Hash {
					h.Hash = e.Hash
				}
				if h.StreamHash != e.StreamHash {
					h.StreamHash = e.StreamHash
				}
			} else if h.Linkname != "" {
				h.Linkname = ""
			}

			x := hashMap[h.Name]
			if !x.IsZero() {
				err := h.seekAndHashIfNeeded(a.file, hasher, streamHasher)
				if err != nil {
					fileErrors[h.Name] = err
				}
				if (streamHash && x != h.StreamHash) || (!streamHash && x != h.Hash) {
					fileNames = append(fileNames, h.Name)
				}
				byOffset[h.Offset] = h
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
