# One static binary on distroless, run as non-root with a read-only root
# filesystem; the only writable path is the data volume (/data).
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/opstide ./cmd/opstide

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/opstide /opstide
USER 65532:65532
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/opstide"]
