package promo

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// The index artifact is plain text: one valid code per line, with optional
// '#' comment lines at the top describing how it was built. Plain text keeps
// it diffable in git and inspectable with cat.

// ReadIndex parses an index artifact: one code per line, blank lines and
// lines starting with '#' ignored.
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

// ErrEmptyIndex is returned when an index file contains no codes. A broken
// build that writes an empty index would otherwise make the server silently
// reject every promo code, so loading it is treated as an error.
var ErrEmptyIndex = errors.New("coupon index contains no codes")

// LoadIndexFile reads an index artifact from disk for serving. It fails on an
// empty index; see ErrEmptyIndex.
func LoadIndexFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	codes, err := ReadIndex(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(codes) == 0 {
		return nil, fmt.Errorf("%s: %w", path, ErrEmptyIndex)
	}
	return codes, nil
}

// WriteIndex writes codes in the artifact format read by ReadIndex.
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
