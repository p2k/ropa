//
//  cmd_update.go
//  ropa
//
//  Created by p2k on 23.04.2023
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
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"ropa/grf"
	"runtime"
	"sort"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/dustin/go-humanize/english"
	"github.com/schollz/progressbar/v3"
)

type updateEntry struct {
	realpath  string
	cmppath   string
	isSymlink bool
	linkpath  string
}

func runUpdate(grfFilePath string, inputPatterns []string, pipeline, quiet bool) bool {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Println("Error while getting current directory:", err)
		return false
	}

	ar, err := grf.Open(grfFilePath, false)
	if err != nil && os.IsNotExist(err) {
		ar, err = grf.Create(grfFilePath)
	}
	if err != nil {
		fmt.Println("Error while opening GRF file:", err)
		return false
	}
	defer ar.Close()

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

	var totalSize, nFiles, nSymlinks int64
	inputFiles := make([]*updateEntry, 0, len(inputPatterns))
	inputFilesSet := make(map[string]bool)
	for _, pattern := range inputPatterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
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
				if !d.IsDir() && !inputFilesSet[cmppath] && cmppath != "symlinks" {
					fi, err := d.Info()
					if err != nil {
						return err
					}
					nFiles++
					totalSize += fi.Size()
					inputFiles = append(inputFiles, &updateEntry{realpath: path, cmppath: cmppath})
					inputFilesSet[cmppath] = true
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
					fmt.Println("Error while listing files:", err)
				}
				return false
			}
		}
	}

	slf, err := grf.OpenSymlinks(cwd)
	if err != nil {
		if bar != nil {
			bar.Exit()
			fmt.Println()
		}
		fmt.Println("Error while reading symlinks file:", err)
		return false
	}

	if len(inputPatterns) == 1 && inputPatterns[0] == "*" {
		for path, linkpath := range slf.GetAll() {
			cmppath := strings.ToLower(path)
			nSymlinks++
			inputFiles = append(inputFiles, &updateEntry{realpath: path, cmppath: cmppath, isSymlink: true, linkpath: linkpath})
		}
	} else {
		for _, pattern := range inputPatterns {
			matches, err := slf.Glob(filepath.ToSlash(pattern))
			if err != nil {
				fmt.Printf("Match pattern \"%s\" error: %s\n", pattern, err)
				return false
			}
			for _, path := range matches {
				err = fs.WalkDir(slf, path, func(path string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					cmppath := strings.ToLower(path)
					if !d.IsDir() && !inputFilesSet[cmppath] {
						nSymlinks++
						inputFiles = append(inputFiles, &updateEntry{realpath: path, cmppath: cmppath, isSymlink: true, linkpath: slf.Get(path)})
						inputFilesSet[cmppath] = true
					}
					return nil
				})
				if err != nil {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Println("Error while listing symlinks:", err)
					return false
				}
			}
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
		humanize.Comma(nSymlinks),
		english.PluralWord(int(nSymlinks), "symbolic link", ""),
		"in",
		humanize.IBytes(uint64(totalSize)),
		"for updating.",
	)

	if nFiles > 0 {
		if bar != nil {
			bar.Describe("Writing")
			bar.ChangeMax64(totalSize)
		} else {
			fmt.Println("Writing...")
		}

		if pipeline {
			arp := grf.NewPipeline(ar, runtime.NumCPU(), 9)
			var pc chan int64
			if bar != nil {
				pc = make(chan int64)
				go func() {
					for p := range pc {
						bar.Set64(p)
					}
				}()
				arp.SetProgressCh(pc)
			}
			defer close(pc)

			for _, e := range inputFiles {
				if !e.isSymlink {
					f, err := os.Open(e.realpath)
					if err != nil {
						if bar != nil {
							bar.Exit()
							fmt.Println()
						}
						fmt.Printf("Error while opening file \"%s\": %s\n", e.realpath, err)
						arp.Wait()
						return false
					}
					ok := arp.Put(filepath.ToSlash(e.realpath), f, true)
					f.Close()
					if !ok {
						break
					}
				} else {
					if !arp.Link(filepath.ToSlash(e.realpath), e.linkpath) {
						break
					}
				}
			}

			err = arp.Wait()
			if err != nil {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				fmt.Println("Error while writing files to GRF:", err)
				return false
			}
		} else {
			for _, e := range inputFiles {
				if !e.isSymlink {
					f, err := os.Open(e.realpath)
					if err != nil {
						if bar != nil {
							bar.Exit()
							fmt.Println()
						}
						fmt.Printf("Error while opening file \"%s\": %s\n", e.realpath, err)
						return false
					}
					h, err := ar.Put(filepath.ToSlash(e.realpath), f, 9)
					if bar != nil {
						bar.Add64(int64(h.Size))
					}
					f.Close()
					if err != nil {
						if bar != nil {
							bar.Exit()
							fmt.Println()
						}
						fmt.Printf("Error while writing file \"%s\": %s\n", e.realpath, err)
					}
				} else {
					_, err = ar.Link(filepath.ToSlash(e.realpath), e.linkpath)
					if err != nil {
						if bar != nil {
							bar.Exit()
							fmt.Println()
						}
						fmt.Printf("Error while linking file \"%s\": %s\n", e.realpath, err)
						return false
					}
				}
			}
		}
	}

	err = ar.Close()
	if err != nil {
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

func runDelete(grfFilePath string, deletePatterns []string) bool {
	ar, err := grf.Open(grfFilePath, false)
	if err != nil {
		fmt.Println("Error while opening GRF file:", err)
		return false
	}
	defer ar.Close()

	fmt.Println("Listing files...")

	nFiles := 0
	deleteFiles := make([]string, 0, len(deletePatterns))
	deleteFilesSet := make(map[string]bool)
	for _, pattern := range deletePatterns {
		matches, err := ar.Glob(filepath.ToSlash(pattern))
		if err != nil {
			fmt.Printf("Match pattern \"%s\" error: %s\n", pattern, err)
			return false
		}
		for _, path := range matches {
			err = fs.WalkDir(ar, path, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() && !deleteFilesSet[path] {
					nFiles++
					deleteFiles = append(deleteFiles, path)
					deleteFilesSet[path] = true
				}
				return nil
			})
			if err != nil {
				fmt.Println("Error while listing files:", err)
				return false
			}
		}
	}

	for _, path := range deleteFiles {
		ar.Delete(path)
	}

	err = ar.Close()
	if err != nil {
		fmt.Println("Error while finalizing GRF:", err)
		return false
	}

	fmt.Printf("Deleted %s %s.\n", humanize.Comma(int64(nFiles)), english.PluralWord(nFiles, "file", ""))
	return true
}
