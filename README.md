# Kart Challenge: Food Ordering API in Go

An implementation of Oolio's [food-ordering OpenAPI spec](https://github.com/oolio-group/kart-challenge/blob/b45fbd09b5499d4a2be823d399f6f494c6fbae8d/api/openapi.yaml) The API server uses only the Go standard library (`net/http` with Go 1.22+ routing, `log/slog`) and has no third-party dependencies. The offline coupon-index builder has two interchangeable engines, and one of them uses embedded DuckDB.

## Quick start

```bash
docker compose up --build            # API on http://localhost:8080

curl localhost:8080/api/product
curl localhost:8080/api/product/1
curl -X POST localhost:8080/api/order \
  -H 'api_key: apitest' -H 'Content-Type: application/json' \
  -d '{"couponCode":"HAPPYHRS","items":[{"productId":"1","quantity":2}]}'
```

Without Docker: `go run ./cmd/server` for the server and `go test -race ./...` for the tests.

## Endpoints

| Method | Path | Auth | Responses |
|---|---|---|---|
| GET | `/api/product` | none | 200 |
| GET | `/api/product/{productId}` | none | 200, 400 (not a positive int64), 404 |
| POST | `/api/order` | `api_key` header | 200, 400, 401, 403, 422 |
| GET | `/healthz` | none | 200 (liveness probe, not in spec) |

Every error body uses the spec's `ApiResponse` shape: `{"code": 422, "type": "validation_error", "message": "couponCode: invalid promo code"}`.

**Status code choices.** The spec lists these codes but doesn't say when to use each one, so these are my decisions:
- **400**: the body isn't valid JSON for `OrderReq`. That covers malformed or empty bodies, trailing data, wrong JSON types, and bodies over 1 MiB.
- **401**: the `api_key` header is missing, so the caller isn't authenticated.
- **403**: an `api_key` is present but wrong. Keys are compared in constant time.
- **422**: the JSON is well formed but breaks a business rule. That covers no items, a missing `productId` or `quantity`, a quantity below 1, an unknown product, more than 100 lines, or an invalid promo code.

## Promo codes: the core of the problem

### What the data actually looks like

I inspected the files before designing anything:

| File | Compressed | Uncompressed | Lines | Line lengths |
|---|---|---|---|---|
| couponbase1.gz | 625 MiB | 921 MiB | 107,260,777 | all 8 chars |
| couponbase2.gz | 695 MiB | 1,023 MiB | 107,260,776 | 9 chars, plus 8 codes of 8 chars |
| couponbase3.gz | 704 MiB | 1,034 MiB | 98,566,152 | 10 chars, plus 8 codes of 8 chars |

All lines are `[A-Z0-9]`. Since each file uses a single length (plus a handful of planted codes), a code can only appear in two files if it is one of those planted 8-character codes. Out of 313M lines, exactly **8 codes are valid**: `BIRTHDAY BUYGETON FIFTYOFF FREEZAAA GNULINUX HAPPYHRS OVER9000 SIXTYOFF`. `MOODYHRS` (file 2 only) and `SUPER100` (file 1 only) are decoys.

So validation is really a large join done once to produce a tiny result, followed by lookups against that result. The design separates the two.

### Design: build offline, serve from memory

```
couponbase{1,2,3}.gz ──► cmd/couponindex ──► data/valid_coupons.txt ──► server (in-memory set)
      2 GB, 313M lines       once, ~2 min           8 lines, ~200 bytes        O(1), lock-free
```

**Build (`cmd/couponindex`, `internal/couponbuild`).** "Present in at least 2 files" is a join across files, and 313M strings don't fit in one map. A Go `map[string]struct{}` costs about 61 bytes per entry (I measured this), so that would be around 19 GB. There are two engines behind one `couponbuild.Builder` interface, and the factory `couponbuild.New(engine, cfg)` picks one (`-engine hashjoin|duckdb`).

*`hashjoin` (default, pure Go)* is a partitioned hash join written by hand:

1. **Scatter:** one goroutine per source file streams and gunzips it. It drops any code outside 8–10 characters and appends the rest to bucket `fnv32(code) % P` for that file. Every copy of a code, from any file, lands in the same bucket number.
2. **Join:** for each bucket, it loads the corresponding pieces from each file into a `map[code]bitmask` and keeps codes with at least 2 bits set. Duplicates within one file set the same bit, so they correctly count once.

