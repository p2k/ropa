---
name: ropa
description: Use the ROPA CLI to inspect, extract, build, modify, verify, optimize, encrypt, recompress or serve Ragnarok Online GRF archives, and to build or assemble ROPA filebases (content-addressed client manifests). Use it whenever a task involves .grf files, GRF Editor key files, SYMLINKS files, .b3c/.b3s checksum files or *.filebase.yml(.gz) manifests.
---

# ROPA: GRF archive toolkit

ROPA is a single Go binary (`ropa`) for Ragnarok Online GRF archives (version 0x200 only). The source is a flat `main` package (`main.go` plus `cmd_*.go`, one file per subcommand), with the format library in `grf/` and a cgo Zopfli wrapper in `zopfli/`.

## Build

```bash
go build -o ropa .
```

- cgo must be enabled, because Zopfli is C. The module path is `github.com/p2k/ropa`; internal imports use `github.com/p2k/ropa/grf` and `github.com/p2k/ropa/zopfli`. Users can also install with `go install github.com/p2k/ropa@latest`.
- In a sandbox where the Go build cache isn't writable, set `GOCACHE=$TMPDIR/gocache`.
- `ropa version` prints the version. Release builds set it with `-ldflags "-X main.version=X.Y.Z"`; other builds use the module version Go records (a tag, a pseudo-version or `+dirty`), and fall back to `dev` plus the git revision.
- There are no tests. `gofmt -l .`, `go vet ./...` and `staticcheck ./...` should be clean. Verify behaviour by running the binary on a throwaway GRF (see "Smoke test" below).

## Releasing

Releases are created by `.github/workflows/release.yml` when a `vX.Y.Z` tag is pushed. Before tagging, add a `## [X.Y.Z] - YYYY-MM-DD` section to `CHANGELOG.md` (moving the `Unreleased` entries) and update the links at the bottom; the workflow fails without that section and uses it as the release notes. Pushing tags publishes a release, so only do it when the user asks.

## Invocation rules

- Use the form `ropa <command> [options] <positional args>`. **Flags must come before positional arguments** (Go `flag` package); a flag placed after the GRF path is treated as a file name.
- Every command prints `All done.` on success and exits 1 on failure. Read the output: some failures (for example a bad path inside `rename`) only print a message.
- On a TTY, commands show progress bars. In scripts, pass `-quiet` where it is offered.
- File names inside a GRF always use forward slashes, are matched case-sensitively by glob patterns, and are shown as real Korean (EUC-KR decoded) by default. Add `-cp1252` to use mangled Windows-1252 names instead. Use the same mode consistently within a workflow.
- For encrypted archives, pass `-key <file>` to any command that reads or writes content. The key is a 256-byte permutation of 0–255 or a 260-byte GRF Editor `.grfkey` file.

## Command reference

| Task | Command | Notes |
| --- | --- | --- |
| List files | `ropa list a.grf` | Prints one name per line. Deduplicated entries print as `name -> target`. |
| Extract | `ropa extract -output DIR a.grf [glob...]` | Links are written to `DIR/SYMLINKS` instead of being duplicated. `-raw` dumps compressed streams; `-ignore` continues past bad entries. |
| Add/replace files | `ropa update a.grf PATH...` | Creates the GRF if it is missing. **GRF paths are the paths as given, relative to the current directory.** Reads `./SYMLINKS` for links. `-compress N` defaults to 9. |
| Delete files | `ropa update -delete a.grf GLOB...` | The globs match paths inside the GRF. |
| Rename | `ropa rename a.grf OLD NEW [OLD NEW...]` | Takes exact names, not globs. Stops at the first failure. |
| Checksums | `ropa checksum a.grf` | Writes `<basename>.b3c` to the **current directory**. `-stream` writes `.b3s` (raw stream hashes, faster); `-output` overrides the path. |
| Repair | `ropa checksum -fix a.grf` | Opens read-write: truncated entries are rewritten with the readable part, unreadable entries are **deleted**. |
| Verify | `ropa verify [-checksum F] a.grf` | Defaults to `./<basename>.b3c`. Prints `Verify ok.` or a list of failed files; missing or extra files count as failed. Exits 1 on failure. |
| Optimize | `ropa optimize -dedup -sort -defrag a.grf` | With no flag it does nothing. Defrag is in place and safe to interrupt. |
| Recompress | `ropa recompress [-level 15] [-output b.grf] a.grf` | Without `-output` it writes `./<name>_recompress_.grf` and renames it over the input. |
| Encrypt/decrypt | `ropa encrypt -key K a.grf` / `ropa decrypt -key K a.grf` | Works in place. Adds or removes the `__encryption.info` marker (a CRC32 of the key); a wrong key is rejected. |
| WebDAV | `ropa serve [-port 8080] [-readonly] a.grf` | Binds to 127.0.0.1 only and blocks until Ctrl-C or `GET /close!`. Run it in the background and stop it via `/close!` so the archive is finalized. |
| Filebase | `ropa filebase [-grf G] [-content DIR] [-whiteout F] [-grfname N] OUT.filebase.yml[.gz] [ROOTPATH...]` | See below. |
| Assemble | `ropa assemble OUT.grf FB.filebase.yml[.gz] CONTENTDIR` | Builds a GRF from `grf_contents` using the stored blobs as-is. **Overwrites OUT.grf.** |
| Check content store | `ropa contentcheck CONTENTDIR` | Re-hashes every `.zl` blob and **deletes** mismatches. |
| Sort SYMLINKS | `ropa symsort PATH` | PATH is a SYMLINKS file or the folder that contains one. |
| Beams-only GRF | `ropa beamsonly [-output beamsonly.grf] [-keep F] a.grf...` | Silences every `.wav` except the item-drop sounds (or the paths listed in `-keep`). |

