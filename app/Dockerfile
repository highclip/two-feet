# Static Go binary on distroless, non-root, read-only root filesystem friendly.
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/twofeet ./cmd/twofeet

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/twofeet /twofeet
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/twofeet"]
