package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	libraryPath := flag.String("library", "", "path to the compiled plugin library")
	entryName := flag.String("entry", "", "dynamic library name inside the zip")
	archivePath := flag.String("archive", "", "path to the output zip archive")
	checksumPath := flag.String("checksum", "", "path to the output checksum file")
	flag.Parse()

	if *libraryPath == "" || *entryName == "" || *archivePath == "" || *checksumPath == "" {
		fatalf("library, entry, archive, and checksum are required")
	}
	if filepath.Base(*entryName) != *entryName {
		fatalf("entry must be a root-level filename")
	}
	archiveData, errPackage := packageLibrary(*libraryPath, *entryName, *archivePath)
	if errPackage != nil {
		fatalf("%v", errPackage)
	}
	checksum := sha256.Sum256(archiveData)
	line := fmt.Sprintf("%s  %s\n", hex.EncodeToString(checksum[:]), filepath.Base(*archivePath))
	if errWrite := os.WriteFile(*checksumPath, []byte(line), 0o644); errWrite != nil {
		fatalf("write checksum: %v", errWrite)
	}
}

func packageLibrary(libraryPath, entryName, archivePath string) ([]byte, error) {
	library, errOpen := os.Open(libraryPath)
	if errOpen != nil {
		return nil, fmt.Errorf("open library: %w", errOpen)
	}
	defer library.Close()

	if errMkdir := os.MkdirAll(filepath.Dir(archivePath), 0o755); errMkdir != nil {
		return nil, fmt.Errorf("create archive directory: %w", errMkdir)
	}
	archive, errCreate := os.Create(archivePath)
	if errCreate != nil {
		return nil, fmt.Errorf("create archive: %w", errCreate)
	}
	archiveClosed := false
	defer func() {
		if !archiveClosed {
			_ = archive.Close()
		}
	}()

	writer := zip.NewWriter(archive)
	header := &zip.FileHeader{Name: entryName, Method: zip.Deflate}
	header.SetMode(0o755)
	entry, errEntry := writer.CreateHeader(header)
	if errEntry != nil {
		return nil, fmt.Errorf("create zip entry: %w", errEntry)
	}
	if _, errCopy := io.Copy(entry, library); errCopy != nil {
		return nil, fmt.Errorf("copy library: %w", errCopy)
	}
	if errClose := writer.Close(); errClose != nil {
		return nil, fmt.Errorf("close zip writer: %w", errClose)
	}
	if errClose := archive.Close(); errClose != nil {
		return nil, fmt.Errorf("close archive: %w", errClose)
	}
	archiveClosed = true

	data, errRead := os.ReadFile(archivePath)
	if errRead != nil {
		return nil, fmt.Errorf("read archive: %w", errRead)
	}
	return data, nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
