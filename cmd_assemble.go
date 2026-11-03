//
//  cmd_assemble.go
//  ropa
//
//  Created by p2k on 02.01.2024
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
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"ropa/grf"
	"strings"

	"github.com/schollz/progressbar/v3"
)

func unquoteIfNeeded(q string) string {
	if len(q) < 2 || q[0] != '"' {
		return q
	}
	var u string
	if err := json.Unmarshal([]byte(q), &u); err != nil {
		return q
	}
	return u
}

// runAssemble creates a GRF from the grf_contents section of a filebase,
// copying the stored blobs without recompressing them.
func runAssemble(grfFilePath, filebasePath, contentDirPath, keyFilePath string, quiet bool) bool {
	fb, err := os.Open(filebasePath)
	if err != nil {
		fmt.Println("Error while opening filebase:", err)
		return false
	}
	defer fb.Close()
	var fbr io.ReadCloser
	if strings.HasSuffix(filebasePath, ".gz") {
		fbr, err = gzip.NewReader(fb)
		if err != nil {
			fmt.Println("Error while opening gzip stream of filebase:", err)
			return false
		}
		defer fbr.Close()
	} else {
		fbr = fb
	}
	rd, err := os.ReadDir(contentDirPath)
	if err != nil {
		fmt.Println("Error while listing content directory:", err)
		return false
	}
	contentSizeMap := make(map[string]int64)
	for _, e := range rd {
		if fi, err := e.Info(); err == nil && !fi.IsDir() {
			contentSizeMap[fi.Name()] = fi.Size()
		}
	}
	ar, err := grf.Create(grfFilePath, true)
	if err != nil {
		fmt.Println("Error while creating grf file:", err)
		return false
	}
	defer ar.Close()

	if !loadSetEncryptionKeyWithFile(keyFilePath, true, ar) {
		return false
	}

	var totalSize int64
	gotType := false
	inGRFContents := false
	scanner := bufio.NewScanner(fbr)
	var pathStack []string
	var contentPaths []string
	var contentFilenames []string
	contentLinks := make(map[string]string)
	for scanner.Scan() {
		t := scanner.Text()
		if !inGRFContents {
			if strings.HasPrefix(t, "type:") {
				if t == "type: ROPAFilebase/v1" {
					gotType = true
				} else {
					fmt.Println("Invalid filebase type")
					return false
				}
			} else if gotType && !inGRFContents && t == "grf_contents:" {
				inGRFContents = true
			}
		} else {
			indent := -2
			for _, r := range t {
				if r == ' ' {
					indent++
				} else {
					break
				}
			}
			if indent == -2 {
				break
			}
			if indent>>1 < len(pathStack) {
				pathStack = pathStack[:(indent >> 1)]
			}
			sc := strings.IndexRune(t, ':')
			if sc == len(t)-1 {
				pathStack = append(pathStack, unquoteIfNeeded(t[indent+2:len(t)-1]))
			} else if len(t)-sc == 75 { // ": " + 64 hex digits hash + "-" + 8 hex digits size
				pathStack = append(pathStack, unquoteIfNeeded(t[indent+2:sc]))
				path := path.Join(pathStack...)
				filename := t[sc+2:] + ".zl"
				pathStack = pathStack[:len(pathStack)-1]
				contentPaths = append(contentPaths, path)
				contentFilenames = append(contentFilenames, filename)
				if _, ok := contentLinks[filename]; !ok {
					if size, ok := contentSizeMap[filename]; ok {
						contentLinks[filename] = path
						totalSize += size
					} else {
						fmt.Println("Content file not found:", filename)
						return false
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Println("Error while reading filebase:", err)
		return false
	}

	var bar *progressbar.ProgressBar
	if !quiet {
		bar = progressbar.NewOptions64(
			totalSize,
			progressbar.OptionSetDescription("Assembling"),
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
		fmt.Println("Assembling...")
	}

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt)
	defer signal.Stop(sc)

	for i, path := range contentPaths {
		select {
		case <-sc:
			if bar != nil {
				bar.Exit()
				fmt.Println()
			}
			fmt.Println("Cancelled.")
			return false
		default:
		}

		filename := contentFilenames[i]
		// The first path using a blob stores it, all others link to that path
		if link, ok := contentLinks[filename]; ok && link != path {
			ar.Link(path, link)
		} else {
			var uncompressedSize uint32
			// Skip the hash and "-", drop ".zl"
			fmt.Sscanf(filename[65:len(filename)-3], "%08x", &uncompressedSize)
			fp := filepath.Join(contentDirPath, filename)
			f, err := os.Open(fp)
			if err != nil {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				fmt.Printf("Error while opening content file \"%s\": %s\n", fp, err)
				return false
			}
			h, err := ar.RawPut(path, f, uncompressedSize, grf.FileFlag)
			f.Close()
			if err != nil {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				fmt.Printf("Error while assembling file \"%s\": %s\n", fp, err)
				return false
			}
			if bar != nil {
				bar.Add64(int64(h.CompressedSize))
			}
		}
	}

	err = ar.Close()
	if err != nil {
		if bar != nil {
			bar.Exit()
			fmt.Println()
		}
		fmt.Println("Error while finalizing GRF:", err)
		return false
	}

	if bar != nil {
		bar.Finish()
		bar.Exit()
		fmt.Println()
	}
	fmt.Println("All done.")
	return true
}
