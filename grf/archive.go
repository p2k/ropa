//
//  archive.go
//  ropa
//
//  Created by p2k on 22.03.2023
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

// TODO: Align file and file table insertion

package grf

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/text/encoding/korean"
)

var (
	magicBytes   = append([]byte("Master of Magic"), 0)
	euckrEncoder = korean.EUCKR.NewEncoder()
	euckrDecoder = korean.EUCKR.NewDecoder()
)

const (
	// FileFlag describes a regular compressed file. It should be set for every FileHeader.
	FileFlag = 1
	// MixcryptFlag describes a mixcrypt encrypted file (obsolete).
	MixcryptFlag = 2
	// DESFlag describes a DES encrypted file (obsolete).
	DESFlag = 4
)

func padding(s uint32) uint32 {
	m := s & 7
	if m == 0 {
		return 0
	}
	return 8 - m
}

// ArchiveError is returned to indicate problems within the GRF file.
type ArchiveError struct {
	msg string
}

func (e *ArchiveError) Error() string {
	return e.msg
}

// FileHeaderHash represents the blake2b-256 hash of a file.
type FileHeaderHash [blake2b.Size256]byte

// FileHeaderHashFromHex converts a hex string representation to a blake2b-256
// hash. Returns a zero hash on error.
func FileHeaderHashFromHex(s string) FileHeaderHash {
	var h FileHeaderHash
	if n, err := hex.Decode(h[:], []byte(s)); err != nil {
		for i := 0; i < n; i++ {
			h[i] = 0
		}
	}
	return h
}

// IsZero checks if all bytes of the hash are zero.
func (h *FileHeaderHash) IsZero() bool {
	// funroll loop
	return h[0] == 0 && h[1] == 0 && h[2] == 0 && h[3] == 0 &&
		h[4] == 0 && h[5] == 0 && h[6] == 0 && h[7] == 0 &&
		h[8] == 0 && h[9] == 0 && h[10] == 0 && h[11] == 0 &&
		h[12] == 0 && h[13] == 0 && h[14] == 0 && h[15] == 0 &&
		h[16] == 0 && h[17] == 0 && h[18] == 0 && h[19] == 0 &&
		h[20] == 0 && h[21] == 0 && h[22] == 0 && h[23] == 0 &&
		h[24] == 0 && h[25] == 0 && h[26] == 0 && h[27] == 0 &&
		h[28] == 0 && h[29] == 0 && h[30] == 0 && h[31] == 0
}

// SetZero sets all hash bytes to zero.
func (h *FileHeaderHash) SetZero() {
	h[0] = 0
	h[1] = 0
	h[2] = 0
	h[3] = 0
	h[4] = 0
	h[5] = 0
	h[6] = 0
	h[7] = 0
	h[8] = 0
	h[9] = 0
	h[10] = 0
	h[11] = 0
	h[12] = 0
	h[13] = 0
	h[14] = 0
	h[15] = 0
	h[16] = 0
	h[17] = 0
	h[18] = 0
	h[19] = 0
	h[20] = 0
	h[21] = 0
	h[22] = 0
	h[23] = 0
	h[24] = 0
	h[25] = 0
	h[26] = 0
	h[27] = 0
	h[28] = 0
	h[29] = 0
	h[30] = 0
	h[31] = 0
}

// SetWithHasher sets the hash's bytes using the hasher's `Sum()` method.
func (h *FileHeaderHash) SetWithHasher(hasher hash.Hash) {
	hs := h[:0]
	hh := hasher.Sum(hs)
	if &hh != &hs {
		copy(h[:], hh)
	}
}

// Hex returns a hex string representation.
func (h *FileHeaderHash) Hex() string {
	return hex.EncodeToString(h[:])
}

