//
//  cmd_filebase.go
//  ropa
//
//  Created by p2k on 16.04.2023
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

package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"ropa/grf"
	"ropa/zopfli"
	"sort"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/dustin/go-humanize/english"
	"github.com/schollz/progressbar/v3"
	"lukechampine.com/blake3"
)

type filebaseEntry struct {
	realpath   string
	cmppath    string
	pathspl    []string
	isDir      bool
	isSymlink  bool
	isWhiteout bool
	size       int64
}

type virtualGRF struct {
	basePath string
	h        hash.Hash
	hashMap  map[string]grf.FileHeaderHash
}

func newVirtualGRF(basePath string, hasher hash.Hash) *virtualGRF {
	return &virtualGRF{basePath: basePath, h: hasher, hashMap: make(map[string]grf.FileHeaderHash)}
}

func (g *virtualGRF) Hash(name string) (grf.FileHeaderHash, error) {
	h, ok := g.hashMap[name]
	if ok {
		return h, nil
	}

	r, err := os.Open(filepath.Join(g.basePath, filepath.FromSlash(name)))
	if err != nil {
		return h, err
	}
	g.h.Reset()
	_, err = io.Copy(g.h, r)
	r.Close()
	if err != nil {
		return h, err
	}
	h.SetWithHasher(g.h)
	g.hashMap[name] = h

	return h, nil
}

func (g *virtualGRF) Get(name string, w io.Writer) (int64, error) {
	r, err := os.Open(filepath.Join(g.basePath, filepath.FromSlash(name)))
	if err != nil {
		return 0, err
	}
	g.h.Reset()
	n, err := io.Copy(w, io.TeeReader(r, g.h))
	r.Close()
	if err != nil {
		return 0, err
	}
	var h grf.FileHeaderHash
	h.SetWithHasher(g.h)
	g.hashMap[name] = h

	return n, nil
}

type filebaseGRF interface {
	Hash(name string) (grf.FileHeaderHash, error)
	Get(name string, w io.Writer) (int64, error)
}

