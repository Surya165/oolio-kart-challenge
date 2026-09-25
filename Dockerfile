# syntax=docker/dockerfile:1

# ---- API server: pure Go, static binary ----
# Only packages the server imports are downloaded, so the DuckDB bindings
# (a cgo dependency of cmd/couponindex) never enter this stage.
FROM golang:1.24-bookworm AS build-server
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
WORKDIR /app
COPY --from=build-server /out/server /app/server
# The server runs as nonroot, so set the file mode explicitly instead of
# inheriting the host's. Copy into /app (created by WORKDIR, mode 755): if COPY
# had to create a parent dir, --chmod would apply to it too, and a 0444 dir
# cannot be traversed.


COPY --chmod=0444 data/valid_coupons.txt /app/valid_coupons.txt
ENV ADDR=:8080 COUPON_INDEX=/app/valid_coupons.txt
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]

# ---- offline index builder: needs cgo for the DuckDB engine ----
FROM golang:1.24-bookworm AS build-couponindex
WORKDIR /src
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/couponindex ./cmd/couponindex

# distroless/cc ships glibc + libstdc++, which the DuckDB library links against.
FROM gcr.io/distroless/cc-debian12 AS couponindex
COPY --from=build-couponindex /out/couponindex /usr/local/bin/couponindex
ENTRYPOINT ["couponindex"]
