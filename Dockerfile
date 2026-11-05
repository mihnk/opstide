# One static binary on distroless, run as non-root with a read-only root
# filesystem; the only writable path is the data volume (/data). The web
# app is built first and embedded in the binary.
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json web/.npmrc ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN mkdir -p ../internal/webui/dist && npm run build

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist/ internal/webui/dist/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/opstide ./cmd/opstide

FROM gcr.io/distroless/static-debian12:nonroot
# Links the package to this repository on GitHub.
LABEL org.opencontainers.image.source="https://github.com/mihnk/opstide" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/opstide /opstide
USER 65532:65532
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/opstide"]
