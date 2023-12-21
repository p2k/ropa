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
	"ropa/grf"
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

   list       List files in a GRF file
   extract    Extract a GRF file
   update     Update a GRF file
   checksum   Calculate checksums for a GRF file's contents
   verify     Verify the contents of a GRF file
   optimize   Optimize a GRF file
   recompress Recompress a GRF file
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
	list := flag.NewFlagSet("list", flag.ExitOnError)
	listCP1252 := list.Bool("cp1252", false, "Use mangled filenames instead of Korean")
	setSubcommandUsage(list, "<grf file>")

	extract := flag.NewFlagSet("extract", flag.ExitOnError)
	setSubcommandUsage(extract, `<grf file> [<output files...>]
Output files can contain glob patterns. Folders are extracted recursively.`)
	extractOutput := extract.String("output", ".", "Output folder")
	extractIgnore := extract.Bool("ignore", false, "Ignore errors on individual file extractions")
	extractCP1252 := extract.Bool("cp1252", false, "Use mangled filenames instead of Korean")
	extractQuiet := extract.Bool("quiet", false, "Don't show progress")

	update := flag.NewFlagSet("update", flag.ExitOnError)
	setSubcommandUsage(update, `<grf file> <input files...>
Input files can contain glob patterns. Folders are added recursively.`)
	updateDelete := update.Bool("delete", false, "Delete files inside the archive instead of updating")
	updateCompress := update.Int("compress", 9, "Compression level")
	updateNopipeline := update.Bool("nopipeline", false, "Does not use a pipeline for inserting files (saves memory)")
	updateCP1252 := update.Bool("cp1252", false, "Use mangled filenames instead of Korean")
	updateQuiet := update.Bool("quiet", false, "Don't show progress")

	checksum := flag.NewFlagSet("checksum", flag.ExitOnError)
	setSubcommandUsage(checksum, "<grf file>")
	checksumOutput := checksum.String("output", "", "Output file name (default \"<input file>.b3c\" or \"<input file>.b3s\")")
	checksumStream := checksum.Bool("stream", false, "Calculate stream hashes instead of content hashes")
	checksumFix := checksum.Bool("fix", false, "Attempt to fix problems; will rewrite files as far as possible")
	checksumCP1252 := checksum.Bool("cp1252", false, "Use mangled filenames instead of Korean")
	checksumQuiet := checksum.Bool("quiet", false, "Don't show progress")

	verify := flag.NewFlagSet("verify", flag.ExitOnError)
	setSubcommandUsage(verify, "<grf file>")
	verifyChecksum := verify.String("checksum", "", "Checksum file name (default \"<input file>.b3c\" or \"<input file>.b3s\")")
	verifyStream := verify.Bool("stream", false, "Verify stream hashes instead of content hashes")
	verifyCP1252 := verify.Bool("cp1252", false, "Use mangled filenames instead of Korean")
	verifyQuiet := verify.Bool("quiet", false, "Don't show progress")

	optimize := flag.NewFlagSet("optimize", flag.ExitOnError)
	setSubcommandUsage(optimize, "<grf file>")
	optimizeDedup := optimize.Bool("dedup", false, "Deduplication")
	optimizeSort := optimize.Bool("sort", false, "Sort file table")
	optimizeDefrag := optimize.Bool("defrag", false, "Defragmentation")
	optimizeQuiet := optimize.Bool("quiet", false, "Don't show progress")

	recompress := flag.NewFlagSet("recompress", flag.ExitOnError)
	setSubcommandUsage(recompress, "<grf file>")
	recompressOutput := recompress.String("output", "", "Output file name (default same as input)")
	recompressLevel := recompress.Int("level", 15, "Compression level")
	recompressNopipeline := recompress.Bool("nopipeline", false, "Does not use a pipeline for inserting files (saves memory)")
	recompressQuiet := recompress.Bool("quiet", false, "Don't show progress")

	filebase := flag.NewFlagSet("filebase", flag.ExitOnError)
	setSubcommandUsage(filebase, `[<input files...>]
Input files can contain glob patterns. Folders are added recursively; .git and .gitignore are excluded. GRF files are not ignored on these paths and will be added as regular files, so be careful; also see the "-grf" option.`)
	filebaseGRF := filebase.String("grf", "", "Path to a GRF file or folder to be used as its root; will be excluded from input files")
	filebaseOutput := filebase.String("output", "", "Output file name (required; should end with \".filebase.yml\" or \".filebase.yml.gz\")")
	filebaseCompress := filebase.Int("compress", 15, "Compression level")
	filebaseWhiteout := filebase.String("whiteout", "", "Path to a file containing a list of file names to whiteout")
	filebaseContent := filebase.String("content", "", "Path to the content folder for updating")
	filebaseCP1252 := filebase.Bool("cp1252", false, "Use mangled filenames instead of Korean")
	filebaseQuiet := filebase.Bool("quiet", false, "Don't show progress")

	beamsonly := flag.NewFlagSet("beamsonly", flag.ExitOnError)
	setSubcommandUsage(beamsonly, "<grf files...>")
	beamsonlyKeep := beamsonly.String("keep", "", "Path to a file containing the list of file names to keep; if not given, uses an internal list")
	beamsonlyCP1252 := beamsonly.Bool("cp1252", false, "Use mangled filenames instead of Korean")
	beamsonlyOutput := beamsonly.String("output", "beamsonly.grf", "Output GRF file name")

	if len(os.Args) < 2 {
		fmt.Println("Error: missing subcommand")
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "list":
		list.Parse(os.Args[2:])
		if extract.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			extract.Usage()
			os.Exit(1)
		}
		ar, err := grf.Open(list.Arg(0), true, !*listCP1252)
		if err != nil {
			fmt.Println("Error while opening GRF file:", err)
			os.Exit(1)
		}
		for _, path := range ar.AllFileNames() {
			h := ar.Head(path)
			if h != nil && h.Linkname != "" {
				fmt.Println(path, "->", h.Linkname)
			} else {
				fmt.Println(path)
			}
		}
		ar.Close()
	case "extract":
		extract.Parse(os.Args[2:])
		if extract.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			extract.Usage()
			os.Exit(1)
		}
		args := extract.Args()
		patters := []string{"*"}
		if len(args) >= 2 {
			patters = args[1:]
		}
		if !runExtract(args[0], patters, *extractOutput, *extractIgnore, !*extractCP1252, *extractQuiet) {
			os.Exit(1)
		}
	case "update":
		update.Parse(os.Args[2:])
		if update.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			update.Usage()
			os.Exit(1)
		}
		if update.NArg() < 2 {
			fmt.Println("Error: missing input files")
			update.Usage()
			os.Exit(1)
		}
		args := update.Args()
		if *updateDelete {
			if !runDelete(args[0], args[1:], !*updateCP1252) {
				os.Exit(1)
			}
		} else {
			if !runUpdate(args[0], args[1:], *updateCompress, !*updateNopipeline, !*updateCP1252, *updateQuiet) {
				os.Exit(1)
			}
		}
	case "checksum":
		checksum.Parse(os.Args[2:])
		if checksum.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			checksum.Usage()
			os.Exit(1)
		}
		if *checksumOutput == "" {
			var ext string
			if *checksumStream {
				ext = "b3s"
			} else {
				ext = "b3c"
			}
			*checksumOutput = fmt.Sprintf("%s.%s", filepath.Base(checksum.Arg(0)), ext)
		}
		if !runChecksum(checksum.Arg(0), *checksumOutput, *checksumStream, *checksumFix, !*checksumCP1252, *checksumQuiet) {
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
			var ext string
			if *verifyStream {
				ext = "b3s"
			} else {
				ext = "b3c"
			}
			*verifyChecksum = fmt.Sprintf("%s.%s", filepath.Base(verify.Arg(0)), ext)
		}
		if !runVerify(verify.Arg(0), *verifyChecksum, *verifyStream, !*verifyCP1252, *verifyQuiet) {
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
	case "recompress":
		recompress.Parse(os.Args[2:])
		if recompress.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			recompress.Usage()
			os.Exit(1)
		}
		if *recompressOutput == "" {
			*recompressOutput = recompress.Arg(0)
		}
		tmpRecompressOutput := *recompressOutput
		if recompress.Arg(0) == tmpRecompressOutput {
			tmpRecompressOutput = fmt.Sprintf("%s_recompress_.grf", filepath.Base(strings.TrimSuffix(recompress.Arg(0), path.Ext(recompress.Arg(0)))))
		}
		if !runRecompress(recompress.Arg(0), tmpRecompressOutput, *recompressLevel, !*recompressNopipeline, *recompressQuiet) {
			os.Exit(1)
		}
		if tmpRecompressOutput != *recompressOutput {
			os.Rename(tmpRecompressOutput, *recompressOutput)
		}
	case "filebase":
		filebase.Parse(os.Args[2:])
		if *filebaseOutput == "" {
			fmt.Println("Error: The \"-output\" option is required; it is an option that is not an option and this dichotomy shall haunt you for the rest of your life")
			os.Exit(1)
		}
		if !runFilebase(filebase.Args(), *filebaseGRF, *filebaseOutput, *filebaseWhiteout, *filebaseContent, *filebaseCompress, !*filebaseCP1252, *filebaseQuiet) {
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
				cleanPath := strings.TrimSpace(path)
				if *beamsonlyCP1252 {
					cleanPath, err = grf.SanitizeFileName(cleanPath)
					if err != nil {
						fmt.Printf("Error while sanitizing file name \"%s\": %s", path, err)
						os.Exit(1)
					}
				}
				if len(cleanPath) > 0 {
					keep[cleanPath] = true
				}
			}
		} else {
			*beamsonlyCP1252 = false
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
