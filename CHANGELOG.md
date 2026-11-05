# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). The version applies to the command-line interface and to the file formats described in [FILEBASE.md](FILEBASE.md).

## [Unreleased]

### Changed

- `optimize -dedup` now also catches files that shared data with a file it had just deduplicated, which previously needed a second run.

### Fixed

- Replacing, deleting or renaming a file that other entries were linked to left those links pointing at the old name until the archive was reopened. `update` could then give a new link from `SYMLINKS` the replaced file's new data instead of the data of the file it names, and `Link` in the `grf` package crashed after the original file had been renamed.
- Files written with `PutBytes` (for example through `serve`) all got the same content hash, so deduplicating in the same session could link files with different contents.
- `serve`: a file deleted and then uploaded again under the same name was missing from folder listings until the archive was reopened.
- `optimize` had a data race between its progress display and the next step.
- Writing to an encrypted archive with `update -nopipeline`, `recompress -nopipeline` or `serve` produced corrupted files while reporting success.
- `update` and `recompress` without `-nopipeline` wrote new files unencrypted into encrypted archives.
- `update -nopipeline` and `recompress -nopipeline` with Zopfli (levels above 9) stored files as if they were uncompressed, so they could not be read back.
- The `__encryption.info` marker is never encrypted, so an archive stays openable when the marker is copied, for example by `recompress`.
- `Put` and `PutBytes` in the `grf` package recorded stream hashes of the uncompressed instead of the compressed data.

## [1.0.1] - 2026-11-05

### Changed

- The module path is now `github.com/p2k/ropa`, so ROPA can be installed with `go install github.com/p2k/ropa@latest` and the `grf` package can be imported by other projects.
- `ropa version` reports the module version when the binary was built with `go install` or from a git checkout.

## [1.0.0] - 2026-11-03

First tagged release.

### Added

- Read and write support for GRF 0x200 archives, including zlib, uncompressed, LZMA (LessGRF / GRFCrush) and legacy DES/mixcrypt entries.
- Commands: `list`, `extract`, `update` (with `-delete`), `rename`, `checksum`, `verify`, `optimize` (`-dedup`, `-sort`, `-defrag`), `recompress`, `encrypt`, `decrypt`, `serve`, `filebase`, `assemble`, `contentcheck`, `symsort`, `beamsonly` and `version`.
- Parallel compression with zlib or Zopfli.
- Deduplicated files as links, kept intact across `extract` and `update` through `SYMLINKS` files.
- BLAKE3 content and stream checksums, with a best-effort repair mode (`checksum -fix`).
- GRF Editor compatible encryption.
- WebDAV server with read and write support.
- `ROPAFilebase/v1` and `ROPAIntegrityMap/v1` formats with a deduplicated content store.
- Proper handling of Korean (EUC-KR) file names, with `-cp1252` for mangled names.

### Fixed

- `update -quiet` and `recompress -quiet` crashed after writing. `recompress` left its temporary file behind instead of replacing the original.
- `update` hung forever when a link's target was not among the inputs, and skipped writing when only links were given.
- The parallel write pipeline crashed when more than one job failed, and had a data race between finishing a job and starting the next one.
- `serve` crashed without a GRF argument.
- `filebase -grf <folder>` listed the `SYMLINKS` file as content, and missed it when given a `data` folder.
- Read errors in filebases, checksum, whiteout, integrity and `SYMLINKS` files were silently ignored, so a truncated file could be processed as if it were complete.
- Hex hashes of the wrong length were partially accepted.

[Unreleased]: https://github.com/p2k/ropa/compare/v1.0.1...HEAD
[1.0.1]: https://github.com/p2k/ropa/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/p2k/ropa/releases/tag/v1.0.0
