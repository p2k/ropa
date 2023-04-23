//
//  optimize.go
//  ropa
//
//  Created by p2k on 24.03.2023
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
	"context"
	"io"
	"sort"

	"golang.org/x/crypto/blake2b"
)

// Sort will put all file headers in alphabetical order.
//
// If the headers are already in order, does nothing.
func (a *Archive) Sort() {
	a.m.Lock()
	defer a.m.Unlock()

	sorter := func(i, j int) bool {
		return a.entriesInSequence[i].Name < a.entriesInSequence[j].Name
	}
	if !sort.SliceIsSorted(a.entriesInSequence, sorter) {
		sort.Slice(a.entriesInSequence, sorter)
		a.hasChanges = true
	}
}

// Dedup will deduplicate files in the archive by comparing their hash values
// and making them point to the same data.
//
// Parameters are optional and can be used to track progress in bytes or cancel
// the operation.
func (a *Archive) Dedup(progress chan<- int64, ctx context.Context) (bool, error) {
	hasher, err := blake2b.New256(nil)
	if err != nil {
		return false, err
	}

	if ctx == nil {
		ctx = context.Background()
	}

	a.m.Lock()
	defer a.m.Unlock()

	var totalProcessed int64
	found := false
	byOffset := make(map[uint32]*FileHeader)
	hashMap := make(map[FileHeaderHash]*FileHeader)
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
			continue
		} else if h.Linkname != "" {
			h.Linkname = ""
		}

		err = h.seekAndHashIfNeeded(a.file, hasher)
		if err != nil {
			return false, err
		}
		byOffset[h.Offset] = h

		select {
		case <-ctx.Done():
			return false, context.Canceled
		default:
			totalProcessed += int64(h.Size)
			if progress != nil {
				progress <- totalProcessed
			}
		}

		hh := hashMap[h.Hash]
		if hh != nil {
			if h.CompressedSize != hh.CompressedSize || h.AlignedLength != hh.AlignedLength ||
				h.Size != hh.Size || h.Flags != hh.Flags || h.Offset != hh.Offset {
				found = true
				a.hasChanges = true
				h.Linkname = hh.Name
				h.CompressedSize = hh.CompressedSize
				h.AlignedLength = hh.AlignedLength
				h.Size = hh.Size
				h.Flags = hh.Flags
				h.Offset = hh.Offset
			} else if h.Linkname != hh.Name {
				h.Linkname = hh.Name
			}
		} else {
			hashMap[h.Hash] = h
		}
	}

	return found, nil
}

func (a *Archive) copyChunks(dst int64, src int64, length int64, buffer []uint8, bufferLength int64) error {
	if buffer == nil {
		if length < bufferLength {
			bufferLength = length
		}
		buffer = make([]byte, bufferLength)
	}

	for length > 0 {
		if length < bufferLength {
			bufferLength = length
			buffer = buffer[:length]
		}

		_, err := a.file.ReadAt(buffer, src)
		if err != nil {
			return err
		}
		_, err = a.file.WriteAt(buffer, dst)
		if err != nil {
			return err
		}

		length -= bufferLength
		src += bufferLength
		dst += bufferLength
	}
	return nil
}

func (a *Archive) padChunks(length int64, buffer []uint8, bufferLength int64) error {
	if buffer == nil {
		if length < bufferLength {
			bufferLength = length
		}
		buffer = make([]byte, bufferLength)
	} else {
		if length < bufferLength {
			bufferLength = length
			buffer = buffer[:length]
		}
		for i := int64(0); i < bufferLength; i++ {
			buffer[i] = 0
		}
	}

	_, err := a.file.Seek(int64(a.insertOffset)+46, io.SeekStart)
	if err != nil {
		return err
	}

	for length > 0 {
		if length < bufferLength {
			bufferLength = length
			buffer = buffer[:length]
		}

		_, err = a.file.Write(buffer)
		if err != nil {
			return err
		}

		length -= bufferLength
	}

	return nil
}

