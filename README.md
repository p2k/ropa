
ROPA
====

ROPA (Ragnarök Online Package Application) is a command-line toolkit for working with **GRF archives**, the container format used by the Ragnarök Online client. It can list, extract, create, update, verify, optimize, recompress, encrypt and serve GRF files, and it can turn client data into a content-addressed *filebase* for distribution.

It is written in Go, uses Zopfli for maximum compression, and treats Korean file names properly (EUC-KR) instead of showing them as Windows-1252 garbage.

Features
--------

- **Full read/write support** for GRF version 0x200 (`Master of Magic` header). Archives are modified in place: new data is appended and the file table is rewritten when you're done. Use `optimize -defrag` to reclaim the space left by replaced files.
- **Reads every common compression variant**: zlib, uncompressed entries, LZMA (as written by LessGRF / GRFCrush) and the legacy DES/mixcrypt encrypted entries.
- **GRF Editor compatible encryption**: encrypt and decrypt archives with a GRF Editor key file.
- **Fast parallel compression** using all CPU cores, with zlib levels 0–9 or Zopfli (levels above 9).
- **Deduplication**: identical files can share one copy of their data. ROPA shows these as *links*, and a plain `SYMLINKS` text file keeps them intact when you extract and rebuild.
- **Integrity checking** with BLAKE3 checksums of file contents or raw streams, plus a best-effort repair mode.
- **Defragmentation and sorting** that work in place and can be interrupted safely.
- **WebDAV server**: mount a GRF as a network drive and edit it with any file manager.
- **Filebase / content store**: describe a whole client (loose files plus GRF contents) as a compact YAML manifest backed by deduplicated, pre-compressed blobs, then rebuild a GRF from it.

Building
--------

You need Go 1.23 or newer and a C compiler, because Zopfli is built with cgo.

```bash
git clone https://github.com/p2k/ropa.git
```

```bash
cd ropa && go build
```

This produces a single `ropa` binary. The module path is `ropa`, so build from a clone rather than with `go install <url>`.

Quick start
-----------

```bash
ropa list data.grf
```

```bash
ropa extract -output extracted data.grf
```

```bash
cd extracted && ropa update ../new.grf data
```

```bash
ropa optimize -dedup -sort -defrag new.grf
```

Run `ropa help` for the list of commands and `ropa <command> -h` for a command's options. Options go **before** the positional arguments.

Commands
--------

### Inspecting and extracting

| Command | What it does |
| --- | --- |
| `list <grf>` | Prints every file in the archive. Linked (deduplicated) files are shown as `name -> target`. |
| `extract [-output dir] <grf> [patterns...]` | Extracts everything, or only the files matching the glob patterns (folders are extracted recursively). `-raw` writes the compressed streams as they are, and `-ignore` skips files that fail to extract instead of stopping. |

When extracting, linked files are not duplicated on disk. They are recorded in a `SYMLINKS` file in the output folder instead, one `name -> target` line per link.

### Creating and modifying

| Command | What it does |
| --- | --- |
| `update <grf> <inputs...>` | Adds or replaces files and creates the GRF if it doesn't exist yet. Folders are added recursively. |
| `update -delete <grf> <patterns...>` | Deletes the matching files from the archive. |
| `rename <grf> <old> <new> [<old> <new>...]` | Renames files inside the archive. |
| `recompress [-level n] [-output out.grf] <grf>` | Rewrites the whole archive with a new compression level (default 15, i.e. Zopfli). Without `-output`, the input is replaced. |

**Paths inside the GRF are the paths you pass, relative to the current directory.** To pack an extracted client, `cd` into the folder that contains `data/` and run `ropa update ../my.grf data`. If that folder contains a `SYMLINKS` file, its links are recreated as well.

`-compress` / `-level` take 0–9 for zlib or a value above 9 for Zopfli with that many iterations; 15 is a good default. Zopfli produces smaller files but is much slower. `-nopipeline` trades speed for lower memory use.

### Integrity