// FileHeader describes a file within a GRF archive.
type FileHeader struct {
	// Name is the name of the file.
	//
	// Forward slashes must be used instead of back slashes.
	Name string

	// Linkname is the name of the canonical file that holds the content.
	//
	// If Linkname is an empty string, the file is not a linked file.
	Linkname string

	// CompressedSize is the compressed size of the file in bytes.
	//
	// Manipulating this value directly is discouraged.
	CompressedSize uint32

	// AlignedLength is the compressed size of the file including padding in bytes.
	//
	// Manipulating this value directly is discouraged.
	AlignedLength uint32

	// Size is the uncompressed size of the file in bytes.
	//
	// Manipulating this value directly is discouraged.
	Size uint32

	// Flags is a byte value containing attributes of the file.
	Flags uint8

	// Offset is the position of the compressed data relative to the GRF header in bytes.
	//
	// Manipulating this value directly is discouraged.
	Offset uint32

	// Deleted marks the file as deleted.
	Deleted bool

	// Hash is a blake2b hash of the file's contents.
	Hash FileHeaderHash
}

// EndOffset calculates the file's end position including alignment
func (h *FileHeader) EndOffset() uint32 {
	return h.Offset + h.AlignedLength
}

func (h *FileHeader) spaceBetween(other *FileHeader) uint32 {
	return other.Offset - h.Offset - h.AlignedLength
}

// WriteTo writes the file header to the given writer.
//
// If the file is marked as deleted, this will return io.ErrUnexpectedEOF
func (h *FileHeader) WriteTo(w io.Writer) (int64, error) {
	if h.Deleted {
		return 0, io.ErrUnexpectedEOF
	}
	bName := strings.Join(strings.Split(h.Name, "/"), "\\")
	kName, err := euckrEncoder.Bytes([]byte(bName))
	if err != nil {
		return 0, err
	}
	n1, err := w.Write(kName)
	if err != nil {
		return 0, err
	}
	v := make([]byte, 18)
	binary.LittleEndian.PutUint32(v[1:], h.CompressedSize)
	binary.LittleEndian.PutUint32(v[5:], h.AlignedLength)
	binary.LittleEndian.PutUint32(v[9:], h.Size)
	v[13] = h.Flags
	binary.LittleEndian.PutUint32(v[14:], h.Offset)
	_, err = w.Write(v)
	if err != nil {
		return 0, err
	}
	return int64(n1) + 18, nil
}

// ReadFrom reads a file header from the given buffered reader.
func (h *FileHeader) ReadFrom(r *bufio.Reader) error {
	kName, err := r.ReadBytes(0)
	if err != nil {
		return err
	}
	bName, err := euckrDecoder.Bytes(kName[:len(kName)-1])
	if err != nil {
		return err
	}
	h.Name = strings.Join(strings.Split(string(bName), "\\"), "/")
	v := make([]byte, 17)
	_, err = io.ReadFull(r, v)
	if err != nil {
		return err
	}
	h.CompressedSize = binary.LittleEndian.Uint32(v)
	h.AlignedLength = binary.LittleEndian.Uint32(v[4:])
	if h.AlignedLength < h.CompressedSize {
		return &ArchiveError{"file table corrupted"}
	}
	h.Size = binary.LittleEndian.Uint32(v[8:])
	h.Flags = v[12]
	h.Offset = binary.LittleEndian.Uint32(v[13:])
	return nil
}

// Archive describes a GRF file.
type Archive struct {
	// Name of the file on disk (absolute path).
	//
	// Changing this value has no effect.
	Name string

	// IsReadonly reflects if the file has been opened read-only.
	//
	// Changing this value has no effect.
	IsReadonly bool

	// Key is the GRF key formerly used for encryption.
	//
	// By default this contains bytes with values of 1 through 14.
	Key [14]uint8

	// Seed is a random value formerly used for encryption.
	//
	// This value should always be zero.
	Seed uint32

	// FilesCount is the real number of files currently present in the GRF.
	FilesCount uint32

	// Version is the GRF file's version.
	//
	// This value should always be 0x200
	Version uint32

	// FileTableOffset is the position of the compressed file table relative to the GRF header in bytes.
	FileTableOffset uint32

	// FileTableCompressedSize is the size of the compressed file table in bytes.
	FileTableCompressedSize uint32

	// FileTableSize is the uncompressed size of the file table in bytes.
	FileTableSize uint32

	entriesInSequence []*FileHeader
	entriesByName     map[string]*FileHeader

	file         *os.File
	hasChanges   bool
	insertOffset uint32
}

