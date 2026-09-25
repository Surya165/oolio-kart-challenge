package promo

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The index is the precomputed set of valid codes. Two concerns are kept
// apart:
//
//   - Format: plain text, one code per line, optional '#' comment lines.
//     ReadIndex/WriteIndex work on any io.Reader/io.Writer.
//   - Location: where the index lives. The server loads it through an
//     IndexSource and the builder publishes it through an IndexSink, so a
//     file today can become an S3 object (or anything else) tomorrow without
//     touching either program's logic.

// IndexSource is where the server loads the valid-code set from.
type IndexSource interface {
	Load(ctx context.Context) ([]string, error)
}

// IndexSink is where the builder publishes a new valid-code set. Publish must
// be atomic: readers see either the previous index or the new one, never a
// partial write.
type IndexSink interface {
	Publish(ctx context.Context, codes []string, header string) error
}

// ErrEmptyIndex is returned when an index contains no codes. A broken build
// that produced an empty index would otherwise make the server silently
// reject every promo code, so loading one is treated as an error.
var ErrEmptyIndex = errors.New("coupon index contains no codes")

// FileIndex stores the index as a file on local disk.
type FileIndex struct {
	Path string
}

var (
	_ IndexSource = FileIndex{}
	_ IndexSink   = FileIndex{}
)

func (f FileIndex) String() string { return f.Path }

// Load reads and parses the index file.
func (f FileIndex) Load(_ context.Context) ([]string, error) {
	file, err := os.Open(f.Path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	codes, err := ReadIndex(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Path, err)
	}
	return codes, nil
}

// Publish writes the index to a temp file in the same directory and renames
// it into place. A rename within one filesystem is atomic, so a server
// reloading concurrently never sees a half-written index.
func (f FileIndex) Publish(_ context.Context, codes []string, header string) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), filepath.Base(f.Path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(tmp.Name()) // don't leave junk behind on failure
		}
	}()
	if err = WriteIndex(tmp, codes, header); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp.Name(), 0o644); err != nil { // CreateTemp uses 0600
		return err
	}
	return os.Rename(tmp.Name(), f.Path)
}

// ReadIndex parses an index: one code per line, blank lines and lines
// starting with '#' ignored.
func ReadIndex(r io.Reader) ([]string, error) {
	var codes []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !ValidLength(line) {
			return nil, fmt.Errorf("index contains code %q outside length %d-%d", line, MinLen, MaxLen)
		}
		codes = append(codes, line)
	}
	return codes, sc.Err()
}

// WriteIndex writes codes in the format read by ReadIndex.
func WriteIndex(w io.Writer, codes []string, header string) error {
	bw := bufio.NewWriter(w)
	for _, line := range strings.Split(strings.TrimSpace(header), "\n") {
		if line != "" {
			fmt.Fprintf(bw, "# %s\n", line)
		}
	}
	for _, c := range codes {
		bw.WriteString(c)
		bw.WriteByte('\n')
	}
	return bw.Flush()
}
