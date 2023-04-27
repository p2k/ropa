//
//  cmd_verify.go
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
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"ropa/grf"
	"strings"

	"github.com/schollz/progressbar/v3"
)

func runVerify(grfFilePath, checksumFilePath string, quiet bool) bool {
	ar, err := grf.Open(grfFilePath, true)
	if err != nil {
		fmt.Println("Error while opening GRF file:", err)
		return false
	}
	defer ar.Close()

	var bar *progressbar.ProgressBar
	if !quiet {
		bar = progressbar.NewOptions64(
			ar.TotalUncompressedSize(),
			progressbar.OptionSetDescription("Reading checksum file"),
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
		fmt.Println("Reading checksum file...")
	}

	checksumFile, err := os.Open(checksumFilePath)
	if err != nil {
		fmt.Println("Error while opening checksum file:", err)
		return false
	}

	hashMap := make(map[string]grf.FileHeaderHash)
	scanner := bufio.NewScanner(checksumFile)
	for scanner.Scan() {
		spl := strings.SplitN(scanner.Text(), "  ", 2)
		if len(spl) == 2 {
			if len(spl[0]) == 64 {
				hashMap[spl[1]] = grf.FileHeaderHashFromHex(spl[0])
			}
		}
	}
	checksumFile.Close()

	if bar != nil {
		bar.Describe("Verifying")
	} else {
		fmt.Println("Verifying...")
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

	failedFiles, err := ar.VerifyAll(ctx, pc, hashMap)

	if bar != nil {
		bar.Finish()
		bar.Exit()
		fmt.Println()
	}
	if err != nil {
		if err == context.Canceled {
			fmt.Println("Cancelled.")
			return false
		} else {
			fmt.Println("Error:", err)
		}
	}
	if len(failedFiles) > 0 {
		fmt.Println("Failed files:")
		for _, name := range failedFiles {
			fmt.Println(name)
		}
	} else {
		fmt.Println("Verify ok.")
	}
	return err == nil && len(failedFiles) == 0
}
