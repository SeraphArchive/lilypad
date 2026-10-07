package codec

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	in := map[string]any{"hdr": "x", "code": 0, "tables": map[string]any{"user_version": []any{}}}
	wire, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if out["hdr"] != "x" {
		t.Fatalf("hdr=%v", out["hdr"])
	}
}

func TestDecodeBadBodyErrors(t *testing.T) {
	if _, err := Decode([]byte{1, 2, 3, 4}); err == nil {
		t.Fatal("expected error decoding non-codec bytes")
	}
}

func TestDecodeUsesJSONNumber(t *testing.T) {
	wire, err := Encode(map[string]any{"savedTime": 1781528792})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	n, ok := out["savedTime"].(json.Number)
	if !ok {
		t.Fatalf("savedTime not json.Number: %T", out["savedTime"])
	}
	if n.String() != "1781528792" {
		t.Fatalf("savedTime=%s", n.String())
	}
}

func TestEncodeDoesNotHTMLEscape(t *testing.T) {
	wire, err := Encode(map[string]any{"message": "a<b>c&d"})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(Unscramble(wire)))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	if !bytes.Contains(raw, []byte("a<b>c&d")) {
		t.Fatalf("expected unescaped JSON, got: %s", raw)
	}
}

func TestDecodeInto(t *testing.T) {
	type resp struct {
		Code int `json:"code"`
	}
	wire, _ := Encode(map[string]any{"code": 7})
	var r resp
	if err := DecodeInto(wire, &r); err != nil {
		t.Fatal(err)
	}
	if r.Code != 7 {
		t.Fatalf("code=%d", r.Code)
	}
}

func TestDecodeRequiresOneJSONDocument(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{body: "{\"code\":7} \r\n\t", valid: true},
		{body: "{\"code\":7} {\"code\":9}"},
		{body: "{\"code\":7} null"},
		{body: "{\"code\":7} trailing"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			if _, err := zw.Write([]byte(tc.body)); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			_, err := Decode(Scramble(buf.Bytes()))
			if (err == nil) != tc.valid {
				t.Fatalf("Decode valid = %v, want %v: %v", err == nil, tc.valid, err)
			}
		})
	}
}