// Create creates and opens a new GRF file at the given path.
func Create(name string) (*Archive, error) {
	name, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	file, err := os.Create(name)
	if err != nil {
		return nil, err
	}
	_, err = file.Write(magicBytes)
	if err != nil {
		file.Close()
		return nil, err
	}
	a := &Archive{
		Name:       name,
		Version:    0x200,
		file:       file,
		hasChanges: true,
	}
	for i := 0; i < 14; i++ {
		a.Key[i] = uint8(i + 1)
	}
	_, err = file.Write(make([]byte, 30))
	if err != nil {
		file.Close()
		return nil, err
	}
	return a, nil
}

// Open opens an existing GRF file at the given path.
//
// The file's internal headers are read and it's put in a state, where new
// data can be written or existing data can be read.
func Open(name string, readonly bool) (*Archive, error) {
	name, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if fi.Size() > math.MaxUint32 {
		return nil, &ArchiveError{"file too large"}
	}
	fileSize := uint32(fi.Size())
	flag := os.O_RDWR
	if readonly {
		flag = os.O_RDONLY
	}
	file, err := os.OpenFile(name, flag, 0666)
	if err != nil {
		return nil, err
	}

	// Auto-close on failure
	success := false
	defer func() {
		if !success {
			file.Close()
		}
	}()

	// Read primary header
	v := make([]byte, 16)
	n, err := file.Read(v)
	if err != nil {
		return nil, err
	}
	if n != 16 {
		return nil, &ArchiveError{"truncated file header"}
	}
	if !bytes.Equal(v, magicBytes) {
		return nil, &ArchiveError{"invalid file header"}
	}
	a := &Archive{
		Name:          name,
		IsReadonly:    readonly,
		entriesByName: make(map[string]*FileHeader),
		file:          file,
	}
	n, err = file.Read(a.Key[:])
	if err != nil {
		return nil, err
	}
	if n != 14 {
		return nil, &ArchiveError{"truncated file header"}
	}
	n, err = file.Read(v)
	if err != nil {
		return nil, err
	}
	if n != 16 {
		return nil, &ArchiveError{"truncated file header"}
	}
	a.FileTableOffset = binary.LittleEndian.Uint32(v)
	if a.FileTableOffset+50 > fileSize {
		return nil, &ArchiveError{"truncated file"}
	}
	a.Seed = binary.LittleEndian.Uint32(v[4:])
	a.FilesCount = binary.LittleEndian.Uint32(v[8:]) - a.Seed - 7
	a.Version = binary.LittleEndian.Uint32(v[12:])
	if a.Version != 0x200 {
		return nil, &ArchiveError{"invalid version"}
	}
	a.entriesInSequence = make([]*FileHeader, int(a.FilesCount))

	// Read file table
	_, err = file.Seek(int64(a.FileTableOffset)+46, io.SeekStart)
	if err != nil {
		return nil, err
	}
	_, err = file.Read(v[:8])
	if err != nil {
		return nil, err
	}
	a.FileTableCompressedSize = binary.LittleEndian.Uint32(v)
	if a.FileTableOffset+a.FileTableCompressedSize+46 > fileSize {
		return nil, &ArchiveError{"truncated file"}
	}
	a.FileTableSize = binary.LittleEndian.Uint32(v[4:])
	r, err := zlib.NewReader(io.LimitReader(file, int64(a.FileTableCompressedSize)))
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(r)
	var highestOffset, thisOffset uint32
	byOffset := make(map[uint32]*FileHeader)
	for i := range a.entriesInSequence {
		h := &FileHeader{}
		err = h.ReadFrom(br)
		if err != nil {
			r.Close()
			return nil, err
		}
		e := byOffset[h.Offset]
		if e != nil {
			h.Linkname = e.Name
		} else {
			byOffset[h.Offset] = h
		}
		a.entriesInSequence[i] = h
		if e := a.entriesByName[h.Name]; e != nil {
			// Invalid duplicate path; delete earlier file
			a.hasChanges = true
			e.Deleted = true
		}
		a.entriesByName[h.Name] = h
		thisOffset = h.EndOffset()
		if thisOffset > highestOffset {
			highestOffset = thisOffset
		}
	}
	_, err = br.ReadByte()
	r.Close()
	if err != io.EOF {
		return nil, &ArchiveError{"file table with trailing bytes"}
	}

	// Defuse auto-close
	success = true

	// Determine insertion spot; either highest offset or end of file table
	if highestOffset > a.FileTableOffset {
		a.insertOffset = highestOffset
	} else {
		a.insertOffset = a.FileTableOffset + 8 + a.FileTableCompressedSize
		pad := padding(a.FileTableCompressedSize)
		if fileSize >= a.insertOffset+46+pad {
			// Padded file table
			a.insertOffset += pad
		}
	}
	return a, nil
}

