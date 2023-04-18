//
//  cmd_extract.go
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

package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"ropa/grf"
	"strings"

	"github.com/schollz/progressbar/v3"
)

func runExtract(grfFilePath, outputFolderPath string, ignoreErrors, quiet bool) bool {
	ar, err := grf.Open(grfFilePath, false)
	if err != nil {
		fmt.Println("Error while opening GRF file:", err)
		return false
	}
	defer ar.Close()

	ofi, err := os.Stat(outputFolderPath)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Println("Error while inspecting output folder path:", err)
			return false
		}
		err = os.MkdirAll(outputFolderPath, 0755)
		if err != nil {
			fmt.Println("Error while creating output folder:", err)
			return false
		}
	} else if !ofi.IsDir() {
		fmt.Println("Error: output path is not a folder")
		return false
	}

	slf, err := grf.OpenSymlinks(outputFolderPath)
	if err != nil {
		fmt.Println("Error while setting up symlinks file:", err)
		return false
	}
	defer slf.Write()

	var bar *progressbar.ProgressBar
	if !quiet {
		bar = progressbar.NewOptions64(
			ar.TotalUncompressedSize(),
			progressbar.OptionSetDescription("Extracting"),
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
		fmt.Println("Extracting...")
	}

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt)
	defer signal.Stop(sc)

	dirCache := make(map[string]bool)
	dirCache[outputFolderPath] = true
	var allErrors []string
	for _, name := range ar.AllFileNames() {
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

		// Symlink?
		h := ar.Head(name)
		if h.Linkname != "" {
			slf.Add(name, h.Linkname)
			continue
		}

		fname := filepath.Join(outputFolderPath, filepath.Join(strings.Split(name, "/")...))
		dirname := filepath.Dir(fname)
		if !dirCache[dirname] {
			_, err := os.Stat(dirname)
			if os.IsNotExist(err) {
				err = os.MkdirAll(dirname, 0755)
				if err != nil {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Printf("Error during mkdir \"%s\": %s\n", dirname, err)
					return false
				}
			} else if err != nil {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				fmt.Printf("Error, cannot stat \"%s\": %s\n", dirname, err)
				return false
			}
			dirCache[dirname] = true
		}

		w, err := os.Create(fname)
		if err != nil {
			if bar != nil {
				bar.Exit()
				fmt.Println()
			}
			fmt.Printf("Error while creating \"%s\": %s\n", fname, err)
			return false
		}
		written, err := ar.Get(name, w)
		w.Close()
		if err != nil {
			if !ignoreErrors {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				fmt.Printf("Error while extracting \"%s\": %s\n", fname, err)
				return false
			} else {
				allErrors = append(allErrors, fmt.Sprintf("Error while extracting \"%s\": %s", fname, err))
			}
		}
		if bar != nil {
			bar.Add64(written)
		}
	}

	if bar != nil {
		bar.Finish()
		bar.Exit()
		fmt.Println()
	}

	for _, e := range allErrors {
		fmt.Println(e)
	}

	fmt.Println("All done.")
	return true
}
