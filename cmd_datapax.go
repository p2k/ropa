//
//  cmd_datapax.go
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
	"archive/tar"
	"fmt"
	"os"
	"os/signal"
	"ropa/grf"

	"github.com/andybalholm/brotli"
	"github.com/schollz/progressbar/v3"
)

func runDatapax(grfFilePath, paxFilePath string, quiet bool) bool {
	ar, err := grf.Open(grfFilePath, true)
	if err != nil {
		fmt.Println("Error while opening GRF file:", err)
		return false
	}
	defer ar.Close()
	paxFile, err := os.Create(paxFilePath)
	if err != nil {
		fmt.Println("Error while creating pax file:", err)
		return false
	}
	defer paxFile.Close()
	brw := brotli.NewWriterLevel(paxFile, brotli.BestCompression)
	defer brw.Close()
	tw := tar.NewWriter(brw)
	defer tw.Close()

	totalUncompressed := ar.TotalUncompressedSize()

	var bar *progressbar.ProgressBar
	if !quiet {
		bar = progressbar.NewOptions64(
			totalUncompressed,
			progressbar.OptionSetDescription("Building"),
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
		fmt.Println("Building...")
	}

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt)
	defer signal.Stop(sc)

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

		h1 := ar.Head(name)
		h2 := &tar.Header{Name: name, Mode: 0644, Format: tar.FormatPAX}
		if h1.Linkname != "" {
			h2.Typeflag = tar.TypeSymlink
			h2.Linkname = h1.Linkname
		} else {
			h2.Typeflag = tar.TypeReg
			h2.Size = int64(h1.Size)
		}
		if h1.Flags != grf.FileFlag {
			h2.PAXRecords = make(map[string]string)
			h2.PAXRecords["GRF.flags"] = string(h1.Flags)
		}
		err = tw.WriteHeader(h2)
		if err != nil {
			if bar != nil {
				bar.Exit()
				fmt.Println()
			}
			fmt.Println("Error while writing pax header:", err)
			return false
		}
		if h1.Linkname == "" {
			copied, err := ar.Get(name, tw)
			if err != nil {
				if bar != nil {
					bar.Exit()
					fmt.Println()
				}
				fmt.Printf("Error while copying \"%s\": %s\n", name, err)
				return false
			}
			if bar != nil {
				bar.Add64(copied)
			}
		} else {
			totalUncompressed -= int64(h1.Size)
			if bar != nil {
				bar.ChangeMax64(totalUncompressed)
			}
		}
	}

	if bar != nil {
		bar.Finish()
		bar.Exit()
		fmt.Println()
	}
	fmt.Println("All done.")
	return true
}
