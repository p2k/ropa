# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). The version applies to the command-line interface and to the file formats described in [FILEBASE.md](FILEBASE.md).

## [Unreleased]

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

[Unreleased]: https://github.com/p2k/ropa/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/p2k/ropa/releases/tag/v1.0.0
