package couponbuild

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"math/bits"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"kart/internal/promo"
)

// HashJoin is a partitioned hash join written by hand in pure Go.
//
// "Present in at least N files" is a join across files, and 300M+ codes do
// not fit in one in-memory map (~19 GB as map[string]struct{}). So:
//
//  1. Scatter: every candidate code from file i is appended to bucket
//     hash(code) % P for that file. All copies of a code, whichever file they
//     come from, land in the same bucket number.
//  2. Join: each bucket number is processed on its own with a small map
//     (code -> bitmask of files seen). Codes with >= MinSources bits set win.
//
// Peak memory is bounded by the largest bucket, not the total input; disk
// holds the rest temporarily.
type HashJoin struct {
	cfg Config
}

func (h *HashJoin) Build(ctx context.Context, sources []string) ([]string, error) {
	if err := checkSources(sources); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(h.cfg.TempDir, "couponbuild-hashjoin-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	// Phase 1: scatter. One goroutine per source; each owns its own bucket
	// files, so no locking is needed.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make([]error, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if errs[i] = scatter(sctx, src, i, tmp, h.cfg.Partitions); errs[i] != nil {
				cancel() // stop the other readers early
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	// Phase 2: join each bucket independently.
	var valid []string
	for p := 0; p < h.cfg.Partitions; p++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		got, err := joinBucket(tmp, p, len(sources), h.cfg.MinSources)
		if err != nil {
			return nil, err
		}
		valid = append(valid, got...)
	}
	sort.Strings(valid)
	return valid, nil
}

func bucketPath(dir string, source, partition int) string {
	return filepath.Join(dir, fmt.Sprintf("s%02d-p%04d", source, partition))
}

func scatter(ctx context.Context, src string, idx int, dir string, parts int) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	r, err := maybeGunzip(f)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}

	files := make([]*os.File, parts)
	writers := make([]*bufio.Writer, parts)
	for p := range files {
		if files[p], err = os.Create(bucketPath(dir, idx, p)); err != nil {
			return err
		}
		defer files[p].Close()
		writers[p] = bufio.NewWriterSize(files[p], 64<<10)
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var n int
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) < promo.MinLen || len(line) > promo.MaxLen {
			continue // can never be valid; don't spend disk on it
		}
		h := fnv.New32a()
		h.Write(line)
		w := writers[h.Sum32()%uint32(parts)]
		w.Write(line)
		w.WriteByte('\n')
		if n++; n%(1<<20) == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	for _, w := range writers {
		if err := w.Flush(); err != nil {
			return err
		}
	}
	return nil
}

func joinBucket(dir string, p, sources, minSources int) ([]string, error) {
	seen := make(map[string]uint64)
	for s := 0; s < sources; s++ {
		f, err := os.Open(bucketPath(dir, s, p))
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			seen[sc.Text()] |= 1 << s
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	var out []string
	for code, mask := range seen {
		if bits.OnesCount64(mask) >= minSources {
			out = append(out, code)
		}
	}
	return out, nil
}

// maybeGunzip returns a gzip reader if the stream starts with the gzip magic
// bytes, otherwise the raw stream (handy for tests and pre-extracted files).
func maybeGunzip(r io.Reader) (io.Reader, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	magic, err := br.Peek(2)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		return gzip.NewReader(br)
	}
	return br, nil
}
