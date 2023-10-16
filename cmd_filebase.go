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
	"compress/zlib"
	"context"
	"encoding/hex"
	"encoding/json"
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
	"golang.org/x/crypto/blake2b"
)

type countingReader struct {
	r io.Reader
	n int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += n
	return n, err
}

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
	b        *bufio.Reader
	t        io.Reader
	hashMap  map[string]grf.FileHeaderHash
}

func newVirtualGRF(basePath string, hasher hash.Hash) *virtualGRF {
	b := bufio.NewReader(nil)
	t := io.TeeReader(b, hasher)
	return &virtualGRF{basePath: basePath, h: hasher, b: b, t: t, hashMap: make(map[string]grf.FileHeaderHash)}
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
	g.b.Reset(r)
	_, err = io.Copy(g.h, g.b)
	r.Close()
	if err != nil {
		g.h.Reset()
		return h, err
	}
	copy(h[:], g.h.Sum(nil))
	g.h.Reset()
	g.hashMap[name] = h

	return h, nil
}

func (g *virtualGRF) Get(name string, w io.Writer) (int64, error) {
	r, err := os.Open(filepath.Join(g.basePath, filepath.FromSlash(name)))
	if err != nil {
		return 0, err
	}
	g.b.Reset(r)
	n, err := io.Copy(w, g.t)
	r.Close()
	if err != nil {
		g.h.Reset()
		return 0, err
	}
	var h grf.FileHeaderHash
	copy(h[:], g.h.Sum(nil))
	g.h.Reset()
	g.hashMap[name] = h

	return n, nil
}

type filebaseGRF interface {
	Hash(name string) (grf.FileHeaderHash, error)
	Get(name string, w io.Writer) (int64, error)
}

func (e *filebaseEntry) writeContentIfNeeded(contentFolderPath string, h grf.FileHeaderHash, d []byte, compress int) error {
	contentPath := filepath.Join(contentFolderPath, fmt.Sprintf("%s%08x", h.Hex(), e.size))
	_, err := os.Stat(contentPath)
	if err != nil {
		if os.IsNotExist(err) {
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
				zc, err = zlib.NewWriterLevel(w, compress)
				if err != nil {
					return err
				}
				_, err = zc.Write(d)
				if err == nil {
					err = zc.Close()
				}
			} else {
				d = zopfli.Zlib(d, compress, d)
				_, err = w.Write(d)
			}
			return err
		} else {
			return err
		}
	}
	return nil
}

type filebaseBuilder struct {
	w       *bytes.Buffer
	indent  [][]byte
	lastspl []string
	sp      []byte
	co      []byte
	lf      []byte
	wo      []byte
	q       *regexp.Regexp
}

func newFilebaseBuilder() *filebaseBuilder {
	w := &filebaseBuilder{
		w:      &bytes.Buffer{},
		indent: make([][]byte, 16),
		sp:     []byte{' '},
		co:     []byte{':'},
		lf:     []byte{'\n'},
		wo:     []byte{' ', '~', '\n'},
		q:      regexp.MustCompile(`^\s|^-\s|^[[{,&*!?|>#%@"']|\s#|:\s|\s$|^\+?\d*(\.\d*)?$`),
	}

	master := make([]byte, 32)
	for i := 0; i < 32; i++ {
		master[i] = ' '
	}
	for i := 0; i < 16; i++ {
		w.indent[i] = master[:(i+1)*2]
	}

	w.w.Write([]byte("type: ROPAFilebase/v1\n"))

	return w
}

func (w *filebaseBuilder) write(filebaseFilePath string) string {
	f, err := os.Create(filebaseFilePath)
	if err != nil {
		return fmt.Sprint("Error while creating filebase file:", err)
	}
	defer f.Close()

	data := w.w.Bytes()
	if strings.HasSuffix(filebaseFilePath, ".gz") {
		data = zopfli.Gzip(data, 15, data)
	}

	_, err = f.Write(data)
	if err != nil {
		return fmt.Sprint("Error while writing filebase file:", err)
	}

	return "All done."
}

func (w *filebaseBuilder) writeQuotedIfNeeded(s string) error {
	if len(s) == 0 || w.q.MatchString(s) {
		b, err := json.Marshal(s)
		if err != nil {
			return err
		}
		_, err = w.w.Write(b)
		return err
	}

	_, err := w.w.Write([]byte(s))
	return err
}

func (w *filebaseBuilder) writePath(pathspl []string) error {
	m := len(w.lastspl)
	n := len(pathspl)
	var err error
	for i := 0; i < n; i++ {
		if i < m && w.lastspl[i] == pathspl[i] {
			continue
		}

		_, err = w.w.Write(w.indent[i])
		if err != nil {
			return err
		}
		err = w.writeQuotedIfNeeded(pathspl[i])
		if err != nil {
			return err
		}
		_, err = w.w.Write(w.co)
		if err != nil {
			return err
		}
		if i+1 < n {
			_, err = w.w.Write(w.lf)
			if err != nil {
				return err
			}
		}
	}

	w.lastspl = pathspl
	return nil
}

func (w *filebaseBuilder) writeEmpty() error {
	_, err := w.w.Write(w.lf)
	return err
}

