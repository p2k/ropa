//
//  cmd_checksum.go
//  ropa
//
//  Created by p2k on 15.04.2023
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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"ropa/grf"

	"github.com/schollz/progressbar/v3"
)

func runChecksum(grfFilePath, checksumFilePath string, streamHash, tryFix, koreanFilenames, quiet bool) bool {
	ar, err := grf.Open(grfFilePath, !tryFix, koreanFilenames)
	if err != nil {
		fmt.Println("Error while opening GRF file:", err)
		return false
	}
	defer ar.Close()
	checksumFile, err := os.Create(checksumFilePath)
	if err != nil {
		fmt.Println("Error while creating checksum file:", err)
		return false
	}

	var bar *progressbar.ProgressBar
	if !quiet {
		bar = progressbar.NewOptions64(
			ar.TotalUncompressedSize(),
			progressbar.OptionSetDescription("Calculating"),
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
		fmt.Println("Calculating...")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt)
	defer signal.Stop(sc)
	pc := make(chan int64)
	defer close(pc)
	go func() {
		for {
			select {
			case <-sc:
				cancel()
			case <-ctx.Done():
				return
			case p, ok := <-pc:
				if !ok {
					return
				}
				if bar != nil {
					bar.Set64(p)
				}
			}
		}
	}()

	hashes, err := ar.HashAll(streamHash, ctx, pc)

	if bar != nil {
		bar.Describe("Writing checksum file")
	} else {
		fmt.Println("Writing checksum file...")
	}
	for name, hash := range hashes {
		checksumFile.WriteString(hash.Hex())
		checksumFile.WriteString("  ")
		checksumFile.WriteString(name)
		checksumFile.WriteString("\n")
	}
	checksumFile.Close()

	if bar != nil {
		bar.Finish()
		bar.Exit()
		fmt.Println()
	}

	if ierr, ok := err.(*grf.ArchiveIntegrityError); ok {
		if tryFix {
			fmt.Println("Attempting to fix errors...")
			var resolved []string
			for n, e := range ierr.FileErrors {
				if e == io.ErrUnexpectedEOF {
					buf := &bytes.Buffer{}
					h := ar.Head(n)
					ar.Get(n, buf)
					l := buf.Len()
					if l == 0 {
						fmt.Printf("%s ==> File could not be read at all; removing...\n", n)
						ar.Delete(n)
					} else {
						fmt.Printf("%s ==> File is truncated (expected %d, got %d); writing rest...\n", n, h.Size, l)
						ar.Put(n, buf, 9)
					}
					resolved = append(resolved, n)
					continue
				}
				if ae, ok := e.(*grf.ArchiveError); ok {
					if ae.Error() == "uncompressed size mismatch" {
						fmt.Printf("%s ==> File not marked as compressed, but has size mismatch; removing...\n", n)
						ar.Delete(n)
						resolved = append(resolved, n)
						continue
					}
				}

				fmt.Printf("%s ==> %v\n", n, e)
				if rf, ferr := ar.OpenRaw(n); ferr == nil {
					if data, rerr := io.ReadAll(rf); rerr == nil {
						fmt.Println(data)
					}
				}
			}
			for _, n := range resolved {
				delete(ierr.FileErrors, n)
			}
		} else {
			for n, e := range ierr.FileErrors {
				fmt.Printf("%s ==> %v\n", n, e)
			}
		}

		if len(ierr.FileErrors) == 0 {
			err = nil
		} else {
			//lint:ignore ST1005 used in message for the user
			err = fmt.Errorf("Found %d files with errors", len(ierr.FileErrors))
		}
	}

	if err != nil {
		if err == context.Canceled {
			fmt.Println("Cancelled.")
		} else {
			fmt.Println("Error:", err)
		}
		return false
	}
	fmt.Println("All done.")
	return true
}