// AllFileNames returns an array of all file names within the GRF in the order
// of appearance.
func (a *Archive) AllFileNames() []string {
	fns := make([]string, len(a.entriesInSequence))

	i := 0
	for _, h := range a.entriesInSequence {
		if !h.Deleted {
			fns[i] = h.Name
			i++
		}
	}

	return fns[:i]
}

// TotalUncompressedSize returns all file sizes added up.
//
// Deleted files and linked files are not taken into account.
func (a *Archive) TotalUncompressedSize() int64 {
	var total int64
	seenOffsets := make(map[uint32]bool)
	for _, h := range a.entriesInSequence {
		if !h.Deleted {
			if !seenOffsets[h.Offset] {
				total += int64(h.Size)
				seenOffsets[h.Offset] = true
			}
		}
	}
	return total
}

// TotalCompressedSize returns all compressed file sizes added up.
//
// Deleted files and linked files are not taken into account.
func (a *Archive) TotalCompressedSize() int64 {
	var total int64
	seenOffsets := make(map[uint32]bool)
	for _, h := range a.entriesInSequence {
		if !h.Deleted {
			if !seenOffsets[h.Offset] {
				total += int64(h.CompressedSize)
				seenOffsets[h.Offset] = true
			}
		}
	}
	return total
}

// Head returns the file header of a given file within the GRF.
//
// This might return headers of files that have previously been marked as
// deleted.
func (a *Archive) Head(name string) *FileHeader {
	return a.entriesByName[name]
}

// RawPut inserts or replaces a file in the GRF directly.
//
// The given data stream needs to already be compressed correctly.
// No hashing is performed.
func (a *Archive) RawPut(name string, r io.Reader, uncompressedSize uint32, flags uint8) (*FileHeader, error) {
	err := a.preparePut()
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(a.file, r)
	if err != nil {
		return nil, err
	}
	return a.finalizePut(name, uncompressedSize, flags)
}

// Put inserts or replaces a file in the GRF.
//
// If the file already exists, its original data will remain in the GRF but
// becomes inaccessible (if no other header points to it). The returned header
// can be used to set flags or create hard links. The last parameter can be
// used to set the compression level 0-9. It is recommended to keep it at 9.
func (a *Archive) Put(name string, r io.Reader, compress int) (*FileHeader, error) {
	if compress < 0 {
		compress = 0
	} else if compress > 9 {
		compress = 9
	}
	hasher, err := blake2b.New256(nil)
	if err != nil {
		return nil, err
	}
	err = a.preparePut()
	if err != nil {
		return nil, err
	}
	t := io.TeeReader(bufio.NewReader(r), hasher)
	var w io.WriteCloser
	var flags uint8
	if compress > 0 {
		flags = FileFlag
		w, err = zlib.NewWriterLevel(a.file, compress)
		if err != nil {
			return nil, err
		}
	} else {
		w = a.file
	}
	s, err := io.Copy(w, t)
	if err != nil {
		return nil, err
	}
	if compress > 0 {
		err = w.Close()
		if err != nil {
			return nil, err
		}
	}
	h, err := a.finalizePut(name, uint32(s), flags)
	if err != nil {
		return nil, err
	}
	h.Hash.SetWithHasher(hasher)
	return h, nil
}