Peak memory is bounded by the largest bucket, not by the total input, and `-partitions` trades memory against the number of open files. The temporary disk space needed is roughly the size of the uncompressed candidates (about 3 GB). On the real data, on a 4-core ARM VM with `-partitions 128`, the build took **2m16s at 257 MB peak RSS**. Output is written to a temp file and then renamed, so a reader never sees a half-written index.

*`duckdb`* expresses the same join as one SQL query over the raw files, run by embedded DuckDB (no server):

```sql
SELECT code FROM (
  SELECT trim(code) AS code, 1::UBIGINT AS bit FROM read_csv('couponbase1.gz', ...)
  UNION ALL SELECT trim(code), 2 FROM read_csv('couponbase2.gz', ...)
  UNION ALL SELECT trim(code), 4 FROM read_csv('couponbase3.gz', ...)
) WHERE strlen(code) BETWEEN 8 AND 10 AND hash(code) % $passes = $k
GROUP BY code HAVING bit_count(bit_or(bit)) >= 2
```

This is the same bitmask idea, with each file contributing one bit. DuckDB runs the `GROUP BY` as a parallel hash aggregate and spills to disk under memory pressure, so it's the hand-written algorithm handed to a query engine. `-partitions N` splits the query into N passes by `hash(code)`, which caps memory and spill at the cost of re-reading the input on each pass.

Both engines implement the rule as written and don't rely on the length pattern above. A single conformance suite runs every engine through the factory against the same fixtures, so they can't drift apart.

**Measured on the real files** (4-core ARM VM, 4 GB RAM, 6 GB free disk):

| Engine | Setting | Result | Wall time | Peak RSS | Peak temp disk |
|---|---|---|---|---|---|
| hashjoin | 128 buckets | 8 codes | 2m16s | 257 MB | ~3 GB |
| duckdb | 1 pass, 1–2.2 GB memory limit | out of disk | — | 2.4 GB | >5.7 GB |
| duckdb | 4 passes, 1 GB memory limit | same 8 codes | ~9m40s (4 × 145s) | 1.3 GB | ~2.9 GB per pass |

On this machine, the hand-written engine wins clearly because it knows exactly what it needs to keep (the key plus a few bits) and reads the input once. DuckDB's spilled rows carry a hash and aggregate state, so a single pass needs about twice the temp disk, and splitting into passes costs one full read of the input per pass. DuckDB's advantage is flexibility. A rule change such as "at least 2 files, excluding file 3" or "expires after date X" is a one-line SQL edit instead of new Go code, and on a machine with more RAM a single pass would do much better. The cgo dependency is isolated: only `cmd/couponindex` imports `internal/couponbuild`, and the API server binary (8.8 MB, static) contains no DuckDB code.

