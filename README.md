
ROPA
====

Usage: `ropa help`

Compiling
---------

### liblzma

To get liblzma static on Windows:

- Download xz-utils from https://tukaani.org/xz/
- `mkdir build`
- `cmake -GNinja ..`
- `ninja`
- Copy liblzma.a to MinGW's lib dir
- Copy src/liblzma/api/lzma.h and the lzma folder to MinGW's include dir