func (a *Archive) preparePut() error {
	_, err := a.file.Seek(int64(a.insertOffset)+46, io.SeekStart)
	if err != nil {
		return err
	}
	padInsert := padding(a.insertOffset)
	if padInsert > 0 {
		_, err = a.file.Write(make([]byte, padInsert))
		if err != nil {
			return err
		}
		a.insertOffset += padInsert
	}
	return nil
}

func (a *Archive) finalizePut(name string, uncompressedSize uint32, flags uint8) (*FileHeader, error) {
	realEnd, err := a.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	cs := uint32(realEnd - int64(a.insertOffset) - 46)
	pad := padding(cs)
	if pad > 0 {
		_, err = a.file.Write(make([]byte, pad))
		if err != nil {
			return nil, err
		}
	}
	if a.entriesByName == nil {
		a.entriesByName = make(map[string]*FileHeader)
	}
	header := a.entriesByName[name]
	if header == nil {
		header = &FileHeader{
			Name:           name,
			CompressedSize: cs,
			AlignedLength:  cs + pad,
			Size:           uncompressedSize,
			Flags:          flags,
			Offset:         a.insertOffset,
		}
		a.entriesInSequence = append(a.entriesInSequence, header)
		a.entriesByName[name] = header
		a.FilesCount++
	} else {
		header.Linkname = ""
		header.CompressedSize = cs
		header.AlignedLength = cs + pad
		header.Size = uncompressedSize
		header.Flags = flags
		header.Offset = a.insertOffset
		header.Hash.SetZero()
		if header.Deleted {
			header.Deleted = false
			a.FilesCount++
		}
	}
	a.insertOffset += header.AlignedLength
	a.hasChanges = true
	return header, nil
}

// Link creates a hard link to the given file.
//
// Does nothing if names are equal. If the given file already is a link, will
// walk back to the root file.
func (a *Archive) Link(name string, linkname string) (*FileHeader, error) {
	h := a.entriesByName[linkname]
	if h == nil || h.Deleted {
		return nil, os.ErrNotExist
	}
	return a.LinkHeader(name, h), nil
}

// LinkHeader creates a hard link to the given file header.
//
// Does nothing if name is equal to the name of the given file header. If the
// given header already is a link, will walk back to the root file.
func (a *Archive) LinkHeader(name string, header *FileHeader) *FileHeader {
	if name == header.Name {
		return header
	}

	for header.Linkname != "" {
		header = a.entriesByName[header.Linkname]
	}

	if a.entriesByName == nil {
		a.entriesByName = make(map[string]*FileHeader)
	}
	link := a.entriesByName[name]
	if link == nil {
		link = &FileHeader{
			Name:           name,
			Linkname:       header.Name,
			CompressedSize: header.CompressedSize,
			AlignedLength:  header.AlignedLength,
			Size:           header.Size,
			Flags:          header.Flags,
			Offset:         header.Offset,
			Hash:           header.Hash,
		}
		a.entriesInSequence = append(a.entriesInSequence, link)
		a.entriesByName[name] = link
		a.FilesCount++
	} else {
		link.Linkname = header.Name
		link.CompressedSize = header.CompressedSize
		link.AlignedLength = header.AlignedLength
		link.Size = header.Size
		link.Flags = header.Flags
		link.Offset = header.Offset
		link.Hash = header.Hash
		link.Deleted = false
	}

	a.hasChanges = true
	return link
}

// Get reads a file from the GRF.
//
// Returns the total number of bytes read and will also update the file's header
// to include a hash.
func (a *Archive) Get(name string, w io.Writer) (int64, error) {
	h := a.entriesByName[name]
	if h == nil || h.Deleted {
		return 0, os.ErrNotExist
	}
	_, err := a.file.Seek(int64(h.Offset)+46, io.SeekStart)
	if err != nil {
		return 0, err
	}
	hasher, err := blake2b.New256(nil)
	if err != nil {
		return 0, err
	}
	lr := io.LimitReader(a.file, int64(h.CompressedSize))
	var r io.ReadCloser
	if h.Flags&FileFlag != 0 {
		r, err = zlib.NewReader(lr)
		if err != nil {
			return 0, err
		}
	} else {
		r = io.NopCloser(lr)
	}
	t := io.TeeReader(bufio.NewReader(r), hasher)
	s, err := io.Copy(w, t)
	r.Close()
	if err != nil {
		return 0, err
	}
	copy(h.Hash[:], hasher.Sum(nil))
	if s != int64(h.Size) {
		return 0, &ArchiveError{"uncompressed size mismatch"}
	}
	return s, nil
}

