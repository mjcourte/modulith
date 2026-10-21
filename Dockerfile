# Images for every Go binary, one target each. From the repo root:
#
#   docker build --target modulith .            # the monolith, with ffprobe
#   docker build --target storage .
#   docker build --target iam .
#   docker build --target documents .
#   docker build --target gateway .
#   docker build --target mediaprocessor .   # ffprobe, and no secrets at run time
#
# deploy/monolith and deploy/distributed build them through compose.

# ---- Build: every binary under cmd/, in one go.
FROM golang:1.27-alpine AS build
WORKDIR /src
# Dependencies first, so editing code doesn't re-download them.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static binaries (no cgo), so the runtime images need no Go or C libraries.
# The cache mount keeps compiled packages between builds.
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

# ---- Runtime bases. Alpine includes the CA certificates TLS to AWS needs.
FROM alpine:3.22 AS base
RUN adduser -D -H -u 10001 modulith
USER modulith

FROM alpine:3.22 AS base-ffprobe
# ffmpeg is the package that provides ffprobe.
RUN apk add --no-cache ffmpeg && adduser -D -H -u 10001 modulith
USER modulith

# ---- One target per binary. Every one listens for gRPC on :9090.
FROM base AS storage
COPY --from=build /out/storage /usr/local/bin/
ENV MODULITH_GRPC_ADDR=:9090
EXPOSE 9090
ENTRYPOINT ["storage"]

FROM base AS iam
COPY --from=build /out/iam /usr/local/bin/
ENV MODULITH_GRPC_ADDR=:9090
EXPOSE 9090
ENTRYPOINT ["iam"]

FROM base AS documents
COPY --from=build /out/documents /usr/local/bin/
ENV MODULITH_GRPC_ADDR=:9090
EXPOSE 9090
ENTRYPOINT ["documents"]

FROM base-ffprobe AS mediaprocessor
COPY --from=build /out/mediaprocessor /usr/local/bin/
ENV MODULITH_GRPC_ADDR=:9090
EXPOSE 9090
ENTRYPOINT ["mediaprocessor"]

# The gateway serves browsers over HTTP on :8080, not gRPC.
FROM base AS gateway
COPY --from=build /out/gateway /usr/local/bin/
ENV MODULITH_GATEWAY_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["gateway"]

# Last, so a plain `docker build .` builds the monolith.
FROM base-ffprobe AS modulith
COPY --from=build /out/modulith /usr/local/bin/
ENV MODULITH_GRPC_ADDR=:9090
ENV MODULITH_GATEWAY_ADDR=:8080
EXPOSE 9090 8080
ENTRYPOINT ["modulith"]
