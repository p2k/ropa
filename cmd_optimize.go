//
//  cmd_optimize.go
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
	"context"
	"fmt"
	"os"
	"os/signal"
	"ropa/grf"

	"github.com/schollz/progressbar/v3"
)

func runOptimize(grfFilePath string, dedup, sort, defrag, quiet bool) bool {
	ar, err := grf.Open(grfFilePath, false)
	if err != nil {
		fmt.Println("Error while opening GRF file:", err)
		return false
	}
	defer ar.Close()
	if !dedup && !sort && !defrag {
		fmt.Println("Nothing to do.")
		return true
	}

	var bar *progressbar.ProgressBar
	var barOptions []progressbar.Option
	if !quiet {
		var initMax int64
		if dedup {
			initMax = ar.TotalUncompressedSize()
		} else {
			initMax = ar.TotalCompressedSize()
		}
		barOptions = []progressbar.Option{
			progressbar.OptionFullWidth(),
			progressbar.OptionShowBytes(true),
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        "=",
				SaucerHead:    ">",
				SaucerPadding: " ",
				BarStart:      "[",
				BarEnd:        "]",
			}),
		}
		bar = progressbar.NewOptions64(initMax, barOptions...)
	}

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt)
	defer signal.Stop(sc)
	pc := make(chan int64)

	if dedup {
		if bar != nil {
			bar.Describe("Deduplicating")
			bar.RenderBlank()
		} else {
			fmt.Println("Deduplicating...")
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
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
		_, err = ar.Dedup(ctx, pc)
		close(pc)
		if err != nil {
			if bar != nil {
				bar.Exit()
				fmt.Println()
			}
			if err == context.Canceled {
				fmt.Println("Cancelled.")
			} else {
				fmt.Println("Error during deduplication:", err)
				fmt.Println("See the \"checksum\" subcommand if you want to attempt repairing a damaged file.")
			}
			return false
		}
		if defrag {
			pc = make(chan int64)
			if bar != nil {
				bar.Clear()
				bar = progressbar.NewOptions64(ar.TotalCompressedSize(), barOptions...)
			}
		}
	}

	if sort {
		if bar != nil {
			bar.Describe("Sorting")
			bar.RenderBlank()
		} else {
			fmt.Println("Sorting...")
		}
		ar.Sort()

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
	}

	if defrag {
		if bar != nil {
			bar.Describe("Defragmenting")
			bar.RenderBlank()
		} else {
			fmt.Println("Defragmenting...")
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
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
		err = ar.Defragment(ctx, pc)
		close(pc)
		if err != nil {
			if bar != nil {
				bar.Exit()
				fmt.Println()
			}
			if err == context.Canceled {
				fmt.Println("Cancelled.")
			} else {
				fmt.Println("Error during defragmentation:", err)
				fmt.Println("See the \"checksum\" subcommand if you want to attempt repairing a damaged file. Note that defragmentation will not (further) damage a broken file.")
			}
			return false
		}
	}

	if bar != nil {
		bar.Exit()
		fmt.Println()
	}
	err = ar.Close()
	if err != nil {
		fmt.Println("Error while finalizing:", err)
		return false
	}
	fmt.Println("All done.")
	return true
}
