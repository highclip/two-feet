# Same shape the app's Dockerfile should take: static Go binary on distroless, non-root,
# read-only root filesystem friendly. Version comes from CI (the git sha).
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY . .
# go.sum is generated on the first `go mod tidy` (docs/RUNBOOK.md, rehearsal step 1) and committed.
# Until then, resolve it here so the very first build still works.
RUN [ -f go.sum ] || go mod tidy
RUN go mod download
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/server .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
