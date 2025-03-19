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
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"ropa/grf"
	"strings"

	"golang.org/x/net/webdav"
)

// File adapter

type grfFileWebdavAdapter struct {
	ar   *grf.Archive
	name string
	af   *grf.ArchivedFile
}

func (a *grfFileWebdavAdapter) Read(p []byte) (int, error) {
	return a.af.Read(p)
}

func (a *grfFileWebdavAdapter) Write(p []byte) (n int, err error) {
	return 0, os.ErrInvalid
}

func (a *grfFileWebdavAdapter) Seek(offset int64, whence int) (int64, error) {
	if offset == 0 && whence == io.SeekStart {
		return 0, a.af.Reset()
	} else if offset == 0 && whence == io.SeekEnd {
		if err := a.Close(); err != nil {
			return 0, err
		}
		if fi, err := a.af.Stat(); err == nil {
			return fi.Size(), nil
		}
		return 0, nil // Shouldn't happen
	}
	return 0, os.ErrInvalid
}

// Archive adapter

func (a *grfFileWebdavAdapter) Close() error {
	return a.af.Close()
}

func (a *grfFileWebdavAdapter) Readdir(count int) ([]fs.FileInfo, error) {
	entries, err := a.ar.ReadDir(a.name)
	if err != nil {
		return nil, err
	}
	infos := make([]fs.FileInfo, len(entries))
	for i, entry := range entries {
		infos[i], _ = entry.Info()
	}
	return infos, nil
}

func (a *grfFileWebdavAdapter) Stat() (fs.FileInfo, error) {
	return a.af.Stat()
}

type grfArchiveWebdavAdapter struct {
	ar *grf.Archive
}

func (a *grfArchiveWebdavAdapter) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	return os.ErrPermission
}

func (a *grfArchiveWebdavAdapter) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	name, _ = strings.CutPrefix(name, "/")
	name, _ = strings.CutSuffix(name, "/")
	if name == "" {
		name = "."
	}
	af, err := a.ar.Open(name)
	if err != nil {
		return nil, err
	}
	return &grfFileWebdavAdapter{a.ar, name, af.(*grf.ArchivedFile)}, nil
}

func (a *grfArchiveWebdavAdapter) RemoveAll(ctx context.Context, name string) error {
	return os.ErrPermission
}

func (a *grfArchiveWebdavAdapter) Rename(ctx context.Context, oldName, newName string) error {
	return os.ErrPermission
}

func (a *grfArchiveWebdavAdapter) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	name, _ = strings.CutPrefix(name, "/")
	name, _ = strings.CutSuffix(name, "/")
	if name == "" {
		name = "."
	}
	return a.ar.Stat(name)
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

func runServe(grfFilePath string, port int, koreanFilenames bool) bool {
	ar, err := grf.Open(grfFilePath, true, koreanFilenames)
	if err != nil {
		fmt.Println("Error while opening GRF file:", err)
		return false
	}
	defer ar.Close()

	ar.AllowHybridFolderEntries = true

	log.SetFlags(log.Ldate | log.Ltime)

	handler := &webdav.Handler{
		FileSystem: &grfArchiveWebdavAdapter{ar: ar},
		LockSystem: webdav.NewMemLS(),
	}

	http.Handle("/", handler)

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		fmt.Println("Error while starting local server:", err)
		return false
	}

	fmt.Printf("Serving on http://127.0.0.1:%d/\n", port)

	server := &http.Server{Handler: logServeRequest(http.DefaultServeMux)}
	server.Serve(listener)

	return true
}
