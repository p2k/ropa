package main

import (
	"encoding/binary"
	"fmt"
	"github.com/p2k/ropa/grf"
	"os"
	"os/signal"

	"github.com/schollz/progressbar/v3"
)

func loadSetEncryptionKeyWithFile(keyFilePath string, addMissingInfo bool, ar *grf.Archive) bool {
	if keyFilePath == "" {
		return true
	}

	key, err := os.ReadFile(keyFilePath)
	if err != nil {
		fmt.Println("Error while reading key file:", err)
		return false
	}

	if len(key) != 256 && len(key) != 260 {
		fmt.Println("Error: invalid key file length")
		return false
	}

	if len(key) == 260 {
		// GRF Editor key file format which has a random 32bit number prefix used to
		// further obscure the key.
		random := int(binary.LittleEndian.Uint32(key))
		key = key[4:]
		for i := range 256 {
			key[i] ^= byte(random & 0xFF)
			random *= 17
		}
	}

	err = ar.SetGRFEditorEncryptionKey(key, addMissingInfo)
	if err != nil {
		fmt.Println("Error while setting encryption key:", err)
		return false
	}

	return true
}

func runCrypt(grfFilePath, keyFilePath string, encrypt, quiet bool) bool {
	ar, err := grf.Open(grfFilePath, false, true)
	if err != nil {
		fmt.Println("Error while opening GRF file:", err)
		return false
	}
	defer ar.Close()

	if !loadSetEncryptionKeyWithFile(keyFilePath, encrypt, ar) {
		return false
	}

	if !encrypt {
		ar.GRFEditorEncryptionEnabled = false
	}

	totalCompressed := ar.TotalCompressedSize()

	var bar *progressbar.ProgressBar
	if !quiet {
		description := "Encrypting"
		if !encrypt {
			description = "Decrypting"
		}
		bar = progressbar.NewOptions64(
			totalCompressed,
			progressbar.OptionSetDescription(description),
			progressbar.OptionFullWidth(),
			progressbar.OptionShowBytes(true),
			progressbar.OptionSetRenderBlankState(true),
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        "=",
				SaucerHead:    ">",
				SaucerPadding: " ",
				BarStart:      "[",
				BarEnd:        "]",
			}))
	} else if encrypt {
		fmt.Println("Encrypting...")
	} else {
		fmt.Println("Decrypting...")
	}

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt)
	defer signal.Stop(sc)

	stop := false
	for _, name := range ar.AllFileNames() {
		select {
		case <-sc:
			stop = true
		default:
		}
		if stop {
			break
		}

		if name == grf.EncryptionInfoFileName {
			continue
		}

		h := ar.Head(name)
		if h.Linkname != "" {
			continue
		}

		err := ar.EnsureEncryptionStatus(name)
		if err != nil {
			fmt.Printf("Error while processing file %s: %v\n", name, err)
			break
		}

		if bar != nil {
			bar.Add64(int64(h.Size))
		}
	}

	if !encrypt {
		ar.Delete(grf.EncryptionInfoFileName)
	}

	err = ar.Close()
	if err != nil {
		fmt.Println("Error while finalizing GRF:", err)
		return false
	}

	if bar != nil {
		bar.Finish()
		bar.Exit()
		fmt.Println()
	}
	fmt.Println("All done.")
	return true
}
