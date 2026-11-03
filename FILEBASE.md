Filebase format
===============

A *filebase* describes the complete set of files of a game client: the loose files in the client folder and the contents of a GRF archive. It goes together with a *content folder*, a deduplicated store of compressed file contents. `ropa filebase` creates both, and `ropa assemble` builds a GRF from them.

This document describes version 1 of both formats.

Hashes and sizes
----------------

Every file is identified by a **content key** of the form `<hash>-<size>`:

- `<hash>` is the BLAKE3-256 hash of the uncompressed file contents, as 64 lowercase hex digits.
- `<size>` is the uncompressed size in bytes, as at least 8 lowercase hex digits with leading zeros.

Example: `8e4c7c1b99dbfd50e7a95185fead5ee1448fa904a2fdd778eaf5f2dbfd629a99-00000006`

The content key `0000000000000000000000000000000000000000000000000000000000000000-00000000` (all zeros) is reserved for **whiteouts**: files that must be deleted.

The filebase file
-----------------

A filebase is a UTF-8 YAML document, usually named `<name>.filebase.yml`. If the name ends with `.gz`, the whole file is gzip-compressed.

```yaml
type: ROPAFilebase/v1
root_contents:
  BGM:
    01.mp3: 3f9a…-0012d3a0
  System:
    iteminfo.lub: 0000000000000000000000000000000000000000000000000000000000000000-00000000
  savedata:
grf_name: data
grf_contents:
  data:
    texture:
      a.bmp: 8e4c…-00000406
      b.bmp: 8e4c…-00000406
```

### Structure

- The first line is always `type: ROPAFilebase/v1`.
- `root_contents:` lists the loose files of the client folder. The section is omitted if there are none.
- `grf_name:` is optional. It names the GRF that the following contents belong to, without the `.grf` extension. Filebases with the same `grf_name` describe the same GRF, so their contents can be merged. If it is missing, the consumer chooses the GRF name.
- `grf_contents:` lists the files inside the GRF. The section is omitted if there are none. Its paths are the paths inside the GRF, usually starting with `data/`.

### Path trees

Each section is a tree of nested mappings with one key per path component:

- Keys are indented by two spaces per level, starting at two spaces directly below the section key. Paths are limited to 16 components.
- A file is a key whose value is its content key.
- A folder is a key without a value; its children follow on the next lines. An empty folder is a key without a value and without children (YAML `null`). Folders only appear in `root_contents`, because GRF files have no folders of their own.
- Entries are sorted by their full path, compared in lowercase. Paths are case-insensitive, but keys keep their original case.
- A key is written as a JSON string (for example `"#file"` or `"2024"`) if it would otherwise be ambiguous in YAML: if it is empty, starts or ends with whitespace, starts with a YAML indicator character, contains `: ` or ` #`, or looks like a number. Readers must accept both plain and quoted keys.
- File names are the real (EUC-KR decoded) Korean names, unless the filebase was created with `-cp1252`.

### Links and duplicates

The format has no explicit links. If several files have the same content key, they have the same content and are stored only once. `ropa assemble` writes the data for the first such path in `grf_contents` and links all later ones to it.

### Reading

A filebase is valid YAML and can be read with any YAML parser. For large clients, a line-based reader is much faster: count the leading spaces to find the depth, and treat a line ending in `:` as a folder. Any other line is a file whose content key follows the final `: `. `ropa assemble` works like this.

The content folder
------------------

The content folder holds one **blob** per distinct content key, plus the integrity map.

### Blobs

- A blob is named `<hash>-<size>.zl` after the content key of the file it contains.
- It holds the file contents as a zlib stream (RFC 1950). The compression level is up to the producer: `ropa filebase -compress` uses zlib for levels 0 to 9 and Zopfli above 9. Files ending in `.mp3` or `.grf` are already compressed and are stored with level 0.
- Blobs are immutable. A producer never rewrites an existing, non-empty blob.
- A blob's raw bytes are exactly what a GRF entry contains. `ropa assemble` copies them into the GRF unchanged, so a GRF entry and the blob it came from have the same stream hash.
- To validate a blob, decompress it and compare the BLAKE3 hash and size with its name. `ropa contentcheck <content folder>` does this and deletes mismatching blobs.

### Integrity map

The file `integrity.yml.gz` in the content folder is a gzip-compressed YAML document:

```yaml
type: ROPAIntegrityMap/v1
s2c:
  86d08f56ccba03a04240c21cbc6a6f8ea143531d439d047ec8234bbea8455678: 8e4c7c1b99dbfd50e7a95185fead5ee1448fa904a2fdd778eaf5f2dbfd629a99
```

`s2c` maps a **stream hash** to a **content hash**. The stream hash is the BLAKE3-256 hash of a blob's raw `.zl` bytes, and the content hash is the hash part of its name. Every run of `ropa filebase -content` loads the existing map and appends entries for new blobs. Existing entries are never changed.

### Suggested use: patching

Hashing the raw stream doesn't require decompression, so the integrity map can make a patcher or launcher considerably faster:

- **Checking a local GRF:** if the local GRF was built from blobs, for example with `ropa assemble`, compute the stream hash of each entry (`ropa checksum -stream`), map it to a content hash through `s2c`, and compare the result with the filebase. Only files whose content key differs need to be fetched.
- **Fetching and verifying:** build the inverse mapping (content hash to stream hash) to know in advance exactly which compressed bytes are expected for a file. A downloaded blob can then be checked before it is decompressed or written into a GRF. The same mapping can serve as an index for repositories or caches that address blobs by stream hash.
- **Avoiding duplicate downloads:** when several paths, or several filebases, refer to the same content key, the blob only has to be fetched once and can be linked or copied locally.
