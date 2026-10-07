.PHONY: build test vet tidy fixtool run seed replay check package

build: ; go build ./...
test:  ; go test ./...
vet:   ; go vet ./...
check: ; python3 check_source.py
package: ; python3 build.py
tidy:  ; go mod tidy
fixtool: ; go build -o bin/fixtool ./cmd/fixtool
run:   ; go run ./cmd/lilypad -config config.local.yaml

# Regenerate the anonymized new-player seed from an external capture.
# Requires LILYPAD_FIXTURES to point at the capture .jsonl.
seed: ; go run ./cmd/seedgen -out internal/model/newplayer_seed.json

# Structural replay-diff of the capture through the live stack (needs config.local.yaml + DB).
# Requires LILYPAD_FIXTURES. Use a fresh player id so the seeded account is clean.
replay: ; go run ./cmd/fixtool -config config.local.yaml -player replay-fresh replay