// Defragment will rebuild the contents of the GRF file in sequence,
// consolidating unused space and zeroing deleted files.
//
// This happens in-place writing the file table after each move operation so
// it's considered to be safe. Parameters are optional and can be used to track
// progress in bytes or cancel the operation.
func (a *Archive) Defragment(progress chan<- int64, ctx context.Context) error {
	a.m.Lock()
	defer a.m.Unlock()

	// Take inventory
	byOffset := make(map[uint32]*FileHeader)     // For detecting links
	links := make(map[*FileHeader][]*FileHeader) // original -> links
	var currentSequence []*FileHeader            // For moving files
	for _, h := range a.entriesInSequence {
		if h.Deleted { // Skip/Destroy
			if h.CompressedSize != 0 || h.AlignedLength != 0 || h.Size != 0 || h.Offset != 0 {
				h.CompressedSize = 0
				h.AlignedLength = 0
				h.Size = 0
				h.Offset = 0
				a.hasChanges = true
			}
		} else {
			e := byOffset[h.Offset]
			if e == nil {
				if h.Linkname != "" {
					h.Linkname = ""
				}
				currentSequence = append(currentSequence, h)
				byOffset[h.Offset] = h
			} else {
				if h.Linkname != e.Name {
					h.Linkname = e.Name
				}
				links[e] = append(links[e], h)
			}
		}
	}

	sort.Slice(currentSequence, func(i, j int) bool { return currentSequence[i].Offset < currentSequence[j].Offset })

	ls := len(currentSequence)

	// Overlap check
	for i, h := range currentSequence {
		if i < ls-1 {
			if currentSequence[i+1].Offset < h.EndOffset() {
				return &ArchiveError{"detected overlapping files"}
			}
		}
	}

	if ls == 0 {
		// No files
		a.insertOffset = 0
		a.hasChanges = true
	} else if a.FileTableOffset < currentSequence[ls-1].EndOffset() {
		// File table not last
		a.hasChanges = true
	} else if a.insertOffset < a.FileTableOffset+8+a.FileTableCompressedSize+padding(a.FileTableCompressedSize) {
		// File table not aligned
		a.insertOffset = currentSequence[ls-1].EndOffset()
		a.hasChanges = true
	}

	// Write file table if there were changes (or other conditions force the write)
	if a.hasChanges {
		err := a.writeFileTableAndHeader(false)
		if err != nil {
			return err
		}
		if ls == 0 {
			// Nothing more to do
			return nil
		}
	}

	// The file table acts as its own file in last place
	ftHeader := FileHeader{
		Name:           ".filetable",
		CompressedSize: a.FileTableCompressedSize + 8,
		AlignedLength:  a.FileTableCompressedSize + 8 + padding(a.FileTableCompressedSize),
		Size:           a.FileTableSize,
		Flags:          FileFlag,
		Offset:         a.FileTableOffset,
	}
	currentSequence = append(currentSequence, &ftHeader)
	ls++

	if ctx == nil {
		ctx = context.Background()
	}

	// Early cancel
	select {
	case <-ctx.Done():
		return context.Canceled
	default:
	}

	// Allocate 32MiB buffer
	buffer := make([]uint8, 1<<25)
	bs := int64(len(buffer))
	bl := uint32(len(buffer))

	moveFile := func(hdi, hsi int, dst uint32) error {
		// Assumes there is enough space at the destination (incl. new alignment)
		h := currentSequence[hsi]
		ho := h.Offset
		err := a.copyChunks(int64(dst)+46, int64(ho)+46, int64(h.CompressedSize), buffer, bs)
		if err != nil {
			return err
		}

		// Update offsets
		h.Offset = dst
		h.AlignedLength = h.CompressedSize + padding(h.CompressedSize)
		for _, e := range links[h] {
			e.Offset = dst
			e.AlignedLength = h.AlignedLength
		}
		a.hasChanges = true
		if hsi < hdi {
			copy(currentSequence[hsi:hdi], currentSequence[hsi+1:hdi+1])
		} else if hsi > hdi {
			copy(currentSequence[hdi+1:hsi+1], currentSequence[hdi:hsi])
		}
		currentSequence[hdi] = h

		// Update insert offset if needed
		if dst+h.AlignedLength > a.insertOffset {
			a.insertOffset = dst + h.AlignedLength
		}

		return nil
	}

	writeFTH := func() error {
		err := a.writeFileTableAndHeader(false)
		if err != nil {
			return err
		}
		if currentSequence[ls-1] != &ftHeader {
			fhi := ls - 2
			for currentSequence[fhi] != &ftHeader {
				fhi--
			}
			copy(currentSequence[fhi:ls-1], currentSequence[fhi+1:ls])
			currentSequence[ls-1] = &ftHeader
		}
		ftHeader.CompressedSize = a.FileTableCompressedSize + 8
		ftHeader.AlignedLength = a.FileTableCompressedSize + 8 + padding(a.FileTableCompressedSize)
		ftHeader.Size = a.FileTableSize
		ftHeader.Offset = a.FileTableOffset
		return nil
	}

	var totalProcessed int64
	var j int    // Index into currentSequence
	var o uint32 // Current offset

	for _, h := range a.entriesInSequence {
		if h.Deleted || h.Linkname != "" {
			continue
		}

		select {
		case <-ctx.Done():
			return context.Canceled
		default:
		}

		// File is already where it belongs?
		if h.Offset == o {
			if currentSequence[j] != h {
				panic("assertion failure")
			}
			j++
			// Alignment error forces file move; proceed otherwise
			if h.AlignedLength == h.CompressedSize+padding(h.CompressedSize) {
				o += h.AlignedLength
				totalProcessed += int64(h.CompressedSize)
				if progress != nil {
					progress <- totalProcessed
				}
				continue
			}
		}

		// Move files out of the way, if needed
		endo := o + h.CompressedSize + padding(h.CompressedSize)
		if currentSequence[j].Offset < endo {
			if a.hasChanges {
				// Finish last sequence of successful moves
				err := writeFTH()
				if err != nil {
					return err
				}
				// Exit opportunity
				select {
				case <-ctx.Done():
					return context.Canceled
				default:
				}
			}

			if endo < o+bl {
				endo = o + bl // At least 32MiB
			}
			passedFileTable := false
			for currentSequence[j].Offset < endo {
				if currentSequence[j] == &ftHeader {
					a.hasChanges = true
					passedFileTable = true
					j++
					continue
				}
				s := currentSequence[j].CompressedSize + padding(currentSequence[j].CompressedSize)
				k := j
				// Find an insertion spot
				for k+1 < ls && (currentSequence[k].EndOffset() < endo || currentSequence[k].spaceBetween(currentSequence[k+1]) < s) {
					k++
				}
				keo := currentSequence[k].EndOffset()
				if keo < endo {
					// Can only happen if we reached the last entry
					if k != ls-1 || keo != a.insertOffset {
						panic("assertion failure")
					}
					err := a.padChunks(int64(endo-keo), buffer, bs)
					if err != nil {
						return err
					}
					a.insertOffset = endo
					keo = endo
				}
				err := moveFile(k, j, keo)
				if err != nil {
					return err
				}
				// Exit opportunity
				select {
				case <-ctx.Done():
					return context.Canceled
				default:
				}
			}
			if passedFileTable {
				j--
			}

			if a.hasChanges {
				// Finish sequence of move aways
				err := writeFTH()
				if err != nil {
					return err
				}
				// Exit opportunity
				select {
				case <-ctx.Done():
					return context.Canceled
				default:
				}
			}
		}

		if h.Offset == o {
			// Alignment error fixed
			h.AlignedLength = h.CompressedSize + padding(h.CompressedSize)
			totalProcessed += int64(h.CompressedSize)
			if progress != nil {
				progress <- totalProcessed
			}
			continue
		}

		// Move this file in place
		hi := sort.Search(ls, func(i int) bool { return currentSequence[i].Offset >= h.Offset })
		if currentSequence[hi] != h {
			panic("assertion failure")
		}
		err := moveFile(j, hi, o)
		if err != nil {
			return err
		}

		j++
		o += h.AlignedLength
		totalProcessed += int64(h.CompressedSize)
		if progress != nil {
			progress <- totalProcessed
		}
	}

	// Finalize
	return a.writeFileTableAndHeader(true)
}
