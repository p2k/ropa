//
//  zopfli.go
//  ropa
//
//  Created by p2k on 14.10.2023
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

// Package zopfli wraps the Zopfli compressor to produce zlib and gzip streams.
package zopfli

/*
#cgo LDFLAGS: -lm
#include <stdlib.h>
#include <string.h>
#include "zopfli.h"
*/
import "C"
import "unsafe"

// Zopfli can't handle empty input, so we use a static result.
const emptyGzip = "\x1f\x8b\x08\x00\xad\xb3\x2a\x65\x02\xff\x03\x00\x00\x00\x00\x00\x00\x00\x00\x00"
const emptyZlib = "\x78\xda\x03\x00\x00\x00\x00\x01"

func compress(input []byte, iterations int, format C.ZopfliFormat, output []byte) []byte {
	var options C.struct_ZopfliOptions
	C.ZopfliInitOptions(&options)

	options.numiterations = C.int(iterations)

	inputSize := C.size_t(len(input))
	if inputSize == 0 {
		if format == C.ZOPFLI_FORMAT_GZIP {
			return []byte(emptyGzip)
		}
		return []byte(emptyZlib)
	}
	inputPtr := (*C.uchar)(unsafe.Pointer(&input[0]))
	var outputPtr *C.uchar
	var outputLength C.size_t

	C.ZopfliCompress(&options, format, inputPtr, inputSize, &outputPtr, &outputLength)
	defer C.free(unsafe.Pointer(outputPtr))

	n := int(outputLength)
	c := cap(output)
	if n <= c {
		output = output[:n]
	} else {
		output = append(output[:c], make([]byte, n-c)...)
	}

	C.memmove(unsafe.Pointer(&output[0]), unsafe.Pointer(outputPtr), outputLength)

	return output
}

// Gzip compresses data with Zopfli using default settings and gzip format.
// The Zopfli library does not return errors, and there are no (detectable)
// failure cases, hence no error return.
//
// The output slice is resized if needed.
func Gzip(input []byte, iterations int, output []byte) []byte {
	return compress(input, iterations, C.ZOPFLI_FORMAT_GZIP, output)
}

// Zlib compresses data with Zopfli using default settings and zlib format.
// The Zopfli library does not return errors, and there are no (detectable)
// failure cases, hence no error return.
//
// The output slice is resized if needed.
func Zlib(input []byte, iterations int, output []byte) []byte {
	return compress(input, iterations, C.ZOPFLI_FORMAT_ZLIB, output)
}
