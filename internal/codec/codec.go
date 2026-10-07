package codec

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
)

// Encode serializes v to compact JSON, gzips it, and applies the cipher,
// producing the on-the-wire body bytes. HTML escaping is disabled so strings
// containing <, >, or & are emitted verbatim (the client parses into a
// dictionary; this keeps emitted bodies faithful to captured traffic).
func Encode(v any) ([]byte, error) {
	var jb bytes.Buffer
	enc := json.NewEncoder(&jb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("codec: marshal: %w", err)
	}
	raw := bytes.TrimRight(jb.Bytes(), "\n") // Encode appends a newline
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, fmt.Errorf("codec: gzip: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("codec: gzip close: %w", err)
	}
	return Scramble(buf.Bytes()), nil
}

// maxDecodedBytes bounds the decompressed body size to prevent a small
// highly-compressible payload (gzip bomb) from exhausting memory.
const maxDecodedBytes = 64 << 20 // 64 MiB

// DecodeInto reverses Encode into v. JSON numbers are decoded as json.Number
// to preserve integer fidelity for round-tripping deltas. The decompressed size
// is bounded by maxDecodedBytes.
func DecodeInto(wire []byte, v any) error {
	zr, err := gzip.NewReader(bytes.NewReader(Unscramble(wire)))
	if err != nil {
		return fmt.Errorf("codec: gunzip: %w", err)
	}
	defer zr.Close()
	limited := io.LimitReader(zr, maxDecodedBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("codec: read: %w", err)
	}
	if len(raw) > maxDecodedBytes {
		return fmt.Errorf("codec: decompressed body exceeds %d bytes", maxDecodedBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("codec: json: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("codec: expected exactly one JSON document")
	}
	return nil
}

// Decode reverses Encode into a generic map.
func Decode(wire []byte) (map[string]any, error) {
	m := map[string]any{}
	if err := DecodeInto(wire, &m); err != nil {
		return nil, err
	}
	return m, nil
}
