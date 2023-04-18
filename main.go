//
//  main.go
//  ropa
//
//  Created by p2k on 21.03.2023
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
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var keep = map[string]bool{
	"data/wav/effect/drop_blue.wav":   true,
	"data/wav/effect/drop_green.wav":  true,
	"data/wav/effect/drop_pink.wav":   true,
	"data/wav/effect/drop_purple.wav": true,
	"data/wav/effect/drop_red.wav":    true,
	"data/wav/effect/drop_yellow.wav": true,
}

func printUsage() {
	fmt.Fprintf(flag.CommandLine.Output(), `Usage: %s <command> [<args>]

   extract    Extract a GRF file
   checksum   Generate checksums for a GRF file's contents
   verify     Verify the contents of a GRF file
   optimize   Optimize a GRF file
   datapax    Build data.pax.xz
   filebase   Build a filebase
   beamsonly  Build the beams-only GRF
`, os.Args[0])
}

func setSubcommandUsage(subcommand *flag.FlagSet, argsText string) {
	if argsText != "" {
		argsText = " " + argsText
	}
	subcommand.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s %s [<options>]%s\n", os.Args[0], subcommand.Name(), argsText)
		subcommand.PrintDefaults()
	}
}

func main() {
	extract := flag.NewFlagSet("extract", flag.ExitOnError)
	setSubcommandUsage(extract, "<grf file>")
	extractOutput := extract.String("output", ".", "Output folder")
	extractIgnore := extract.Bool("ignore", false, "Ignore errors on individual file extractions")
	extractQuiet := extract.Bool("quiet", false, "Don't show progress")

	checksum := flag.NewFlagSet("checksum", flag.ExitOnError)
	setSubcommandUsage(checksum, "<grf file>")
	checksumOutput := checksum.String("output", "", "Output file name (default \"<input file>.b2b\")")
	checkFix := checksum.Bool("fix", false, "Attempt to fix problems; will delete unreadable files and rewrite files with checksum errors")
	checksumQuiet := checksum.Bool("quiet", false, "Don't show progress")

	verify := flag.NewFlagSet("verify", flag.ExitOnError)
	setSubcommandUsage(verify, "<grf file>")
	verifyChecksum := verify.String("checksum", "", "Checksum file name (default \"<input file>.b2b\")")
	verifyQuiet := verify.Bool("quiet", false, "Don't show progress")

	optimize := flag.NewFlagSet("optimize", flag.ExitOnError)
	setSubcommandUsage(optimize, "<grf file>")
	optimizeDedup := optimize.Bool("dedup", false, "Deduplication")
	optimizeSort := optimize.Bool("sort", false, "Sort file table")
	optimizeDefrag := optimize.Bool("defrag", false, "Defragmentation")
	optimizeQuiet := optimize.Bool("quiet", false, "Don't show progress")

	datapax := flag.NewFlagSet("datapax", flag.ExitOnError)
	setSubcommandUsage(datapax, "<path to data.grf>")
	datapaxOutput := datapax.String("output", "", "Output file name (default \"<input file>.pax.xz\")")
	datapaxQuiet := datapax.Bool("quiet", false, "Don't show progress")

	filebase := flag.NewFlagSet("filebase", flag.ExitOnError)
	setSubcommandUsage(filebase, `[<input files...>]
Input files can contain glob patterns. Folders are added recursively. GRF files are ignored on these paths; see the "-grf" option.`)
	filebaseGRF := filebase.String("grf", "", "Path to a GRF file or folder to be used as its root")
	filebaseOutput := filebase.String("output", "", "Output file name (required; should end with \".filebase.yml\" or \".filebase.yml.xz\")")
	filebaseXZ := filebase.Bool("xz", false, "Compress output file")
	filebaseWhiteout := filebase.String("whiteout", "", "Path to a file containing a list of file names to whiteout")
	filebaseContent := filebase.String("content", "", "Path to the content folder for updating")
	filebaseQuiet := filebase.Bool("quiet", false, "Don't show progress")

	beamsonly := flag.NewFlagSet("beamsonly", flag.ExitOnError)
	setSubcommandUsage(beamsonly, "<grf files...>")
	beamsonlyKeep := beamsonly.String("keep", "", "Path to a file containing the list of file names to keep; if not given, uses an internal list")
	beamsonlyOutput := beamsonly.String("output", "beamsonly.grf", "Output GRF file name")

	if len(os.Args) < 2 {
		fmt.Println("Error: missing subcommand")
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "extract":
		extract.Parse(os.Args[2:])
		if extract.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			extract.Usage()
			os.Exit(1)
		}
		if !runExtract(extract.Arg(0), *extractOutput, *extractIgnore, *extractQuiet) {
			os.Exit(1)
		}
	case "checksum":
		checksum.Parse(os.Args[2:])
		if checksum.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			checksum.Usage()
			os.Exit(1)
		}
		if *checksumOutput == "" {
			*checksumOutput = fmt.Sprintf("%s.b2b", filepath.Base(checksum.Arg(0)))
		}
		if !runChecksum(checksum.Arg(0), *checksumOutput, *checkFix, *checksumQuiet) {
			os.Exit(1)
		}
	case "verify":
		verify.Parse(os.Args[2:])
		if verify.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			verify.Usage()
			os.Exit(1)
		}
		if *verifyChecksum == "" {
			*verifyChecksum = fmt.Sprintf("%s.b2b", filepath.Base(verify.Arg(0)))
		}
		if !runVerify(verify.Arg(0), *verifyChecksum, *verifyQuiet) {
			os.Exit(1)
		}
	case "optimize":
		optimize.Parse(os.Args[2:])
		if optimize.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			optimize.Usage()
			os.Exit(1)
		}
		if !runOptimize(optimize.Arg(0), *optimizeDedup, *optimizeSort, *optimizeDefrag, *optimizeQuiet) {
			os.Exit(1)
		}
	case "datapax":
		datapax.Parse(os.Args[2:])
		if datapax.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			datapax.Usage()
			os.Exit(1)
		}
		if *datapaxOutput == "" {
			*datapaxOutput = fmt.Sprintf("%s.pax.xz", filepath.Base(strings.TrimSuffix(datapax.Arg(0), path.Ext(datapax.Arg(0)))))
		}
		if !runDatapax(datapax.Arg(0), *datapaxOutput, *datapaxQuiet) {
			os.Exit(1)
		}
	case "filebase":
		filebase.Parse(os.Args[2:])
		if *filebaseOutput == "" {
			fmt.Println("Error: The \"-output\" option is required; it is an option that is not an option and this dichotomy shall haunt you for the rest of your life")
			os.Exit(1)
		}
		if !runFilebase(filebase.Args(), *filebaseGRF, *filebaseOutput, *filebaseWhiteout, *filebaseContent, *filebaseXZ, *filebaseQuiet) {
			os.Exit(1)
		}
	case "contentcheck":
		if len(os.Args) < 3 {
			fmt.Println("Error: missing path to content directory")
			os.Exit(1)
		}
		runContentcheck(os.Args[2])
	case "beamsonly":
		beamsonly.Parse(os.Args[2:])
		if beamsonly.NArg() < 1 {
			fmt.Println("Error: missing paths to GRF files")
			beamsonly.Usage()
			os.Exit(1)
		}
		if *beamsonlyKeep != "" {
			data, err := os.ReadFile(*beamsonlyKeep)
			if err != nil {
				fmt.Println("Error while reading keep file:", err)
				os.Exit(1)
			}
			for k := range keep {
				delete(keep, k)
			}
			for _, path := range strings.Split(string(data), "\n") {
				path = strings.TrimSpace(path)
				if len(path) > 0 {
					keep[path] = true
				}
			}
		}
		if !runBeamsonly(beamsonly.Args(), *beamsonlyOutput) {
			os.Exit(1)
		}
	case "-h", "-help", "--help", "help":
		printUsage()
	default:
		fmt.Println("Error: unknown subcommand")
		os.Exit(1)
	}
}
