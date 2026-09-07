# syntax=docker/dockerfile:1.7

FROM golang:1.27.1-alpine3.23 AS build

WORKDIR /src
COPY . .
RUN --network=none CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath \
    -ldflags="-s -w" -o /out/alib-fetcher ./cmd/alib-fetcher \
    && mkdir -p /out/state

FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=build --chmod=0555 /out/alib-fetcher /alib-fetcher
COPY --from=build --chown=65532:65532 /out/state /var/lib/alib-fetcher
VOLUME ["/var/lib/alib-fetcher"]
USER 65532:65532
ENTRYPOINT ["/alib-fetcher"]
