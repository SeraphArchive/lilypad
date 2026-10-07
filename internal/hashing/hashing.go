// Package hashing produces deterministic per-table hashes for the DeltaComm
// save-sync protocol. The hash is server-owned and opaque to the client, which
// only echoes the last server-provided hash as an optimistic-concurrency
// baseline. Only self-consistency across confirm/pull/push/RPC matters, so any
// deterministic pure function of the content suffices; we use md5 over a
// canonical JSON encoding.
package hashing

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// CanonicalJSON encodes v with object keys sorted recursively and no
// insignificant whitespace, so logically-equal values encode identically.
func CanonicalJSON(v any) ([]byte, error) {
	// Normalize all typed slices/maps through the same number-preserving tree.
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var normalized any
	if err := dec.Decode(&normalized); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, normalized); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case json.Number:
		n, err := canonicalNumber(string(t))
		if err != nil {
			return err
		}
		buf.WriteString(n)
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			buf.Write(kb)
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	default:
		enc := json.NewEncoder(buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return err
		}
		buf.Truncate(buf.Len() - 1) // strip trailing newline written by Encode
	}
	return nil
}

// canonicalNumber produces exact decimal notation without insignificant zeroes.
// No float conversion: 64-bit keys must remain exact across JSONB round trips.
func canonicalNumber(s string) (string, error) {
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	mantissa, exponent, hasExponent := strings.Cut(strings.ToLower(s), "e")
	exp := 0
	if hasExponent {
		var err error
		exp, err = strconv.Atoi(exponent)
		if err != nil || exp < -128 || exp > 128 {
			return "", fmt.Errorf("number exponent out of range")
		}
	}
	whole, fraction, _ := strings.Cut(mantissa, ".")
	if len(whole)+len(fraction) > 512 {
		return "", fmt.Errorf("number precision exceeds game-data limit")
	}
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return "0", nil
	}
	scale := len(fraction) - exp
	for strings.HasSuffix(digits, "0") {
		digits = digits[:len(digits)-1]
		scale--
	}
	var out string
	switch {
	case scale <= 0:
		out = digits + strings.Repeat("0", -scale)
	case scale >= len(digits):
		out = "0." + strings.Repeat("0", scale-len(digits)) + digits
	default:
		out = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if negative {
		out = "-" + out
	}
	return out, nil
}

// TableHash returns the 32-hex md5 of the canonical JSON of rows.
func TableHash(rows any) (string, error) {
	cj, err := CanonicalJSON(rows)
	if err != nil {
		return "", fmt.Errorf("hashing: %w", err)
	}
	sum := md5.Sum(cj)
	return hex.EncodeToString(sum[:]), nil
}