// Delete removes a file from the GRF.
//
// The file is not physically removed, just its file header won't be written on
// closing.
func (a *Archive) Delete(name string) bool {
	h := a.entriesByName[name]
	if h == nil || h.Deleted {
		return false
	}
	h.Deleted = true
	a.FilesCount--
	a.hasChanges = true
	return true
}

// writeFileTableAndHeader at the insert point and advance the insert point
// including a padding.
func (a *Archive) writeFileTableAndHeader(truncate bool) error {
	if truncate {
		// In truncate mode, we place the header behind the last file
		var highestOffset, thisOffset uint32
		for _, h := range a.entriesInSequence {
			thisOffset = h.EndOffset()
			if thisOffset > highestOffset {
				highestOffset = thisOffset
			}
		}
		a.insertOffset = highestOffset
	}

	_, err := a.file.Seek(int64(a.insertOffset)+46, io.SeekStart)
	if err != nil {
		return err
	}
	padInsert := padding(a.insertOffset)
	if padInsert > 0 {
		_, err = a.file.Write(make([]byte, padInsert))
		if err != nil {
			return err
		}
		a.insertOffset += padInsert
	}
	a.FileTableOffset = a.insertOffset
	v := make([]byte, 16)
	_, err = a.file.Write(v[:8]) // Writes zeroes
	if err != nil {
		return err
	}
	w, err := zlib.NewWriterLevel(a.file, 9)
	if err != nil {
		return err
	}
	// Re-count
	a.FilesCount = 0
	a.FileTableSize = 0
	var entrySize int64
	for _, h := range a.entriesInSequence {
		entrySize, err = h.WriteTo(w)
		if err != io.ErrUnexpectedEOF {
			if err != nil {
				return err
			}
			a.FileTableSize += uint32(entrySize)
			a.FilesCount++
		}
	}
	err = w.Close()
	if err != nil {
		return err
	}
	fileTableRealEnd, err := a.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	a.FileTableCompressedSize = uint32(fileTableRealEnd - int64(a.insertOffset) - 54)
	pad := padding(a.FileTableCompressedSize)
	if pad > 0 {
		_, err = a.file.Write(v[:pad])
		if err != nil {
			return err
		}
	}
	if truncate {
		err = a.file.Truncate(fileTableRealEnd) // Truncate here
		if err != nil {
			return err
		}
	}

	binary.LittleEndian.PutUint32(v, a.FileTableCompressedSize)
	binary.LittleEndian.PutUint32(v[4:], a.FileTableSize)
	_, err = a.file.WriteAt(v[:8], int64(a.FileTableOffset)+46)
	if err != nil {
		return err
	}

	_, err = a.file.WriteAt(a.Key[:], 16)
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(v, a.FileTableOffset)
	binary.LittleEndian.PutUint32(v[4:], a.Seed)
	binary.LittleEndian.PutUint32(v[8:], a.FilesCount+a.Seed+7)
	binary.LittleEndian.PutUint32(v[12:], a.Version)
	_, err = a.file.WriteAt(v, 30)
	if err != nil {
		return err
	}
	a.insertOffset += a.FileTableCompressedSize + 8 + pad
	a.hasChanges = false
	return nil
}

// Close finalizes a GRF file.
//
// This will write the file table as well as all header fields if the file has
// been modified since opening.
func (a *Archive) Close() error {
	if !a.hasChanges {
		return a.file.Close()
	}

	err := a.writeFileTableAndHeader(true)
	if err != nil {
		return err
	}

	return a.file.Close()
}
