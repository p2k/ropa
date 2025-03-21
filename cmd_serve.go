//
//  cmd_serve.go
//  ropa
//
//  Created by p2k on 19.03.2025
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
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"ropa/grf"
	"strings"

	"golang.org/x/net/webdav"
)

func pathFromURLPath(path string) string {
	path, _ = strings.CutPrefix(path, "/")
	path, _ = strings.CutSuffix(path, "/")
	if path == "" {
		path = "."
	}
	return path
}

type grfArchiveWebdavAdapter struct {
	ar       *grf.Archive
	compress int
	server   *http.Server
}

func (a *grfArchiveWebdavAdapter) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	return a.ar.Mkdir(pathFromURLPath(name))
}

func (a *grfArchiveWebdavAdapter) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	return a.ar.OpenFile(pathFromURLPath(name), flag, a.compress)
}

func (a *grfArchiveWebdavAdapter) RemoveAll(ctx context.Context, name string) error {
	return a.ar.RemoveAll(pathFromURLPath(name))
}

func (a *grfArchiveWebdavAdapter) Rename(ctx context.Context, oldName, newName string) error {
	if a.ar.Rename(pathFromURLPath(oldName), pathFromURLPath(newName)) {
		return nil
	}
	return os.ErrNotExist
}

func (a *grfArchiveWebdavAdapter) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	return a.ar.Stat(pathFromURLPath(name))
}

func (a *grfArchiveWebdavAdapter) handleClose(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
	w.Write([]byte("closing\n"))
	go a.server.Shutdown(context.Background())
}

// Logging

type serveInterceptor struct {
	base       http.ResponseWriter
	Size       int
	StatusCode int
}

func (i *serveInterceptor) Header() http.Header {
	return i.base.Header()
}

func (i *serveInterceptor) Write(bb []byte) (int, error) {
	i.Size += len(bb)
	return i.base.Write(bb)
}

func (i *serveInterceptor) WriteHeader(statusCode int) {
	i.StatusCode = statusCode
	i.base.WriteHeader(statusCode)
}

func logServeRequest(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := &serveInterceptor{
			base:       w,
			StatusCode: http.StatusOK,
		}
		handler.ServeHTTP(i, r)
		log.Printf("%s %s %s - %d %d\n", r.RemoteAddr, r.Method, r.URL, i.StatusCode, i.Size)
	})
}

// WebDAV via HTTP server

func runServe(grfFilePath string, port, compress int, koreanFilenames, readOnly bool) bool {
	ar, err := grf.Open(grfFilePath, readOnly, koreanFilenames)
	if err != nil {
		if !readOnly && os.IsNotExist(err) {
			ar, err = grf.Create(grfFilePath, koreanFilenames)
		}
		if err != nil {
			fmt.Println("Error while opening GRF file:", err)
			return false
		}
	}
	defer ar.Close()

	ar.AllowHybridFolderEntries = true

	log.SetFlags(log.Ldate | log.Ltime)

	server := &http.Server{Handler: logServeRequest(http.DefaultServeMux)}
	adapter := &grfArchiveWebdavAdapter{ar: ar, compress: compress, server: server}

	handler := &webdav.Handler{
		FileSystem: adapter,
		LockSystem: webdav.NewMemLS(),
	}

	http.HandleFunc("/close!", adapter.handleClose)
	http.Handle("/", handler)

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		log.Println("Error while starting local server:", err)
		return false
	}

	log.Printf("Serving on http://127.0.0.1:%d/\n", port)

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt)
	defer signal.Stop(sc)
	go func() {
		<-sc
		server.Shutdown(context.Background())
	}()

	server.Serve(listener)

	err = ar.Close()
	if err == nil {
		log.Println("Archive closed.")
	} else {
		log.Println("Failed to close archive:", err)
	}

	return true
}
