//
//  pipeline.go
//  ropa
//
//  Created by p2k on 19.04.2023
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

package grf

import (
	"bytes"
	"compress/zlib"
	"hash"
	"io"
	"ropa/zopfli"

	"lukechampine.com/blake3"
)

type pipelineWorker struct {
	in    *bytes.Buffer
	inh   FileHeaderHash
	out   []byte
	outp  int
	err   error
	name  string
	lname string
	raw   bool
	size  int64
	flags uint8

	empty chan bool
	ready chan bool
	done  chan bool

	dead bool // when empty gets closed
	exit bool // when done gets closed
}

func newPipelineWorker(compress int) *pipelineWorker {
	pw := &pipelineWorker{
		in:    &bytes.Buffer{},
		out:   make([]byte, 0, 65536),
		empty: make(chan bool, 1),
		ready: make(chan bool),
		done:  make(chan bool, 1),
	}
	pw.empty <- true
	go pw.work(compress)
	return pw
}

func (pw *pipelineWorker) work(compress int) {
	defer close(pw.done)
	if compress < 10 {
		w, err := zlib.NewWriterLevel(pw, compress)
		if err != nil {
			panic(err)
		}
		for range pw.ready {
			if pw.lname == "" && !pw.raw && pw.flags != 0 && pw.err == nil {
				pw.outp = 0
				w.Reset(pw)
				_, pw.err = io.Copy(w, pw.in)
				if pw.err == nil {
					pw.err = w.Close()
				}
			}
			pw.done <- true
		}
	} else {
		for range pw.ready {
			if pw.lname == "" && !pw.raw && pw.flags != 0 && pw.err == nil {
				pw.out = zopfli.Zlib(pw.in.Bytes(), compress, pw.out)
			}
			pw.done <- true
		}
	}
}

func (pw *pipelineWorker) Write(p []byte) (n int, err error) {
	n = len(p)
	l := pw.outp + n
	c := cap(pw.out)
	if l <= c {
		pw.out = pw.out[:l]
		copy(pw.out[pw.outp:l], p)
	} else if len(pw.out) < c {
		pw.out = pw.out[:c]
		d := copy(pw.out[pw.outp:c], p)
		pw.out = append(pw.out, p[d:]...)
	} else {
		pw.out = append(pw.out, p...)
	}
	pw.outp += n
	return
}

// Pipeline offers a way to write files to an Archive in parallel.
//
// Input data is fully copied into memory before performing compression or raw
// writing. One buffer is maintained per worker.
//
// As long as the pipeline is active, the Archive should not be touched by other
// means. The input methods should also not be called from more than one thread.
type Pipeline struct {
	err error

	workers    []*pipelineWorker
	nocompress bool
	size       int
	inp        int
	hs         hash.Hash

	processed int64
	pc        chan<- int64
	allDone   chan bool
}

// NewPipeline creates a new pipeline with the given archive and compression
// setting.
//
// Values for compress > 9 will use Zopfli compression with the given number of
// iterations; 15 is the recommended default for this.
//
// If the given archive is read only, this will produce errors in all uses.
func NewPipeline(ar *Archive, size int, compress int) *Pipeline {
	p := &Pipeline{
		nocompress: compress == 0,
		size:       size,
		hs:         blake3.New(32, nil),
		allDone:    make(chan bool),
	}
	for i := 0; i < p.size; i++ {
		p.workers = append(p.workers, newPipelineWorker(compress))
	}
	// Writer routine
	go func() {
		n := p.size
		outp := 0
		for n > 0 {
			pw := p.workers[outp]
			outp = (outp + 1) % p.size
			if !pw.exit {
				_, ok := <-pw.done
				if ok {
					if pw.err == nil {
						if pw.lname != "" {
							_, pw.err = ar.Link(pw.name, pw.lname)
						} else {
							var h *FileHeader
							h, pw.err = ar.RawPutBytes(pw.name, pw.out, uint32(pw.size), pw.flags)
							if !pw.inh.IsZero() {
								h.Hash = pw.inh
							}
							p.processed += pw.size
							if p.pc != nil {
								p.pc <- p.processed
							}
						}
						if pw.err == nil {
							pw.empty <- true
						}
					}
					if pw.err != nil {
						p.err = pw.err
						for _, ww := range p.workers {
							close(ww.empty)
						}
					}
				} else {
					pw.exit = true
					n--
				}
			}
		}
		close(p.allDone)
	}()
	return p
}

// SetProgressCh sets a channel to report progress in bytes.
//
// Calling this while the pipeline is running will likely crash.
func (p *Pipeline) SetProgressCh(pc chan<- int64) {
	p.pc = pc
}

func (p *Pipeline) nextEmptyWorker() *pipelineWorker {
	n := p.size
	for n > 0 {
		pw := p.workers[p.inp]
		p.inp = (p.inp + 1) % p.size
		if !pw.dead {
			_, ok := <-pw.empty
			if ok {
				return pw
			} else {
				pw.dead = true
				n--
			}
		} else {
			n--
		}
	}
	return nil
}

// Link queues the linking of a file to another file.
//
// Blocks until a worker is available. Returns `false` if the pipeline is broken
// and accepts no more jobs.
func (p *Pipeline) Link(name string, linkname string) bool {
	pw := p.nextEmptyWorker()
	if pw == nil {
		return false
	}
	pw.name = name
	pw.lname = linkname
	pw.raw = false
	pw.ready <- true
	return true
}

// Put queues the insertion or replacement of a file.
//
// Will buffer the entire file in memory. Blocks until a worker is available.
// Returns `false` if the pipeline is broken and accepts no more jobs.
func (p *Pipeline) Put(name string, r io.Reader, compress bool) bool {
	pw := p.nextEmptyWorker()
	if pw == nil {
		return false
	}
	pw.name = name
	pw.lname = ""
	pw.raw = false
	if compress && !p.nocompress {
		pw.flags = FileFlag
		pw.in.Reset()
		pw.size, pw.err = io.Copy(pw.in, io.TeeReader(r, p.hs))
		pw.inh.SetWithHasher(p.hs)
		p.hs.Reset()
	} else {
		pw.flags = 0
		pw.outp = 0
		pw.size, pw.err = io.Copy(pw, r)
		pw.inh.SetZero()
	}
	pw.ready <- true
	return true
}

// RawPut queues the insertion or replacement of a compressed file directly.
//
// The given data stream needs to already be compressed correctly.
// Will buffer the entire file in memory. Blocks until a worker is available.
// Returns `false` if the pipeline is broken and accepts no more jobs.
func (p *Pipeline) RawPut(name string, uncompressedSize int64, r io.Reader) bool {
	pw := p.nextEmptyWorker()
	if pw == nil {
		return false
	}
	pw.name = name
	pw.lname = ""
	pw.raw = true
	pw.flags = FileFlag
	pw.outp = 0
	_, pw.err = io.Copy(pw, r)
	pw.size = uncompressedSize
	pw.inh.SetZero()
	pw.ready <- true
	return true
}

// Wait until all operations are done and return final result.
//
// Calls to `Link` or `Put` after this by other goroutines will lead to a panic.
func (p *Pipeline) Wait() error {
	for _, pw := range p.workers {
		close(pw.ready)
	}
	<-p.allDone
	return p.err
}
