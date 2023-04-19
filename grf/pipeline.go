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
	"io"
)

type pipelineWorker struct {
	in    *bytes.Buffer
	out   *bytes.Buffer
	err   error
	name  string
	lname string
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
		out:   &bytes.Buffer{},
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
	w, err := zlib.NewWriterLevel(pw.out, compress)
	if err != nil {
		panic(err)
	}
	for range pw.ready {
		if pw.lname == "" && pw.flags != 0 {
			pw.out.Reset()
			w.Reset(pw.out)
			_, pw.err = io.Copy(w, pw.in)
			if pw.err == nil {
				pw.err = w.Close()
			}
		}
		pw.done <- true
	}
}

// Pipeline offers a way to write files to an Archive in parallel.
//
// As long as the pipeline is active, the Archive should not be touched by other
// means. The input methods should also not be called from more than one thread.
type Pipeline struct {
	err error

	workers    []*pipelineWorker
	nocompress bool
	size       int
	inp        int

	allDone chan bool
}

// NewPipeline creates a new pipeline with the given archive and compression
// setting.
func NewPipeline(ar *Archive, size, compress int) *Pipeline {
	p := &Pipeline{
		nocompress: compress == 0,
		size:       size,
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
							_, pw.err = ar.RawPut(pw.name, pw.out, uint32(pw.size), pw.flags)
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
	if compress && !p.nocompress {
		pw.flags = FileFlag
		pw.in.Reset()
		pw.size, pw.err = io.Copy(pw.in, r)
	} else {
		pw.flags = 0
		pw.out.Reset()
		pw.size, pw.err = io.Copy(pw.out, r)
	}
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
