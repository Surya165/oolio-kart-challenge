# Oolio Kart Challenge

A Go implementation of Oolio's [food-ordering OpenAPI spec](https://github.com/oolio-group/kart-challenge/blob/b45fbd09b5499d4a2be823d399f6f494c6fbae8d/api/openapi.yaml). The API server uses only the standard library.

## Run

```bash
docker compose up --build        # http://localhost:8080

curl localhost:8080/api/product
curl -X POST localhost:8080/api/order -H 'api_key: apitest' \
  -d '{"couponCode":"HAPPYHRS","items":[{"productId":"1","quantity":2}]}'
```

Tests: `go test ./...`

## API

| Endpoint | Notes |
|---|---|
| `GET /api/product` | List products |
| `GET /api/product/{id}` | 400 if the id isn't a positive integer, 404 if not found |
| `POST /api/order` | Needs `api_key` header: 401 if missing, 403 if wrong. 400 for bad JSON, 422 for invalid items or promo code |

Errors use the spec's `ApiResponse` shape (`code`, `type`, `message`).

## Promo codes

A code is valid if it's 8–10 characters long and appears in at least two of the three coupon files. The files hold about 313M codes (~3 GB uncompressed), so they can't be scanned per request or held in memory.

So validation is split in two:

- **Offline:** `cmd/couponindex` reads the files once and writes the valid codes to `data/valid_coupons.txt`. That comes out to 8 codes.
- **Server:** loads that file at startup into an in-memory set behind the `promo.Validator` interface. Sending `SIGHUP` reloads it without a restart.

The builder has two engines, chosen with `-engine`:

- `hashjoin` (default): pure Go. Splits codes into buckets on disk by hash so every copy of a code lands in the same bucket, then checks one bucket at a time. Memory stays small no matter how big the files are.
- `duckdb`: the same logic as a SQL `GROUP BY` query, run with embedded DuckDB. Only the builder uses it; the server doesn't depend on it.

Codes are matched exactly (case-sensitive).

To rebuild the index, put the three `.gz` files in `data/` and run:

```bash
go run ./cmd/couponindex data/couponbase1.gz data/couponbase2.gz data/couponbase3.gz
```

## Scaling

The server holds no shared state (except orders, see below), so it scales by adding replicas. Coupon updates mean rebuilding the index and shipping the new file; the server can reload it live. If coupons needed expiry or usage limits, they'd move to a database behind the same `Validator` interface.

## Layout

```
cmd/server            API server
cmd/couponindex       offline coupon index builder
internal/httpapi      HTTP handlers
internal/order        order rules
internal/catalog      products
internal/promo        promo validation (serving side)
internal/couponbuild  index builder engines
```

## Not included

- **Discounts:** the spec's `Order` has no price fields, so coupons are only validated.
- **Persistent orders:** orders are kept in memory behind a `Repository` interface.
- **Frontend.**
