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
	"runtime/debug"
	"strings"
)

// version is set for release builds with -ldflags "-X main.version=x.y.z".
var version = "dev"

// versionString returns the version; development builds include the VCS
// revision if available.
func versionString() string {
	if version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	var revision, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				modified = ", modified"
			}
		}
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if revision == "" {
		return version
	}
	return fmt.Sprintf("%s (%s%s)", version, revision, modified)
}

// keep lists the sound files that beamsonly leaves untouched by default; these
// are the item drop sounds that accompany the colored drop beams.
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

   list         List files in a GRF file
   extract      Extract a GRF file
   update       Update a GRF file
   rename       Rename files inside a GRF file
   checksum     Calculate checksums for a GRF file's contents
   verify       Verify the contents of a GRF file
   encrypt      Encrypt a GRF file (GRF Editor compatible)
   decrypt      Decrypt a GRF file (GRF Editor compatible)
   optimize     Optimize a GRF file
   recompress   Recompress a GRF file
   filebase     Build a filebase
   assemble     Assemble a GRF file from a filebase
   contentcheck Check a filebase content folder and delete broken files
   symsort      Sort a symlink file
   beamsonly    Build the beams-only GRF
   serve        Serve a GRF through WebDAV
   version      Print the version
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
	extractRaw := extract.Bool("raw", false, "Extract raw file streams")
	extractIgnore := extract.Bool("ignore", false, "Ignore errors on individual file extractions")
	extractCP1252 := extract.Bool("cp1252", false, "Use mangled filenames instead of Korean")
	extractKeyFile := extract.String("key", "", "Path to the encryption key file")
	extractQuiet := extract.Bool("quiet", false, "Don't show progress")

	update := flag.NewFlagSet("update", flag.ExitOnError)
	setSubcommandUsage(update, `<grf file> <input files...>
Input files can contain glob patterns. Folders are added recursively.`)
	updateDelete := update.Bool("delete", false, "Delete files inside the archive instead of updating")
	updateCompress := update.Int("compress", 9, "Compression level")
	updateNopipeline := update.Bool("nopipeline", false, "Does not use a pipeline for inserting files (saves memory)")
	updateCP1252 := update.Bool("cp1252", false, "Use mangled filenames instead of Korean")
	updateKeyFile := update.String("key", "", "Path to the encryption key file")
	updateQuiet := update.Bool("quiet", false, "Don't show progress")

	rename := flag.NewFlagSet("rename", flag.ExitOnError)
	setSubcommandUsage(rename, `<grf file> <old name> <new name>...
Old names and new names should be given in pairs.`)
	renameCP1252 := rename.Bool("cp1252", false, "Use mangled filenames instead of Korean")

	checksum := flag.NewFlagSet("checksum", flag.ExitOnError)
	setSubcommandUsage(checksum, "<grf file>")
	checksumOutput := checksum.String("output", "", "Output file name (default \"<input file>.b3c\" or \"<input file>.b3s\")")
	checksumStream := checksum.Bool("stream", false, "Calculate stream hashes instead of content hashes")
	checksumFix := checksum.Bool("fix", false, "Attempt to fix problems; will rewrite files as far as possible")
	checksumCP1252 := checksum.Bool("cp1252", false, "Use mangled filenames instead of Korean")
	checksumKeyFile := checksum.String("key", "", "Path to the encryption key file")
	checksumQuiet := checksum.Bool("quiet", false, "Don't show progress")

	verify := flag.NewFlagSet("verify", flag.ExitOnError)
	setSubcommandUsage(verify, "<grf file>")
	verifyChecksum := verify.String("checksum", "", "Checksum file name (default \"<input file>.b3c\" or \"<input file>.b3s\")")
	verifyStream := verify.Bool("stream", false, "Verify stream hashes instead of content hashes")
	verifyCP1252 := verify.Bool("cp1252", false, "Use mangled filenames instead of Korean")
	verifyKeyFile := verify.String("key", "", "Path to the encryption key file")
	verifyQuiet := verify.Bool("quiet", false, "Don't show progress")

	encrypt := flag.NewFlagSet("encrypt", flag.ExitOnError)
	setSubcommandUsage(encrypt, "<grf file>")
	encryptKeyFile := encrypt.String("key", "", "Path to the encryption key file")
	encryptQuiet := encrypt.Bool("quiet", false, "Don't show progress")

	decrypt := flag.NewFlagSet("decrypt", flag.ExitOnError)
	setSubcommandUsage(decrypt, "<grf file>")
	decryptKeyFile := decrypt.String("key", "", "Path to the encryption key file")
	decryptQuiet := decrypt.Bool("quiet", false, "Don't show progress")

	optimize := flag.NewFlagSet("optimize", flag.ExitOnError)
	setSubcommandUsage(optimize, "<grf file>")
	optimizeDedup := optimize.Bool("dedup", false, "Deduplication")
	optimizeSort := optimize.Bool("sort", false, "Sort file table")
	optimizeDefrag := optimize.Bool("defrag", false, "Defragmentation")
	optimizeKeyFile := optimize.String("key", "", "Path to the encryption key file")
	optimizeQuiet := optimize.Bool("quiet", false, "Don't show progress")

	recompress := flag.NewFlagSet("recompress", flag.ExitOnError)
	setSubcommandUsage(recompress, "<grf file>")
	recompressOutput := recompress.String("output", "", "Output file name (default same as input)")
	recompressLevel := recompress.Int("level", 15, "Compression level")
	recompressNopipeline := recompress.Bool("nopipeline", false, "Does not use a pipeline for inserting files (saves memory)")
	recompressKeyFile := recompress.String("key", "", "Path to the encryption key file")
	recompressQuiet := recompress.Bool("quiet", false, "Don't show progress")

	filebase := flag.NewFlagSet("filebase", flag.ExitOnError)
	setSubcommandUsage(filebase, `<filebase file> [<input files...>]
The filebase file should end with ".filebase.yml" or ".filebase.yml.gz". Input files can contain glob patterns. Folders are added recursively; files/folders starting with a "." are excluded. GRF files are not ignored on these paths and will be added as regular files, so be careful; also see the "-grf" option.`)
	filebaseGRF := filebase.String("grf", "", "Path to a GRF file or folder to be used as its root; will be excluded from input files; if the folder name is \"data\", this will prefix all paths with \"data\"")
	filebaseGRFName := filebase.String("grfname", "", "Name override for the GRF file without extension (used to merge GRF contents)")
	filebaseWhiteout := filebase.String("whiteout", "", "Path to a file containing a list of file names to whiteout")
	filebaseContent := filebase.String("content", "", "Path to the content folder for updating")
	filebaseCompress := filebase.Int("compress", 15, "Compression level")
	filebaseCP1252 := filebase.Bool("cp1252", false, "Use mangled filenames instead of Korean")
	filebaseKeyFile := filebase.String("key", "", "Path to the encryption key file")
	filebaseQuiet := filebase.Bool("quiet", false, "Don't show progress")

	symsort := flag.NewFlagSet("symsort", flag.ExitOnError)
	setSubcommandUsage(symsort, `<symlink file>`)

	beamsonly := flag.NewFlagSet("beamsonly", flag.ExitOnError)
	setSubcommandUsage(beamsonly, "<grf files...>")
	beamsonlyKeep := beamsonly.String("keep", "", "Path to a file containing the list of file names to keep; if not given, uses an internal list")
	beamsonlyCP1252 := beamsonly.Bool("cp1252", false, "Use mangled filenames instead of Korean")
	beamsonlyKeyFile := beamsonly.String("key", "", "Path to the encryption key file")
	beamsonlyOutput := beamsonly.String("output", "beamsonly.grf", "Output GRF file name")

	assemble := flag.NewFlagSet("assemble", flag.ExitOnError)
	setSubcommandUsage(assemble, "<grf file> <filebase file> <content dir>")
	assembleKeyFile := assemble.String("key", "", "Path to the encryption key file")
	assembleQuiet := assemble.Bool("quiet", false, "Don't show progress")

	serve := flag.NewFlagSet("serve", flag.ExitOnError)
	setSubcommandUsage(serve, "<grf file>")
	servePort := serve.Int("port", 8080, "Port")
	serveCompress := serve.Int("compress", 9, "Compression level")
	serveCP1252 := serve.Bool("cp1252", false, "Use mangled filenames instead of Korean")
	serveKeyFile := serve.String("key", "", "Path to the encryption key file")
	serveReadonly := serve.Bool("readonly", false, "Serve the archive read-only")

	if len(os.Args) < 2 {
		fmt.Println("Error: missing subcommand")
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "list":
		list.Parse(os.Args[2:])
		if list.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			list.Usage()
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
		if !runExtract(args[0], *extractKeyFile, patters, *extractOutput, *extractRaw, *extractIgnore, !*extractCP1252, *extractQuiet) {
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
			if !runUpdate(args[0], *updateKeyFile, args[1:], *updateCompress, !*updateNopipeline, !*updateCP1252, *updateQuiet) {
				os.Exit(1)
			}
		}
	case "rename":
		rename.Parse(os.Args[2:])
		if rename.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			rename.Usage()
			os.Exit(1)
		}
		if rename.NArg() < 3 {
			fmt.Println("Error: missing old-new name pair(s)")
			rename.Usage()
			os.Exit(1)
		}
		if rename.NArg()%2 != 1 {
			fmt.Println("Error: old and new names should be given in pairs")
			rename.Usage()
			os.Exit(1)
		}
		args := rename.Args()
		ar, err := grf.Open(args[0], false, !*renameCP1252)
		if err != nil {
			fmt.Println("Error while opening GRF file:", err)
			os.Exit(1)
		}
		for i := 1; i < len(args); i += 2 {
			oldName := args[i]
			newName := args[i+1]
			if !ar.Rename(oldName, newName) {
				fmt.Printf("Could not rename file \"%s\" to \"%s\"\n", oldName, newName)
				ar.Close()
				os.Exit(1)
			}
		}
		err = ar.Close()
		if err != nil {
			fmt.Println("Error while finalizing GRF:", err)
			os.Exit(1)
		}
		fmt.Println("All done.")
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
		if !runChecksum(checksum.Arg(0), *checksumOutput, *checksumKeyFile, *checksumStream, *checksumFix, !*checksumCP1252, *checksumQuiet) {
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
		if !runVerify(verify.Arg(0), *verifyChecksum, *verifyKeyFile, *verifyStream, !*verifyCP1252, *verifyQuiet) {
			os.Exit(1)
		}
	case "encrypt":
		encrypt.Parse(os.Args[2:])
		if encrypt.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			encrypt.Usage()
			os.Exit(1)
		}
		if *encryptKeyFile == "" {
			fmt.Println("Error: encryption key file is required")
			encrypt.Usage()
			os.Exit(1)
		}
		if !runCrypt(encrypt.Arg(0), *encryptKeyFile, true, *encryptQuiet) {
			os.Exit(1)
		}
	case "decrypt":
		decrypt.Parse(os.Args[2:])
		if decrypt.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			decrypt.Usage()
			os.Exit(1)
		}
		if *decryptKeyFile == "" {
			fmt.Println("Error: encryption key file is required")
			decrypt.Usage()
			os.Exit(1)
		}
		if !runCrypt(decrypt.Arg(0), *decryptKeyFile, false, *decryptQuiet) {
			os.Exit(1)
		}
	case "optimize":
		optimize.Parse(os.Args[2:])
		if optimize.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			optimize.Usage()
			os.Exit(1)
		}
		if !runOptimize(optimize.Arg(0), *optimizeKeyFile, *optimizeDedup, *optimizeSort, *optimizeDefrag, *optimizeQuiet) {
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
		if !runRecompress(recompress.Arg(0), tmpRecompressOutput, *recompressKeyFile, *recompressLevel, !*recompressNopipeline, *recompressQuiet) {
			os.Exit(1)
		}
		if tmpRecompressOutput != *recompressOutput {
			os.Rename(tmpRecompressOutput, *recompressOutput)
		}
	case "filebase":
		filebase.Parse(os.Args[2:])
		if filebase.NArg() < 1 {
			fmt.Println("Error: missing path to filebase file")
			filebase.Usage()
			os.Exit(1)
		}
		args := filebase.Args()
		if !runFilebase(args[0], args[1:], *filebaseGRF, *filebaseGRFName, *filebaseWhiteout, *filebaseContent, *filebaseKeyFile, *filebaseCompress, !*filebaseCP1252, *filebaseQuiet) {
			os.Exit(1)
		}
	case "symsort":
		symsort.Parse(os.Args[2:])
		if symsort.NArg() < 1 {
			fmt.Println("Error: missing path to symlink file")
			symsort.Usage()
			os.Exit(1)
		}
		args := symsort.Args()
		if !runSymsort(args[0]) {
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
		if !runBeamsonly(beamsonly.Args(), *beamsonlyOutput, *beamsonlyKeyFile) {
			os.Exit(1)
		}
	case "assemble":
		assemble.Parse(os.Args[2:])
		if assemble.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			assemble.Usage()
			os.Exit(1)
		}
		if assemble.NArg() < 2 {
			fmt.Println("Error: missing path to filebase file")
			assemble.Usage()
			os.Exit(1)
		}
		if assemble.NArg() < 3 {
			fmt.Println("Error: missing path to content directory")
			assemble.Usage()
			os.Exit(1)
		}
		args := assemble.Args()
		if !runAssemble(args[0], args[1], args[2], *assembleKeyFile, *assembleQuiet) {
			os.Exit(1)
		}
	case "serve":
		serve.Parse(os.Args[2:])
		if serve.NArg() < 1 {
			fmt.Println("Error: missing path to GRF file")
			serve.Usage()
			os.Exit(1)
		}
		args := serve.Args()
		if !runServe(args[0], *serveKeyFile, *servePort, *serveCompress, !*serveCP1252, *serveReadonly) {
			os.Exit(1)
		}
	case "-v", "-version", "--version", "version":
		fmt.Println("ropa", versionString())
	case "-h", "-help", "--help", "help":
		printUsage()
	default:
		fmt.Println("Error: unknown subcommand")
		os.Exit(1)
	}
}
