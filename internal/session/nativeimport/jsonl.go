package nativeimport

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
)

// readRecords calls fn with each newline-terminated record of a regular file.
// A final record without a newline is still delivered: native tools flush
// whole lines, and the last one may simply lack the terminator. The file must
// be a regular file, never a FIFO or device that could block forever.
func readRecords(path string, fn func(lineNo int, line []byte) error) (os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	rd := bufio.NewReaderSize(f, 64*1024)
	lineNo := 0
	for {
		var line []byte
		var readErr error
		for {
			part, e := rd.ReadSlice('\n')
			if len(line)+len(part) > maxRecordBytes {
				return info, fmt.Errorf("record exceeds %d bytes at line %d", maxRecordBytes, lineNo+1)
			}
			line = append(line, part...)
			if errors.Is(e, bufio.ErrBufferFull) {
				continue
			}
			readErr = e
			break
		}
		if len(line) > 0 {
			lineNo++
			if err := fn(lineNo, line); err != nil {
				return info, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return info, nil
		}
		if readErr != nil {
			return info, readErr
		}
	}
}