(The DuckDB numbers come from running the exact query generated by `buildQuery` through DuckDB 1.5.5's Python client, the same engine version as the Go driver, because the VM had no Go toolchain.)

**Serve (`internal/promo/validator.go`).** The server loads the artifact at startup in under 1 ms and keeps about 1 KB in memory. The `promo.Validator` interface is the only thing the order service sees. `SetValidator` holds an immutable map behind an `atomic.Pointer`. Lookups are lock-free, and `Replace` publishes a new set atomically. Sending `SIGHUP` to the server reloads the index file without a restart; if the reload fails, the old index keeps serving.

**Matching is exact and case-sensitive.** All source codes are uppercase. I treat codes as identifiers and don't normalise them, so `happyhrs` and ` HAPPYHRS ` are rejected. If the product wants to be lenient, it's a one-line change in `Valid`, but it should be a deliberate product decision, not a side effect.

### Alternatives considered

| Approach | Memory | Cold start | Why not |
|---|---|---|---|
| Scan the files per request | ~0 | 0 | Minutes per request. |
| Load everything into a map at startup | ~19 GB | minutes | Paid on every replica and every restart. |
| Sorted `[]uint64` (base-36 packed) at startup | ~2.5 GB | minutes | Better, but still does the offline job online. |
| Bloom filter per file at startup | ~375 MB at 1% FPR | minutes | A false positive means granting a discount on an invalid code. |
| Put codes in Postgres/Redis | external | 0 | Adds a network hop and a shared dependency for read-only data that fits in 1 KB. |
| Load files into Postgres, join in SQL | external | 0 | Works, but means loading 313M rows (roughly 10–15 GB) into a server to produce 8 lines; embedded DuckDB gets the SQL benefit without a server. |
| **Offline build + tiny artifact** | **~1 KB** | **<1 ms** | Needs a build step whenever the coupon files change. |

## Scaling

**Current state.** The server is stateless apart from the in-memory order repository (see below). A coupon check is one map lookup. The product catalogue is an immutable in-memory slice and map. There is no lock on any read path.

**At 10x.** Run more replicas behind a load balancer. Each replica ships the same image, including the index, so there's nothing to coordinate. The first real change is moving orders into a database (`order.Repository` is already an interface), because in-memory orders are lost on restart and aren't shared between replicas.

**At 100x.**
- **Orders:** a write-optimised store, partitioned by order ID, with idempotency keys so client retries don't create duplicate orders.
- **Coupon refresh:** publish the artifact as a versioned object (for example on S3). Replicas poll for it or are notified, then download and call `Replace`, with no redeploy. The build runs as a batch job whenever new source files land, and it parallelises naturally by bucket if the input grows.
- **Coupons as mutable state:** if the business adds expiry, per-customer limits or redemption counts, coupons become transactional state, and that belongs in a database with atomic decrements. That would be a new `promo.Validator` (or a richer `promo.Redeemer`) implementation, and handlers wouldn't change. A database is justified by mutability, not by the size of the raw files.
- **Catalogue:** a DB-backed `catalog.Store` with a short-TTL in-process cache, since menus are read-heavy and change rarely.

## Project layout

```
cmd/server          HTTP server: config, wiring, graceful shutdown, SIGHUP reload
cmd/couponindex     offline index builder CLI (-engine hashjoin|duckdb)
internal/couponbuild  Builder interface + factory; hashjoin and duckdb engines
internal/promo      Validator interface, in-memory SetValidator, index file format
internal/order      order-placement use case, validation rules, Repository
internal/catalog    Product model, Store interface, embedded menu
internal/httpapi    thin handlers, auth, error mapping, logging/recovery middleware
data/               valid_coupons.txt (committed); *.gz sources are gitignored
```

Handlers only decode input, call a service, and map errors to status codes. Business rules live in `internal/order`, which has no HTTP imports.

## Rebuilding the coupon index

```bash
# download the three files into ./data (links in the challenge README), then either:
go run ./cmd/couponindex -out data/valid_coupons.txt data/couponbase1.gz data/couponbase2.gz data/couponbase3.gz
go run ./cmd/couponindex -engine duckdb -partitions 4 -out data/valid_coupons.txt data/couponbase*.gz
# or, without Go installed:
docker compose --profile index run --rm couponindex
ENGINE=duckdb docker compose --profile index run --rm couponindex
```

## Testing

`go test -race ./...` covers:
- **Builder conformance (table-driven, run against every engine via the factory):** length boundaries (7/8/10/11 characters), presence in one, two or three files, `MinSources` 1–4, duplicates within one file not counting twice, case preserved, CRLF line endings, mixed gzip and plain input, a path containing a quote, a missing source file, and results that don't change with the partition count (1/3/16).
- **Validator:** exact match, case variants, whitespace, length bounds, and atomic replace.
- **HTTP:** every status code on every endpoint, the `ApiResponse` error shape, response shape (UUID id, items echoed, products de-duplicated), and auth.

## Left out, and why

- **Pricing and discounts:** the spec's `Order` schema has no total or discount fields, so the coupon is validated but no discount amount is computed. I didn't invent response fields the spec doesn't define.
- **Persistent orders:** there's no endpoint to read orders back, so in-memory storage behind an interface is enough to demonstrate the boundary.
- **Product images:** the spec's `Product` schema has only id, name, price and category. `products.json` holds the challenge menu. The demo server was unreachable while I built this, so the menu should be checked against it.
- **Rate limiting, metrics, tracing:** they belong in a real deployment (probably at the gateway) but add noise to a take-home.
- **Frontend:** optional, and skipped in favour of polishing the backend.
