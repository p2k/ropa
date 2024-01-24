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
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"ropa/zopfli"
	"strings"
	"sync"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/korean"
	"lukechampine.com/blake3"
)

var (
	magicBytes    = append([]byte("Master of Magic"), 0)
	euckrEncoder  = korean.EUCKR.NewEncoder()
	euckrDecoder  = korean.EUCKR.NewDecoder()
	cp1252Encoder = charmap.Windows1252.NewEncoder()
	cp1252Decoder = charmap.Windows1252.NewDecoder()

	// ErrReadOnly is returned if an attempt is made to write to a read only archive.
	ErrReadOnly = errors.New("archive is read only")
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

// FileNameEncoder encodes a unicode string to the archive's internal format.
type FileNameEncoder func(s string) ([]byte, error)

// FileNameDecoder decodes the archive's internal format to a unicode string.
type FileNameDecoder func(b []byte) (string, error)

// SaneFileNameEncoder does the right thing and converts Korean characters using
// the EUCKR code page as it is used by the original game developers.
func SaneFileNameEncoder(s string) ([]byte, error) {
	return euckrEncoder.Bytes([]byte(s))
}

// SaneFileNameDecoder does the right thing and converts Korean characters using
// the EUCKR code page as it is used by the original game developers.
func SaneFileNameDecoder(b []byte) (string, error) {
	bb, err := euckrDecoder.Bytes(b)
	if err != nil {
		return "", err
	}
	return string(bb), nil
}

// InsaneFileNameEncoder turns messed up file names magically into the EUCKR
// representation they should have.
func InsaneFileNameEncoder(s string) ([]byte, error) {
	t := []byte(s)
	var subs map[int]byte
	j := 0
	for i, c := range s {
		if c == 0x81 || c == 0x8d || c == 0x8f || c == 0x90 || c == 0x9d {
			if subs == nil {
				subs = make(map[int]byte)
			}
			subs[j] = byte(c)
			t[i+1] = 0xa0
		}
		j++
	}
	t, err := cp1252Encoder.Bytes(t)
	if err != nil {
		return nil, err
	}
	for i, c := range subs {
		t[i] = c
	}
	return t, nil
}

// InsaneFileNameDecoder takes perfectly fine file names in their EUCKR
// representation and messes them up completely for no reason.
func InsaneFileNameDecoder(b []byte) (string, error) {
	t := b
	var subs map[int]byte
	for i, c := range b {
		if c == 0x81 || c == 0x8d || c == 0x8f || c == 0x90 || c == 0x9d {
			if subs == nil {
				subs = make(map[int]byte)
				t = make([]byte, len(b))
				copy(t, b)
			}
			subs[i] = c
			t[i] = 0xa0
		}
	}
	t, err := cp1252Decoder.Bytes(t)
	if err != nil {
		return "", err
	}
	s := string(t)
	if subs != nil {
		j := 0
		for i := range s {
			if c, ok := subs[j]; ok {
				t[i+1] = c
			}
			j++
		}
		return string(t), nil
	}
	return s, nil
}

// SanitizeFileName restores the original Korean characters back from garbage.
func SanitizeFileName(s string) (string, error) {
	ks, err := InsaneFileNameEncoder(s)
	if err != nil {
		return "", err
	}
	return SaneFileNameDecoder(ks)
}

// ContamineFileName turns the original Korean characters to garbage.
func ContamineFileName(s string) (string, error) {
	ks, err := SaneFileNameEncoder(s)
	if err != nil {
		return "", err
	}
	return InsaneFileNameDecoder(ks)
}

// ArchiveError is returned to indicate problems within the GRF file.
type ArchiveError struct {
	msg string
}

func (e *ArchiveError) Error() string {
	return e.msg
}

// FileHeaderHash represents the BLAKE3-256 hash of a file.
type FileHeaderHash [32]byte

// FileHeaderHashFromHex converts a hex string representation to a BLAKE3-256
// hash. Returns a zero hash on error.
func FileHeaderHashFromHex(s string) FileHeaderHash {
	var h FileHeaderHash
	if n, err := hex.Decode(h[:], []byte(s)); err != nil && n == 32 {
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

	// Hash is a BLAKE3-256 hash of the file's contents.
	Hash FileHeaderHash

	// StreamHash is a BLAKE3-256 hash of the file's raw/compressed data.
	StreamHash FileHeaderHash
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
// Uses the given encoder to encode the file name. If the file is marked as
// deleted, this will return io.ErrUnexpectedEOF.
func (h *FileHeader) WriteTo(w io.Writer, e FileNameEncoder) (int64, error) {
	if h.Deleted {
		return 0, io.ErrUnexpectedEOF
	}
	bName := strings.Join(strings.Split(h.Name, "/"), "\\")
	kName, err := e(bName)
	if err != nil {
		return 0, fmt.Errorf("%s - %w", bName, err)
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
//
// Uses the given decoder to decode the file name.
func (h *FileHeader) ReadFrom(r *bufio.Reader, d FileNameDecoder) error {
	kName, err := r.ReadBytes(0)
	if err != nil {
		return err
	}
	sName, err := d(kName[:len(kName)-1])
	if err != nil {
		return err
	}
	h.Name = strings.Join(strings.Split(sName, "\\"), "/")
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

	// KoreanFilenames indicates if the EUCKR text encoding is used for files
	// inside the GRF. If not, the Windows-1252 codepage is used.
	//
	// Changing this will likely produce errors when finalizing the archive.
	KoreanFilenames bool

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
	folders           map[string]map[string]bool

	wfile        *os.File
	rfile        io.ReadSeekCloser
	hasChanges   bool
	insertOffset uint32
	m            sync.RWMutex
	lf           *ArchivedFile // last used archived file
}

// Create creates and opens a new GRF file at the given path.
//
// The second parameter controls whether you want to use Korean filenames or
// (mangled) filenames using the Windows-1252 codepage.
func Create(name string, koreanFilenames bool) (*Archive, error) {
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
		Name:            name,
		KoreanFilenames: koreanFilenames,
		Version:         0x200,
		wfile:           file,
		rfile:           file,
		hasChanges:      true,
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

func (a *Archive) insertFolderEntry(name string) {
	if a.folders == nil {
		a.folders = make(map[string]map[string]bool)
	}
	var entry string
	for name != "." {
		name, entry = path.Split(name)
		if name != "" {
			name = name[:len(name)-1]
		} else {
			name = "."
		}
		folder := a.folders[name]
		if folder == nil {
			folder = make(map[string]bool)
			a.folders[name] = folder
		}
		folder[entry] = true
	}
}

func (a *Archive) deleteFolderEntry(name string) {
	if a.folders == nil {
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
		folder := a.folders[name]
		if folder != nil {
			delete(folder, entry)
			if len(folder) > 0 {
				return
			}
			delete(a.folders, name)
		}
	}
}

// Open opens an existing GRF file at the given path.
//
// The file's internal headers are read and it's put in a state, where new
// data can be written or existing data can be read. The last parameter controls
// whether you want to use Korean filenames or (mangled) filenames using the
// Windows-1252 codepage.
func Open(name string, readonly, koreanFilenames bool) (*Archive, error) {
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

	ar, err := OpenIO(file, fileSize, koreanFilenames)
	if err != nil {
		return nil, err
	}

	ar.Name = name
	if !readonly {
		ar.IsReadonly = false
		ar.wfile = file
	}

	return ar, nil
}

// OpenIO opens a GRF file via the given io interface and file size.
//
// Archives opened like this are inherently read only. Close will be called on
// the given interface when the archive is closed.
func OpenIO(file io.ReadSeekCloser, fileSize uint32, koreanFilenames bool) (*Archive, error) {
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
		KoreanFilenames: koreanFilenames,
		IsReadonly:      true,
		entriesByName:   make(map[string]*FileHeader),
		folders:         make(map[string]map[string]bool),
		rfile:           file,
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
	var d = SaneFileNameDecoder
	if !koreanFilenames {
		d = InsaneFileNameDecoder
	}
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
		err = h.ReadFrom(br, d)
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
			a.deleteFolderEntry(h.Name)
			e.Deleted = true
		}
		a.entriesByName[h.Name] = h
		a.insertFolderEntry(h.Name)
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
	a.m.RLock()
	defer a.m.RUnlock()

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
	a.m.RLock()
	defer a.m.RUnlock()

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
	a.m.RLock()
	defer a.m.RUnlock()

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
	a.m.RLock()
	defer a.m.RUnlock()

	if a.entriesByName != nil {
		return a.entriesByName[name]
	}
	return nil
}

// RawPut inserts or replaces a file in the GRF directly.
//
// The given data stream needs to already be compressed correctly.
// Will calculate the StreamHash only.
func (a *Archive) RawPut(name string, r io.Reader, uncompressedSize uint32, flags uint8) (*FileHeader, error) {
	if a.wfile == nil {
		return nil, ErrReadOnly
	}

	a.m.Lock()
	defer a.m.Unlock()

	streamHasher := blake3.New(32, nil)
	err := a.preparePut(name)
	if err != nil {
		return nil, err
	}
	t := io.TeeReader(r, streamHasher)
	_, err = io.Copy(a.wfile, t)
	if err != nil {
		return nil, err
	}
	h, err := a.finalizePut(name, uncompressedSize, flags)
	if err != nil {
		return nil, err
	}
	h.StreamHash.SetWithHasher(streamHasher)
	return h, nil
}

// RawPutBytes inserts or replaces a file in the GRF directly.
//
// The given bytes need to already be compressed correctly.
// Will calculate the stream hash only, except if the file is not marked as
// compressed where it will also set the content hash since they are identical.
func (a *Archive) RawPutBytes(name string, b []byte, uncompressedSize uint32, flags uint8) (*FileHeader, error) {
	if a.wfile == nil {
		return nil, ErrReadOnly
	}

	a.m.Lock()
	defer a.m.Unlock()

	streamHasher := blake3.New(32, nil)
	err := a.preparePut(name)
	if err != nil {
		return nil, err
	}
	_, err = streamHasher.Write(b)
	if err != nil {
		return nil, err
	}
	_, err = a.wfile.Write(b)
	if err != nil {
		return nil, err
	}
	h, err := a.finalizePut(name, uncompressedSize, flags)
	if err != nil {
		return nil, err
	}
	h.StreamHash.SetWithHasher(streamHasher)
	if flags&FileFlag == 0 {
		h.Hash.SetWithHasher(streamHasher)
	}
	return h, nil
}

// Put inserts or replaces a file in the GRF.
//
// If the file already exists, its original data will remain in the GRF but
// becomes inaccessible (if no other header points to it). The returned header
// can be used to set flags or create hard links. The last parameter can be
// used to set the compression level. Values > 9 will use Zopfli compression
// with the given number of iterations; 15 is the recommended default for this.
func (a *Archive) Put(name string, r io.Reader, compress int) (*FileHeader, error) {
	if a.wfile == nil {
		return nil, ErrReadOnly
	}

	a.m.Lock()
	defer a.m.Unlock()

	hasher := blake3.New(32, nil)
	streamHasher := blake3.New(32, nil)
	err := a.preparePut(name)
	if err != nil {
		return nil, err
	}
	tr := io.TeeReader(r, hasher)
	var s int64
	var flags uint8
	if compress < 10 {
		var w io.WriteCloser
		var tw io.Writer
		if compress != 0 {
			flags = FileFlag
			w, err = zlib.NewWriterLevel(a.wfile, compress)
			tw = io.MultiWriter(w, streamHasher)
			if err != nil {
				return nil, err
			}
		} else {
			streamHasher = hasher
			w = a.wfile
			tw = w
		}
		s, err = io.Copy(tw, tr)
		if err != nil {
			return nil, err
		}
		if compress != 0 {
			err = w.Close()
			if err != nil {
				return nil, err
			}
		}
	} else {
		w := &bytes.Buffer{}
		s, err = io.Copy(w, tr)
		if err != nil {
			return nil, err
		}
		b := w.Bytes()
		b = zopfli.Zlib(b, compress, b)
		_, err = streamHasher.Write(b)
		if err != nil {
			return nil, err
		}
		_, err = a.wfile.Write(b)
		if err != nil {
			return nil, err
		}
	}
	h, err := a.finalizePut(name, uint32(s), flags)
	if err != nil {
		return nil, err
	}
	h.Hash.SetWithHasher(hasher)
	h.StreamHash.SetWithHasher(streamHasher)
	return h, nil
}

func (a *Archive) preparePut(name string) error {
	if name == "." || (a.folders != nil && a.folders[name] != nil) {
		return &ArchiveError{"cannot write to a directory entry"}
	}
	if a.lf != nil {
		a.lf = nil
	}
	_, err := a.wfile.Seek(int64(a.insertOffset)+46, io.SeekStart)
	if err != nil {
		return err
	}
	padInsert := padding(a.insertOffset)
	if padInsert > 0 {
		_, err = a.wfile.Write(make([]byte, padInsert))
		if err != nil {
			return err
		}
		a.insertOffset += padInsert
	}
	return nil
}

func (a *Archive) finalizePut(name string, uncompressedSize uint32, flags uint8) (*FileHeader, error) {
	realEnd, err := a.wfile.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	cs := uint32(realEnd - int64(a.insertOffset) - 46)
	pad := padding(cs)
	if pad > 0 {
		_, err = a.wfile.Write(make([]byte, pad))
		if err != nil {
			return nil, err
		}
	}
	if a.entriesByName == nil {
		a.entriesByName = make(map[string]*FileHeader)
	}
	// TODO this can change linked files
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
		a.insertFolderEntry(name)
		a.FilesCount++
	} else {
		header.Linkname = ""
		header.CompressedSize = cs
		header.AlignedLength = cs + pad
		header.Size = uncompressedSize
		header.Flags = flags
		header.Offset = a.insertOffset
		header.Hash.SetZero()
		header.StreamHash.SetZero()
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
	if a.wfile == nil {
		return nil, ErrReadOnly
	}

	a.m.RLock()
	h := a.entriesByName[linkname]
	a.m.RUnlock()
	if h == nil || h.Deleted {
		return nil, fs.ErrNotExist
	}
	return a.LinkHeader(name, h), nil
}

// LinkHeader creates a hard link to the given file header.
//
// Does nothing if name is equal to the name of the given file header. If the
// given header already is a link, will walk back to the root file.
func (a *Archive) LinkHeader(name string, header *FileHeader) *FileHeader {
	if a.wfile == nil {
		return nil
	}

	if name == header.Name {
		return header
	}

	a.m.Lock()
	defer a.m.Unlock()

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
			StreamHash:     header.StreamHash,
		}
		a.entriesInSequence = append(a.entriesInSequence, link)
		a.entriesByName[name] = link
		a.insertFolderEntry(name)
		a.FilesCount++
	} else {
		link.Linkname = header.Name
		link.CompressedSize = header.CompressedSize
		link.AlignedLength = header.AlignedLength
		link.Size = header.Size
		link.Flags = header.Flags
		link.Offset = header.Offset
		link.Hash = header.Hash
		link.StreamHash = header.StreamHash
		link.Deleted = false
	}

	a.hasChanges = true
	return link
}

// Get reads a file from the GRF in one go.
//
// Returns the total number of bytes read and will also update the file's header
// to include its hash and stream hash.
func (a *Archive) Get(name string, w io.Writer) (int64, error) {
	a.m.Lock()
	defer a.m.Unlock()

	if a.entriesByName == nil {
		return 0, fs.ErrNotExist
	}
	h := a.entriesByName[name]
	if h == nil || h.Deleted {
		return 0, fs.ErrNotExist
	}

	if a.lf != nil {
		a.lf = nil
	}

	_, err := a.rfile.Seek(int64(h.Offset)+46, io.SeekStart)
	if err != nil {
		return 0, err
	}
	var hasher hash.Hash
	var streamHasher hash.Hash
	if h.Hash.IsZero() {
		hasher = blake3.New(32, nil)
	}
	lr := io.LimitReader(a.rfile, int64(h.CompressedSize))
	if h.StreamHash.IsZero() {
		streamHasher = blake3.New(32, nil)
		lr = io.TeeReader(lr, streamHasher)
	}
	var zr io.ReadCloser
	if h.Flags&FileFlag != 0 {
		zr, err = zlib.NewReader(lr)
		if err != nil {
			return 0, err
		}
	} else {
		zr = io.NopCloser(lr)
		if streamHasher != nil {
			hasher = streamHasher
		}
	}
	var r io.Reader
	if hasher != nil && hasher != streamHasher {
		r = io.TeeReader(zr, hasher)
	} else {
		r = zr
	}
	s, err := io.Copy(w, r)
	zr.Close()
	if err != nil {
		return 0, err
	}
	if hasher != nil {
		h.Hash.SetWithHasher(hasher)
	}
	if streamHasher != nil {
		h.StreamHash.SetWithHasher(streamHasher)
	}
	if s != int64(h.Size) {
		return 0, &ArchiveError{"uncompressed size mismatch"}
	}
	return s, nil
}

// Rename renames (moves) oldname to newname in the GRF.
func (a *Archive) Rename(oldname string, newname string) bool {
	if a.wfile == nil {
		return false
	}

	a.m.Lock()
	defer a.m.Unlock()

	oldh := a.entriesByName[oldname]
	if oldh == nil || oldh.Deleted {
		return false
	}

	// TODO update linked files

	delete(a.entriesByName, oldname)
	newh := a.entriesByName[newname]
	if newh != nil {
		newh.Deleted = true
		a.FilesCount--
	}

	oldh.Name = newname
	a.entriesByName[newname] = oldh

	a.hasChanges = true
	return true
}

// Delete removes a file from the GRF.
//
// The file is not physically removed, just its file header won't be written on
// closing.
func (a *Archive) Delete(name string) bool {
	if a.wfile == nil {
		return false
	}

	a.m.Lock()
	defer a.m.Unlock()

	if a.entriesByName == nil {
		return false
	}
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

	_, err := a.wfile.Seek(int64(a.insertOffset)+46, io.SeekStart)
	if err != nil {
		return err
	}
	padInsert := padding(a.insertOffset)
	if padInsert > 0 {
		_, err = a.wfile.Write(make([]byte, padInsert))
		if err != nil {
			return err
		}
		a.insertOffset += padInsert
	}
	a.FileTableOffset = a.insertOffset
	v := make([]byte, 16)
	_, err = a.wfile.Write(v[:8]) // Writes zeroes
	if err != nil {
		return err
	}
	w, err := zlib.NewWriterLevel(a.wfile, 9)
	if err != nil {
		return err
	}
	// Re-count
	a.FilesCount = 0
	a.FileTableSize = 0
	e := SaneFileNameEncoder
	if !a.KoreanFilenames {
		e = InsaneFileNameEncoder
	}
	var entrySize int64
	for _, h := range a.entriesInSequence {
		entrySize, err = h.WriteTo(w, e)
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
	fileTableRealEnd, err := a.wfile.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	a.FileTableCompressedSize = uint32(fileTableRealEnd - int64(a.insertOffset) - 54)
	pad := padding(a.FileTableCompressedSize)
	if pad > 0 {
		_, err = a.wfile.Write(v[:pad])
		if err != nil {
			return err
		}
	}
	if truncate {
		err = a.wfile.Truncate(fileTableRealEnd) // Truncate here
		if err != nil {
			return err
		}
	}

	binary.LittleEndian.PutUint32(v, a.FileTableCompressedSize)
	binary.LittleEndian.PutUint32(v[4:], a.FileTableSize)
	_, err = a.wfile.WriteAt(v[:8], int64(a.FileTableOffset)+46)
	if err != nil {
		return err
	}

	_, err = a.wfile.WriteAt(a.Key[:], 16)
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(v, a.FileTableOffset)
	binary.LittleEndian.PutUint32(v[4:], a.Seed)
	binary.LittleEndian.PutUint32(v[8:], a.FilesCount+a.Seed+7)
	binary.LittleEndian.PutUint32(v[12:], a.Version)
	_, err = a.wfile.WriteAt(v, 30)
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
// been modified since opening (and isn't read only).
func (a *Archive) Close() error {
	a.m.Lock()
	defer a.m.Unlock()

	if !a.hasChanges || a.wfile == nil {
		return a.rfile.Close()
	}

	err := a.writeFileTableAndHeader(true)
	if err != nil {
		return err
	}

	return a.wfile.Close()
}
