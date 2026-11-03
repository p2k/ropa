//
//  cmd_beamsonly.go
//  ropa
//
//  Created by p2k on 15.04.2023
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
	"encoding/base64"
	"fmt"
	"ropa/grf"
	"strings"
)

const silenceWavZBase64 = "eNoL8nRzK2JlYAh3DHNNyy1REGBgYGAEQqUwBgaXNQwMTAwCDD6ewSFSQHFPPzd/z2C3ED4g2yexLM3UQs/IUs/QwIAhJbEkUYeVgQgwCkbBKBgFowAA9mEOpA=="
const silenceWavSize = 1402

func runBeamsonly(grfFilePaths []string, beamsonlyGRFPath, keyFilePath string) bool {
	silence := make(map[string]bool)
	for _, path := range grfFilePaths {
		fmt.Println("Loading", path, "...")
		ar, err := grf.Open(path, true, true)
		if err != nil {
			fmt.Println("Error:", err)
			return false
		}
		for _, fn := range ar.AllFileNames() {
			fn = strings.ToLower(fn)
			if strings.HasSuffix(fn, ".wav") && !keep[fn] {
				silence[fn] = true
			}
		}
		ar.Close()
	}

	fmt.Println("Creating", beamsonlyGRFPath, "...")
	ar, err := grf.Create(beamsonlyGRFPath, true)
	if err != nil {
		fmt.Println("Error:", err)
		return false
	}

	if !loadSetEncryptionKeyWithFile(keyFilePath, true, ar) {
		return false
	}

	silenceWavZ, err := base64.StdEncoding.DecodeString(silenceWavZBase64)
	if err != nil {
		panic(err)
	}

	var h *grf.FileHeader
	for fn := range silence {
		if h == nil {
			h, err = ar.RawPutBytes(fn, silenceWavZ, silenceWavSize, grf.FileFlag)
			if err != nil {
				ar.Close()
				fmt.Println("Error:", err)
				return false
			}
		} else {
			ar.LinkHeader(fn, h)
		}
	}
	err = ar.Close()
	if err != nil {
		fmt.Println("Error:", err)
		return false
	}

	fmt.Println("Silenced", len(silence), "wav files.")
	return true
}
