# syntax=docker/dockerfile:1.7

FROM golang:1.26.5-alpine3.23 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/alib-fetcher ./cmd/alib-fetcher \
    && mkdir -p /out/state

FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=build --chmod=0555 /out/alib-fetcher /alib-fetcher
COPY --from=build --chown=65532:65532 /out/state /tmp/alib-fetcher
VOLUME ["/tmp/alib-fetcher"]
USER 65532:65532
ENTRYPOINT ["/alib-fetcher"]
