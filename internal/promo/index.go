package promo

import (
	"bufio"
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

// LoadIndexFile reads an index artifact from disk.
func LoadIndexFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadIndex(f)
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
