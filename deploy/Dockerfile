# syntax=docker/dockerfile:1

# --- build ---
FROM --platform=$BUILDPLATFORM golang:1.26.6 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static build; embeds migrations, templates, static assets, and table key map.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/lilypad ./cmd/lilypad
# Retain upstream notices alongside the binary.
RUN set -eu; \
    mkdir -p /out/licenses; cp LICENSE /out/LICENSE; cp "$(go env GOROOT)/LICENSE" /out/licenses/Go-LICENSE; \
    go list -m -f '{{if not .Main}}{{if .Dir}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' all > /out/modules.txt; \
    while read -r name version directory; do \
      [ -n "$name" ] || continue; \
      target="/out/licenses/$name"; mkdir -p "$target"; found=false; \
      for notice in "$directory"/*; do \
        [ -f "$notice" ] || continue; \
        filename="$(basename "$notice" | tr '[:lower:]' '[:upper:]')"; \
        case "$filename" in LICENSE*|COPYING*) found=true;; NOTICE*|PATENTS) ;; *) continue;; esac; \
        cp "$notice" "$target/"; \
      done; \
      [ "$found" = true ] || { printf 'Missing upstream license: %s\n' "$name" >&2; exit 1; }; \
      printf '%s %s\n' "$name" "$version" >> /out/licenses/dependencies.txt; \
    done < /out/modules.txt; rm /out/modules.txt
ARG VERSION=dev
RUN printf '%s\n' "$VERSION" > /out/lilypad.version

# --- runtime ---
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/lilypad /app/lilypad
COPY --from=build /out/lilypad.version /app/lilypad.version
COPY --from=build /out/LICENSE /app/LICENSE
COPY --from=build /out/licenses /app/licenses
# Provide config via mounted file (/app/config.yaml) or LILYPAD_* env vars.
EXPOSE 8443
USER nonroot:nonroot
ENTRYPOINT ["/app/lilypad", "-config", "/app/config.yaml"]
