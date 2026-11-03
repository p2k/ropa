//
//  cmd_recompress.go
//  ropa
//
//  Created by p2k on 14.10.2023
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
	"ropa/grf"
	"runtime"

	"github.com/schollz/progressbar/v3"
)

func runRecompress(inputFilePath, outputFilePath, keyFilePath string, level int, pipeline, quiet bool) bool {
	srcGrf, err := grf.Open(inputFilePath, true, true)
	if err != nil {
		fmt.Println("Error while opening GRF file:", err)
		return false
	}
	defer srcGrf.Close()

	if !loadSetEncryptionKeyWithFile(keyFilePath, false, srcGrf) {
		return false
	}

	dstGrf, err := grf.Create(outputFilePath, true)
	if err != nil {
		fmt.Println("Error while creating GRF file:", err)
		return false
	}
	defer dstGrf.Close()

	if !loadSetEncryptionKeyWithFile(keyFilePath, true, dstGrf) {
		return false
	}

	totalUncompressed := srcGrf.TotalUncompressedSize()

	var bar *progressbar.ProgressBar
	if !quiet {
		bar = progressbar.NewOptions64(
			totalUncompressed,
			progressbar.OptionSetDescription("Recompressing"),
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
		fmt.Println("Recompressing...")
	}

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt)
	defer signal.Stop(sc)

	if pipeline {
		dstp := grf.NewPipeline(dstGrf, runtime.NumCPU(), level)
		var pc chan int64
		if bar != nil {
			pc = make(chan int64)
			go func() {
				for p := range pc {
					bar.Set64(p)
				}
			}()
			dstp.SetProgressCh(pc)
			defer close(pc)
		}

		for _, name := range srcGrf.AllFileNames() {
			h := srcGrf.Head(name)
			if h.Linkname == "" {
				f, err := srcGrf.Open(name)
				if err != nil {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Printf("Error while opening file in GRF \"%s\": %s\n", name, err)
					dstp.Wait()
					return false
				}
				ok := dstp.Put(name, f, true)
				f.Close()
				if !ok {
					break
				}
			} else {
				if !dstp.Link(name, h.Linkname) {
					break
				}
			}
		}

		err = dstp.Wait()
		if err != nil {
			if bar != nil {
				bar.Exit()
				fmt.Println()
			}
			fmt.Println("Error while writing files to GRF:", err)
			return false
		}
	} else {
		for _, name := range srcGrf.AllFileNames() {
			h := srcGrf.Head(name)
			if h.Linkname == "" {
				f, err := srcGrf.Open(name)
				if err != nil {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Printf("Error while opening file in GRF \"%s\": %s\n", name, err)
					return false
				}
				h, err = dstGrf.Put(name, f, level)
				if bar != nil {
					bar.Add64(int64(h.Size))
				}
				f.Close()
				if err != nil {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Printf("Error while writing file \"%s\": %s\n", name, err)
				}
			} else {
				_, err = dstGrf.Link(name, h.Linkname)
				if err != nil {
					if bar != nil {
						bar.Exit()
						fmt.Println()
					}
					fmt.Printf("Error while linking file \"%s\": %s\n", name, err)
					return false
				}
			}
		}
	}

	err = dstGrf.Close()
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
