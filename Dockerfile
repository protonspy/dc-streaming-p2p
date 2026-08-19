# Build stage — the toolchain never reaches the image that runs.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first, so a source edit does not re-resolve them. There are none
# beyond the standard library today, and this layer costs nothing until there are.
COPY go.mod ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w \
      -X github.com/protonspy/dc-streaming-p2p/internal/buildinfo.Version=${VERSION} \
      -X github.com/protonspy/dc-streaming-p2p/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/protonspy/dc-streaming-p2p/internal/buildinfo.Date=${DATE}" \
    -o /out/central ./cmd/central

# Run stage — no shell, no package manager, nothing to pivot to.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/central /central

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/central"]