Compression levels: 0–9 are zlib; a value above 9 is Zopfli with that many iterations (15 is the recommended setting, and the default for `recompress` and `filebase`). Zopfli is many times slower, so prefer 9 for quick iteration.

## Common workflows

Round-trip an archive (extract, edit, rebuild):

```bash
ropa extract -output work a.grf
```

```bash
cd work && ropa update ../b.grf data
```

```bash
ropa optimize -dedup -sort -defrag b.grf
```

The `cd` matters: `update` must run from the folder that contains `data/` and `SYMLINKS`, otherwise the paths stored in the GRF get the wrong prefix.

Patch a few files into an existing GRF: put them under the same relative path (for example `patch/data/texture/x.bmp`), `cd patch`, then run `ropa update ../a.grf data`. Replaced data is appended; run `optimize -defrag` afterwards to reclaim space.

Check integrity before and after a change:

```bash
ropa checksum -stream a.grf
```

```bash
ropa verify -stream a.grf
```

## Filebase format

`ROPAFilebase/v1` is YAML written in a restricted, line-oriented form that `assemble` parses by hand:

```yaml
type: ROPAFilebase/v1
root_contents:
  data:
    texture:
      a.txt: 0000000000000000000000000000000000000000000000000000000000000000-00000000
  BGM:
    01.mp3: <64-hex blake3>-<8-hex size>
grf_name: foo
grf_contents:
  data:
    texture:
      a.txt: <64-hex blake3>-<8-hex size>
```

- Indentation is 2 spaces per level, keys are path components, and a leaf value is `<blake3 of uncompressed content>-<size as 8 hex digits>`. An all-zero value is a whiteout (deleted file). Empty directories appear as keys with no value.
- Keys are JSON-quoted only when YAML would need it. If you edit a filebase by hand, keep this exact shape (`assemble` relies on the line length of leaf entries).
- The content store holds `<hash>-<size>.zl` zlib blobs (`.mp3` and `.grf` files are stored at level 0) plus `integrity.yml.gz` (`ROPAIntegrityMap/v1`, which maps stream hashes to content hashes). Existing blobs are never rewritten.
- `-grf` accepts a GRF file or an extracted folder. With a folder, its top-level `SYMLINKS` file is used for links and is not itself listed as content. If the folder is named `data`, paths get a `data/` prefix and `SYMLINKS` is read from the parent folder, which is where `extract` writes it. Root inputs keep the path as typed, relative to the current directory, and anything whose name starts with `.` is skipped.

## Things to keep in mind

- `checksum` and `verify` resolve the default checksum file name from the GRF's base name in the **current directory**, not next to the GRF.
- `optimize`, `recompress`, `encrypt` and `decrypt` always use Korean names and have no `-cp1252` flag.
- Destructive operations (`checksum -fix`, `contentcheck`, `assemble`, `recompress` without `-output`, `update -delete`, `encrypt`/`decrypt`) modify or replace files in place. Copy the GRF first if the original matters.

## Smoke test

Run this in a scratch directory:

```bash
mkdir -p src/data/t && echo hi > src/data/t/a.txt && cp src/data/t/a.txt src/data/t/b.txt
```

```bash
cd src && ropa update ../t.grf data && cd ..
```

```bash
ropa optimize -quiet -dedup t.grf && ropa list t.grf
```

Expected output: `data/t/a.txt` and `data/t/b.txt -> data/t/a.txt`. Then confirm the archive with `ropa checksum -quiet t.grf && ropa verify -quiet t.grf`, which should print `Verify ok.`

## Library (`grf` package) pointers

- Import path: `github.com/p2k/ropa/grf` (needs cgo, since it uses the Zopfli wrapper).
- `grf.Open(path, readonly, koreanFilenames)` and `grf.Create(...)` return an `*Archive`. Always call `Close()`: that is when the file table is written.
- `Archive` implements `fs.FS`, `ReadDirFS`, `StatFS` and `GlobFS`. `Put`/`PutBytes` compress; `RawPut` stores pre-compressed zlib; `Link`/`LinkHeader` create links, which are entries that share an offset. Links are copy-on-write: writing to one member gives only that entry new data. The archive keeps `Linkname` up to date (see `grf/links.go`); don't set it yourself.
- `NewPipeline(ar, workers, level)` compresses in parallel. Don't touch the archive until `Wait()` returns.
- `Dedup`, `Sort`, `Defragment`, `HashAll`, `VerifyAll` and `SetGRFEditorEncryptionKey`/`EnsureEncryptionStatus` back the matching subcommands.
- `grf.OpenSymlinks(pathOrDir)` reads and writes `SYMLINKS` files (lines of the form `name -> target`).