func (e *filebaseEntry) writeContentIfNeeded(contentFolderPath string, h grf.FileHeaderHash, s hash.Hash, d []byte, compress int) error {
	contentPath := filepath.Join(contentFolderPath, fmt.Sprintf("%s-%08x.zl", h.Hex(), e.size))
	fi, err := os.Stat(contentPath)
	if err != nil || fi.Size() == 0 {
		if err == nil || os.IsNotExist(err) {
			if strings.HasSuffix(e.cmppath, ".mp3") || strings.HasSuffix(e.cmppath, ".grf") {
				compress = 0
			}
			w, err := os.Create(contentPath)
			if err != nil {
				return err
			}
			defer w.Close()
			if compress < 10 {
				var zc *zlib.Writer
				if s != nil {
					zc, err = zlib.NewWriterLevel(io.MultiWriter(w, s), compress)
				} else {
					zc, err = zlib.NewWriterLevel(w, compress)
				}
				if err != nil {
					return err
				}
				_, err = zc.Write(d)
				if err == nil {
					err = zc.Close()
				}
			} else {
				d = zopfli.Zlib(d, compress, d)
				if s != nil {
					s.Write(d)
				}
				_, err = w.Write(d)
			}
			return err
		} else {
			return err
		}
	} else if s != nil {
		w, err := os.Open(contentPath)
		if err != nil {
			return err
		}
		_, err = io.Copy(s, w)
		w.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

type filebaseBuilder struct {
	b       *bytes.Buffer
	indent  [][]byte
	lastspl []string
	sp      []byte
	co      []byte
	lf      []byte
	wo      []byte
	q       *regexp.Regexp
}

func newFilebaseBuilder() *filebaseBuilder {
	b := &filebaseBuilder{
		b:      &bytes.Buffer{},
		indent: make([][]byte, 16),
		sp:     []byte{' '},
		co:     []byte{':'},
		lf:     []byte{'\n'},
		wo:     []byte(" 0000000000000000000000000000000000000000000000000000000000000000-00000000\n"),
		q:      regexp.MustCompile(`^\s|^-\s|^[[{,&*!?|>#%@"']|\s#|:\s|\s$|^\+?\d*(\.\d*)?$`),
	}

	master := make([]byte, 32)
	for i := 0; i < 32; i++ {
		master[i] = ' '
	}
	for i := 0; i < 16; i++ {
		b.indent[i] = master[:(i+1)*2]
	}

	b.b.Write([]byte("type: ROPAFilebase/v1\n"))

	return b
}

// Note: if the file name ends with .gz, you can only save once since the
// buffer is re-used to hold the compressed data.
func (b *filebaseBuilder) save(filebaseFilePath string) string {
	f, err := os.Create(filebaseFilePath)
	if err != nil {
		return fmt.Sprint("Error while creating filebase file:", err)
	}
	defer f.Close()

	data := b.b.Bytes()
	if strings.HasSuffix(filebaseFilePath, ".gz") {
		data = zopfli.Gzip(data, 15, data)
	}

	_, err = f.Write(data)
	f.Close()
	if err != nil {
		return fmt.Sprint("Error while writing filebase file:", err)
	}

	return "All done."
}

func (b *filebaseBuilder) writeQuotedIfNeeded(s string) error {
	if len(s) == 0 || b.q.MatchString(s) {
		bs, err := json.Marshal(s)
		if err != nil {
			return err
		}
		_, err = b.b.Write(bs)
		return err
	}

	_, err := b.b.Write([]byte(s))
	return err
}

func (b *filebaseBuilder) writePath(pathspl []string) error {
	m := len(b.lastspl)
	n := len(pathspl)
	var err error
	for i := 0; i < n; i++ {
		if i < m && b.lastspl[i] == pathspl[i] {
			continue
		}

		_, err = b.b.Write(b.indent[i])
		if err != nil {
			return err
		}
		err = b.writeQuotedIfNeeded(pathspl[i])
		if err != nil {
			return err
		}
		_, err = b.b.Write(b.co)
		if err != nil {
			return err
		}
		if i+1 < n {
			_, err = b.b.Write(b.lf)
			if err != nil {
				return err
			}
		}
	}

	b.lastspl = pathspl
	return nil
}

func (b *filebaseBuilder) writeEmpty() error {
	_, err := b.b.Write(b.lf)
	return err
}

func (b *filebaseBuilder) writeWhiteout() error {
	_, err := b.b.Write(b.wo)
	return err
}

func (b *filebaseBuilder) writeHashAndSize(h grf.FileHeaderHash, s int64) error {
	_, err := b.b.Write(b.sp)
	if err != nil {
		return err
	}
	_, err = b.b.Write([]byte(fmt.Sprintf("%s-%08x", h.Hex(), s)))
	if err != nil {
		return err
	}
	_, err = b.b.Write(b.lf)
	return err
}

func (b *filebaseBuilder) writeRootContents() error {
	_, err := b.b.Write([]byte("root_contents:\n"))
	b.lastspl = nil
	return err
}

func (b *filebaseBuilder) writeGRFContents() error {
	_, err := b.b.Write([]byte("grf_contents:\n"))
	b.lastspl = nil
	return err
}

func (b *filebaseBuilder) writeGRFName(name string) error {
	_, err := b.b.Write([]byte("grf_name: "))
	if err != nil {
		return err
	}
	err = b.writeQuotedIfNeeded(name)
	if err != nil {
		return err
	}
	_, err = b.b.Write(b.lf)
	return err
}

type integrityMap struct {
	b   *bytes.Buffer
	s2c map[grf.FileHeaderHash]grf.FileHeaderHash
}

func newIntegrityMap() *integrityMap {
	m := &integrityMap{
		b:   &bytes.Buffer{},
		s2c: make(map[grf.FileHeaderHash]grf.FileHeaderHash),
	}

	m.b.Write([]byte("type: ROPAIntegrityMap/v1\n"))
	m.b.Write([]byte("s2c:\n"))

	return m
}

func (m *integrityMap) load(integrityFilePath string) error {
	if m.b.Len() > 0 {
		m.b.Reset()
	}

	r, err := os.Open(integrityFilePath)
	if err != nil {
		return err
	}
	defer r.Close()
	var scanner *bufio.Scanner
	if strings.HasSuffix(integrityFilePath, ".gz") {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		scanner = bufio.NewScanner(zr)
	} else {
		scanner = bufio.NewScanner(r)
	}
	gotType := false
	inS2C := false
	for scanner.Scan() {
		t := scanner.Text()
		if !inS2C {
			if strings.HasPrefix(t, "type:") {
				if t == "type: ROPAIntegrityMap/v1" {
					gotType = true
				} else {
					return errors.New("invalid integrity file type")
				}
				m.b.WriteString(t)
				m.b.WriteByte('\n')
			} else if gotType && t == "s2c:" {
				inS2C = true
				m.b.WriteString(t)
				m.b.WriteByte('\n')
			}
		} else if strings.HasPrefix(t, "  ") {
			spl := strings.SplitN(t[2:len(t)-1], ": ", 2)
			if len(spl) == 2 {
				h1 := grf.FileHeaderHashFromHex(spl[0])
				h2 := grf.FileHeaderHashFromHex(spl[1])
				if !h1.IsZero() && !h2.IsZero() {
					m.s2c[h1] = h2
					m.b.WriteString(t)
					m.b.WriteByte('\n')
				}
			}
		} else {
			inS2C = false
		}
	}

	return nil
}

// Note: if the file name ends with .gz, you can only save once since the
// buffer is used to hold compressed data
func (m *integrityMap) save(integrityFilePath string) string {
	f, err := os.Create(integrityFilePath)
	if err != nil {
		return fmt.Sprint("Error while creating integrity file:", err)
	}
	defer f.Close()

	data := m.b.Bytes()
	if strings.HasSuffix(integrityFilePath, ".gz") {
		data = zopfli.Gzip(data, 15, data)
	}

	_, err = f.Write(data)
	f.Close()
	if err != nil {
		return fmt.Sprint("Error while writing integrity file:", err)
	}

	return "All done."
}

func (m *integrityMap) add(streamHasher hash.Hash, contentHash grf.FileHeaderHash) {
	var streamHash grf.FileHeaderHash
	streamHash.SetWithHasher(streamHasher)
	if _, ok := m.s2c[streamHash]; !ok {
		m.s2c[streamHash] = contentHash
		m.b.Write([]byte(fmt.Sprintf("  %s: %s\n", streamHash.Hex(), contentHash.Hex())))
	}
}

func runFilebase(filebaseFilePath string, inputFilePathsPatterns []string, grfFilePath, grfNameOverride, whiteoutFilePath, contentFolderPath string, compress int, koreanFilenames, quiet bool) bool {
	hasher := blake3.New(32, nil)

	imap := newIntegrityMap()
	imapPath := filepath.Join(contentFolderPath, "integrity.yml.gz")
	writeContent := false
	if contentFolderPath != "" {
		cfi, err := os.Stat(contentFolderPath)
		if err != nil {
			fmt.Println("Error while statting content folder:", err)
			return false
		}
		if !cfi.IsDir() {
			fmt.Println("Error: content folder path does not point to a folder")
			return false
		}

		if _, err = os.Stat(imapPath); err == nil {
			err = imap.load(imapPath)
			if err != nil {
				fmt.Println("Error: could not read integrity file")
				return false
			}
		}

		writeContent = true
	}

	var bar *progressbar.ProgressBar
	if !quiet {
		bar = progressbar.NewOptions64(
			1,
			progressbar.OptionSetDescription("Listing files"),
			progressbar.OptionFullWidth(),
			progressbar.OptionShowBytes(true),
			progressbar.OptionSetRenderBlankState(true),
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        "=",
				SaucerHead:    ">",
				SaucerPadding: " ",
				BarStart:      "[",
				BarEnd:        "]",
			}))
	} else {
		fmt.Println("Listing files...")
	}

	grfIsDir := false
	if grfFilePath != "" {
		var err error
		grfFilePath, err = filepath.Abs(grfFilePath)
		if err != nil {
			if bar != nil {
				bar.Exit()
				fmt.Println()
			}
			fmt.Println("Error while finding absolute path to GRF file:", err)
			return false
		}

		grfFi, err := os.Stat(grfFilePath)
		if err != nil {
			if bar != nil {
				bar.Exit()
				fmt.Println()
			}
			fmt.Println("Error while statting GRF file:", err)
			return false
		}

		grfIsDir = grfFi.IsDir()
	}

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt)
	defer signal.Stop(sc)

	sep := string(filepath.Separator)

	var totalUncompressed, nDirs, nFiles int64
	inputFiles := make([]*filebaseEntry, 0, len(inputFilePathsPatterns))
	inputFilesIndex := make(map[string]int)
	for _, pattern := range inputFilePathsPatterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			if bar != nil {
				bar.Exit()
				fmt.Println()
			}
			fmt.Printf("Match pattern \"%s\" error: %s\n", pattern, err)
			return false
		}
		for _, path := range matches {
			err = filepath.WalkDir(path, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				select {
				case <-sc:
					return context.Canceled
				default:
				}
				sanitizedPath := path
				if !koreanFilenames {
					sanitizedPath, err = grf.SanitizeFileName(path)
					if err != nil {
						return fmt.Errorf("problematic file name in root: %s - %w", path, err)
					}
				}
				cmppath := filepath.ToSlash(strings.ToLower(sanitizedPath))
				if d.IsDir() {
					cmppath = cmppath + "/"
				}
				if _, ok := inputFilesIndex[cmppath]; ok {
					return nil
				}
				realpath, err := filepath.Abs(path)
				if err != nil {
					return err
				}
				pathspl := strings.Split(sanitizedPath, sep)
				basename := pathspl[len(pathspl)-1]
				if realpath == grfFilePath || basename == ".git" || basename == ".gitignore" {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if d.IsDir() {
					nDirs++
					inputFilesIndex[cmppath] = len(inputFiles)
					inputFiles = append(inputFiles, &filebaseEntry{cmppath: cmppath, pathspl: pathspl, isDir: true})
				} else {
					fi, err := d.Info()
					if err != nil {
						return err
					}
					nFiles++
					size := fi.Size()
					inputFilesIndex[cmppath] = len(inputFiles)
					inputFiles = append(inputFiles, &filebaseEntry{realpath: realpath, cmppath: cmppath, pathspl: pathspl, size: size})
					totalUncompressed += size
				}
				return err
			})
			if err != nil {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				if err == context.Canceled {
					fmt.Println("Cancelled.")
				} else {
					fmt.Println("Error while listing files:", err)
				}
				return false
			}
		}
	}

	if whiteoutFilePath != "" {
		r, err := os.Open(whiteoutFilePath)
		if err != nil {
			fmt.Println("Error while opening whiteout file:", err)
			return false
		}
		defer r.Close()

		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			path := scanner.Text()
			sanitizedPath := path
			if !koreanFilenames {
				sanitizedPath, err = grf.SanitizeFileName(path)
				if err != nil {
					fmt.Printf("problematic file name in whiteout file: %s - %s\n", path, err)
					return false
				}
			}
			cmppath := filepath.ToSlash(strings.ToLower(sanitizedPath))
			if i, ok := inputFilesIndex[cmppath]; ok {
				inputFile := inputFiles[i]
				if !inputFile.isWhiteout {
					if inputFile.isDir {
						fmt.Println("Error: path referred to in whiteout file points to directory:", path)
						return false
					}
					inputFile.isWhiteout = true
					totalUncompressed -= inputFile.size
				}
				continue
			}

			pathspl := strings.Split(sanitizedPath, sep)
			for i := 1; i < len(pathspl); i++ {
				dirPathspl := pathspl[:i]
				dirCmppath := strings.ToLower(strings.Join(dirPathspl, "/")) + "/"
				if j, ok := inputFilesIndex[dirCmppath]; ok && inputFiles[j].isDir {
					pathspl[i-1] = inputFiles[j].pathspl[i-1]
				} else {
					nDirs++
					inputFilesIndex[dirCmppath] = len(inputFiles)
					inputFiles = append(inputFiles, &filebaseEntry{cmppath: dirCmppath, pathspl: dirPathspl, isDir: true})
				}
			}

			nFiles++
			inputFilesIndex[cmppath] = len(inputFiles)
			inputFiles = append(inputFiles, &filebaseEntry{cmppath: cmppath, pathspl: pathspl, isWhiteout: true})
		}
	}

	sort.Slice(inputFiles, func(i, j int) bool { return inputFiles[i].cmppath < inputFiles[j].cmppath })

	if bar != nil {
		bar.Clear()
	}

	fmt.Println(
		"Found",
		humanize.Comma(nFiles),
		english.PluralWord(int(nFiles), "file", ""),
		"and",
		humanize.Comma(nDirs),
		english.PluralWord(int(nDirs), "directory", ""),
		"in",
		humanize.IBytes(uint64(totalUncompressed)),
		"for directory root.",
	)

	var grfTotalUncompressed, grfNFiles, grfNSymlinks int64
	var grfInputFiles []*filebaseEntry
	var fbar filebaseGRF
	if grfFilePath != "" {
		if bar != nil {
			bar.Describe("Listing GRF contents")
		} else {
			fmt.Println("Listing GRF contents...")
		}

		if grfIsDir {
			grfRelPath := grfFilePath
			if filepath.Base(grfFilePath) == "data" { // pragmatic case
				grfRelPath = filepath.Dir(grfFilePath)
			}
			fbar = newVirtualGRF(grfRelPath, hasher)
			existingFiles := make(map[string]int64)
			err := filepath.WalkDir(grfFilePath, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				select {
				case <-sc:
					return context.Canceled
				default:
				}
				path, err = filepath.Rel(grfRelPath, path)
				if err != nil {
					return err
				}
				path = filepath.ToSlash(path)
				sanitizedPath := path
				if !koreanFilenames {
					sanitizedPath, err = grf.SanitizeFileName(path)
					if err != nil {
						return fmt.Errorf("problematic file name in GRF: %s - %w", path, err)
					}
				}
				cmppath := strings.ToLower(sanitizedPath)
				pathspl := strings.Split(sanitizedPath, "/")
				basename := pathspl[len(pathspl)-1]
				if basename != "symlinks" && basename != ".gitignore" && !d.IsDir() {
					fi, err := d.Info()
					if err != nil {
						return err
					}
					grfNFiles++
					size := fi.Size()
					existingFiles[sanitizedPath] = size
					grfInputFiles = append(grfInputFiles, &filebaseEntry{realpath: path, cmppath: cmppath, pathspl: pathspl, size: size})
					grfTotalUncompressed += size
				} else if basename == ".git" && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			})
			if err != nil {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				if err == context.Canceled {
					fmt.Println("Cancelled.")
				} else {
					fmt.Println("Error while reading GRF folder:", err)
				}
				return false
			}
			slf, err := grf.OpenSymlinks(grfFilePath)
			if err != nil {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				fmt.Println("Error while reading symlinks file:", err)
				return false
			}
			for name, target := range slf.GetAll() {
				sanitizedName := name
				sanitizedTarget := target
				if !koreanFilenames {
					sanitizedName, err = grf.SanitizeFileName(name)
					if err != nil {
						fmt.Printf("Error while listing symlinks: source file name \"%s\" could not be sanitized\n", name)
						return false
					}
					sanitizedTarget, err = grf.SanitizeFileName(target)
					if err != nil {
						fmt.Printf("Error while listing symlinks: target file name \"%s\" could not be sanitized\n", target)
						return false
					}
				}
				size, ok := existingFiles[sanitizedTarget]
				if !ok {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Printf("Error while listing symlinks: target file \"%s\" does not exist\n", target)
					return false
				}
				grfNSymlinks++
				grfInputFiles = append(grfInputFiles, &filebaseEntry{
					realpath:  target,
					cmppath:   sanitizedName,
					pathspl:   strings.Split(sanitizedName, "/"),
					size:      size,
					isSymlink: true,
				})
			}
		} else {
			ar, err := grf.Open(grfFilePath, true, true)
			fbar = ar
			if err != nil {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				fmt.Println("Error while reading GRF file:", err)
				return false
			}
			defer ar.Close()
			for _, name := range ar.AllFileNames() {
				h := ar.Head(name)
				cmppath := strings.ToLower(name)
				pathspl := strings.Split(name, "/")
				if h.Linkname == "" {
					grfNFiles++
					grfInputFiles = append(grfInputFiles, &filebaseEntry{realpath: name, cmppath: cmppath, pathspl: pathspl, size: int64(h.Size)})
					grfTotalUncompressed += int64(h.Size)
				} else {
					grfNSymlinks++
					grfInputFiles = append(grfInputFiles, &filebaseEntry{
						realpath:  h.Linkname,
						cmppath:   cmppath,
						pathspl:   pathspl,
						size:      int64(h.Size),
						isSymlink: true,
					})
				}
			}
		}

		sort.Slice(grfInputFiles, func(i, j int) bool { return grfInputFiles[i].cmppath < grfInputFiles[j].cmppath })

		if bar != nil {
			bar.Clear()
		}

		fmt.Println(
			"Found",
			humanize.Comma(grfNFiles),
			english.PluralWord(int(grfNFiles), "file", ""),
			"and",
			humanize.Comma(grfNSymlinks),
			english.PluralWord(int(grfNSymlinks), "symbolic link", ""),
			"in",
			humanize.IBytes(uint64(grfTotalUncompressed)),
			"for GRF.",
		)
	}

	fbw := newFilebaseBuilder()

	var h grf.FileHeaderHash

	if nFiles > 0 {
		if bar != nil {
			bar.Describe("Building for root")
			bar.ChangeMax64(totalUncompressed + grfTotalUncompressed)
		} else {
			fmt.Println("Building for root...")
		}
		err := fbw.writeRootContents()
		if err != nil {
			if bar != nil {
				bar.Exit()
				fmt.Println()
			}
			fmt.Println("Error while writing to filebase:", err)
			return false
		}
		bufo := &bytes.Buffer{}
		var ell io.Writer
		if writeContent {
			ell = bufo
		} else {
			ell = io.Discard
		}

		for _, e := range inputFiles {
			err = fbw.writePath(e.pathspl)
			if err != nil {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				fmt.Println("Error while writing path in filebase:", err)
				return false
			}

			if e.isDir {
				fbw.writeEmpty()
				continue
			} else if e.isWhiteout {
				fbw.writeWhiteout()
				continue
			}

			r, err := os.Open(e.realpath)
			if err != nil {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				fmt.Printf("Error while opening file \"%s\": %s\n", e.realpath, err)
				return false
			}
			hasher.Reset()
			_, err = io.Copy(ell, io.TeeReader(r, hasher))
			r.Close()
			if err != nil {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				fmt.Printf("Error while reading file \"%s\": %s\n", e.realpath, err)
				return false
			}

			h.SetWithHasher(hasher)

			if writeContent {
				hasher.Reset()
				err = e.writeContentIfNeeded(contentFolderPath, h, hasher, bufo.Bytes(), compress)
				if err != nil {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Printf("Error while writing content \"%s\": %s\n", h.Hex(), err)
					return false
				}
				if imap != nil {
					imap.add(hasher, h)
				}
				bufo.Reset()
			}

			if bar != nil {
				bar.Add64(e.size)
			}
			fbw.writeHashAndSize(h, e.size)
		}
	}

	if grfNFiles > 0 {
		if bar != nil {
			bar.Describe("Building for GRF")
			bar.ChangeMax64(totalUncompressed + grfTotalUncompressed)
		} else {
			fmt.Println("Building for GRF...")
		}
		var err error
		if grfNameOverride != "" {
			err = fbw.writeGRFName(grfNameOverride)
		}
		if err == nil {
			err = fbw.writeGRFContents()
		}
		if err != nil {
			if bar != nil {
				bar.Exit()
				fmt.Println()
			}
			fmt.Println("Error while writing to filebase:", err)
			return false
		}
		bufo := &bytes.Buffer{}

		for _, e := range grfInputFiles {
			err = fbw.writePath(e.pathspl)
			if err != nil {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				fmt.Println("Error while writing path in filebase:", err)
				return false
			}

			if writeContent && !e.isSymlink {
				_, err = fbar.Get(e.realpath, bufo)
				if err != nil {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Printf("Error while reading file \"%s\": %s\n", e.realpath, err)
					return false
				}
				h, err = fbar.Hash(e.realpath)
				if err != nil {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Printf("Error while retrieving hash of \"%s\": %s\n", e.realpath, err)
					return false
				}
				hasher.Reset()
				err = e.writeContentIfNeeded(contentFolderPath, h, hasher, bufo.Bytes(), compress)
				if err != nil {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Printf("Error while writing content \"%s\": %s\n", h.Hex(), err)
					return false
				}
				if imap != nil {
					imap.add(hasher, h)
				}
				bufo.Reset()
				if bar != nil {
					bar.Add64(e.size)
				}
			} else {
				h, err = fbar.Hash(e.realpath)
				if err != nil {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Printf("Error while reading file \"%s\": %s\n", e.realpath, err)
					return false
				}
			}

			fbw.writeHashAndSize(h, e.size)
		}
	}

	if bar != nil {
		bar.Describe("Writing filebase")
	} else {
		fmt.Println("Writing filebase...")
	}

	finalMessage := fbw.save(filebaseFilePath)
	if finalMessage == "All done." && imap != nil {
		if bar != nil {
			bar.Describe("Writing integrity map")
		} else {
			fmt.Println("Writing integrity map...")
		}
		finalMessage = imap.save(imapPath)
	}

	if bar != nil {
		bar.Finish()
		bar.Exit()
		fmt.Println()
	}
	fmt.Println(finalMessage)
	return finalMessage == "All done."
}

func runContentcheck(contentFolderPath string) {
	hasher := blake3.New(32, nil)
	err := filepath.WalkDir(contentFolderPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		r, err := os.Open(path)
		if err != nil {
			return err
		}
		zr, err := zlib.NewReader(r)
		if err != nil {
			r.Close()
			return err
		}
		hasher.Reset()
		size, err := io.Copy(hasher, zr)
		r.Close()
		expected := fmt.Sprintf("%s-%08x.zl", hex.EncodeToString(hasher.Sum(nil)), size)
		if err != nil {
			fmt.Println(path, "- error:", err)
			err = os.Remove(path)
		} else if expected != filepath.Base(path) {
			fmt.Println(path, "!=", expected)
			err = os.Remove(path)
		}
		return err
	})
	if err != nil {
		fmt.Println("Error:", err)
	} else {
		fmt.Println("All done.")
	}
}