func (w *filebaseBuilder) writeWhiteout() error {
	_, err := w.w.Write(w.wo)
	return err
}

func (w *filebaseBuilder) writeHashAndSize(h grf.FileHeaderHash, s int64) error {
	_, err := w.w.Write(w.sp)
	if err != nil {
		return err
	}
	_, err = w.w.Write([]byte(fmt.Sprintf("%s%08x", h.Hex(), s)))
	if err != nil {
		return err
	}
	_, err = w.w.Write(w.lf)
	return err
}

func (w *filebaseBuilder) writeRoot() error {
	_, err := w.w.Write([]byte("root:\n"))
	w.lastspl = nil
	return err
}

func (w *filebaseBuilder) writeGRF() error {
	_, err := w.w.Write([]byte("grf:\n"))
	w.lastspl = nil
	return err
}

func runFilebase(inputFilePathsPatterns []string, grfFilePath, filebaseFilePath, whiteoutFilePath, contentFolderPath string, compress int, quiet bool) bool {
	hasher, err := blake2b.New256(nil)
	if err != nil {
		fmt.Println("Error while creating hasher:", err)
		return false
	}

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
				cmppath := filepath.ToSlash(strings.ToLower(path))
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
				pathspl := strings.Split(path, sep)
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

		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			path := scanner.Text()
			cmppath := filepath.ToSlash(strings.ToLower(path))
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

			pathspl := strings.Split(path, sep)
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
		r.Close()
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

		if grfFi.IsDir() {
			fbar = newVirtualGRF(grfFilePath, hasher)
			existingFiles := make(map[string]bool)
			err = filepath.WalkDir(grfFilePath, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				select {
				case <-sc:
					return context.Canceled
				default:
				}
				fi, err := d.Info()
				if err != nil {
					return err
				}
				path, err = filepath.Rel(grfFilePath, path)
				if err != nil {
					return err
				}
				path = filepath.ToSlash(path)
				cmppath := strings.ToLower(path)
				if cmppath != "symlinks" && !d.IsDir() {
					existingFiles[path] = true
					pathspl := strings.Split(path, "/")
					grfNFiles++
					size := fi.Size()
					grfInputFiles = append(grfInputFiles, &filebaseEntry{realpath: path, cmppath: cmppath, pathspl: pathspl, size: size})
					grfTotalUncompressed += size
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
				if !existingFiles[target] {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Printf("Error while listing symlinks: target file \"%s\" does not exist\n", target)
					return false
				}
				grfNSymlinks++
				grfInputFiles = append(grfInputFiles, &filebaseEntry{realpath: target, cmppath: name, pathspl: strings.Split(name, "/"), isSymlink: true})
			}
		} else {
			ar, err := grf.Open(grfFilePath, true)
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
				} else {
					grfNSymlinks++
					grfInputFiles = append(grfInputFiles, &filebaseEntry{realpath: h.Linkname, cmppath: cmppath, pathspl: pathspl, size: int64(h.Size), isSymlink: true})
				}
				grfTotalUncompressed += int64(h.Size)
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
		err = fbw.writeRoot()
		if err != nil {
			if bar != nil {
				bar.Exit()
				fmt.Println()
			}
			fmt.Println("Error while writing to filebase:", err)
			return false
		}
		bufi := bufio.NewReader(nil)
		tee := io.TeeReader(bufi, hasher)
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
			bufi.Reset(r)
			_, err = io.Copy(ell, tee)
			r.Close()
			if err != nil {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				fmt.Printf("Error while reading file \"%s\": %s\n", e.realpath, err)
				return false
			}

			copy(h[:], hasher.Sum(nil))
			hasher.Reset()

			if writeContent {
				err = e.writeContentIfNeeded(contentFolderPath, h, bufo.Bytes(), compress)
				if err != nil {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Printf("Error while writing content \"%s\": %s\n", h.Hex(), err)
					return false
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
		err = fbw.writeGRF()
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
				e.writeContentIfNeeded(contentFolderPath, h, bufo.Bytes(), compress)
				if err != nil {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Printf("Error while writing content \"%s\": %s\n", h.Hex(), err)
					return false
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

	finalMessage := fbw.write(filebaseFilePath)

	if bar != nil {
		bar.Finish()
		bar.Exit()
		fmt.Println()
	}
	fmt.Println(finalMessage)
	return finalMessage == "All done."
}

func runContentcheck(contentFolderPath string) {
	hasher, err := blake2b.New256(nil)
	if err == nil {
		err = filepath.WalkDir(contentFolderPath, func(path string, d fs.DirEntry, err error) error {
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
			cr := &countingReader{r: zr}
			_, err = io.Copy(hasher, cr)
			r.Close()
			expected := fmt.Sprintf("%s%08x", hex.EncodeToString(hasher.Sum(nil)), cr.n)
			if err != nil {
				fmt.Println(path, "- error:", err)
				err = os.Remove(path)
			} else if expected != filepath.Base(path) {
				fmt.Println(path, "!=", expected)
				err = os.Remove(path)
			}
			hasher.Reset()
			return err
		})
	}
	if err != nil {
		fmt.Println("Error:", err)
	} else {
		fmt.Println("All done.")
	}
}