| Command | What it does |
| --- | --- |
| `checksum <grf>` | Writes a BLAKE3 checksum file (`<grf name>.b3c` in the current directory, in the same `hash  name` layout as `b3sum`). |
| `checksum -stream <grf>` | Hashes the raw compressed streams instead (`.b3s`), which is faster because nothing is decompressed. |
| `checksum -fix <grf>` | Also tries to repair damaged entries: truncated files are rewritten with what could be read, and unreadable ones are removed. |
| `verify [-checksum file] <grf>` | Checks the archive against a checksum file. Missing or extra files count as failures, and the exit code is non-zero if anything fails. |

### Optimizing

`ropa optimize` takes any combination of:

- `-dedup` links files with identical content to a single copy of the data.
- `-sort` orders the file table alphabetically.
- `-defrag` rewrites the data in order and reclaims the space left by deleted or replaced files. This runs in place, and Ctrl-C leaves a valid archive behind.

### Encryption

```bash
ropa encrypt -key my.grfkey data.grf
```

```bash
ropa decrypt -key my.grfkey data.grf
```

The key file is either a raw 256-byte key (a permutation of the bytes 0–255) or a 260-byte GRF Editor key file. An encrypted archive contains a small `__encryption.info` marker that stores a CRC32 of the key, so using the wrong key is detected.

Most other commands also accept `-key`, so you can read and write encrypted archives directly.

### WebDAV server

```bash
ropa serve -port 8080 data.grf
```

This serves the archive at `http://127.0.0.1:8080/` (local connections only), so you can mount it in Finder, Explorer or any WebDAV client. Writes go straight into the GRF and are compressed with `-compress` (default 9). Use `-readonly` to prevent changes. Stop the server with Ctrl-C or by requesting `http://127.0.0.1:8080/close!`; the archive is finalized on shutdown.

### Filebase and content store

A *filebase* is a YAML manifest (`ROPAFilebase/v1`) that lists every file of a client with its BLAKE3 hash and size. It has two sections: `root_contents` for loose files and `grf_contents` for the contents of a GRF. Next to it lives a content folder of zlib-compressed blobs named `<hash>-<size in hex>.zl`, plus an `integrity.yml.gz` map. Identical files are stored only once, and only new blobs are written on later runs.

```bash
ropa filebase -grf data.grf -content store client.filebase.yml.gz BGM System
```

```bash
ropa assemble rebuilt.grf client.filebase.yml.gz store
```

- Positional inputs after the manifest name become `root_contents`. Folders are added recursively, and anything starting with `.` is skipped.
- `-grf` accepts a GRF file or an extracted folder. With a folder, its `SYMLINKS` file is honoured. A folder named `data` is placed under a `data/` prefix, and its `SYMLINKS` file is read from the parent folder, where `extract` puts it.
- `-whiteout file` marks the listed paths as deleted (they get an all-zero hash), and `-grfname` records a GRF name override.
- Without `-content`, only the manifest is written and no blobs are stored.
- A `.gz` suffix on the manifest name compresses it.
- `assemble` builds a GRF from the `grf_contents` section by copying the pre-compressed blobs directly, so nothing is compressed again. It always creates the output file from scratch.
- `ropa contentcheck <content dir>` re-hashes every blob and **deletes** any blob whose content doesn't match its name.

### Other utilities

- `symsort <SYMLINKS file or folder>` sorts a `SYMLINKS` file by path.
- `beamsonly [-output beamsonly.grf] <grfs...>` builds a small GRF that replaces every `.wav` in the given archives with silence, except for the item-drop "beam" sounds (or the paths listed in `-keep`). Load it with top priority to mute the game while still hearing drops.

Korean file names
-----------------

GRF files store names in EUC-KR. Many older tools decode them as Windows-1252, which turns them into the familiar mojibake such as `À¯ÀúÀÎÅÍÆäÀÌ½º`. ROPA shows and expects the real Korean names by default. Pass `-cp1252` to list, extract or add files using the mangled names instead, for example to stay compatible with existing folders or patch lists.

License
-------

Copyright © Patrick "p2k" Schneider. Licensed under the [EUPL v1.2](LICENSE).

ROPA is an independent project and is not affiliated with or endorsed by Gravity Co., Ltd. Ragnarok Online is a trademark of its respective owner. ROPA contains no game data.
