package envelope

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// checkUnicode refuses what encoding/json would silently repair: invalid UTF-8 and \u escapes that
// form a lone surrogate. Both decode to U+FFFD, so the signed bytes and the text the agent reads
// would differ.
func checkUnicode(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid UTF-8")
	}
	for i := 0; i < len(raw); {
		if raw[i] != '\\' {
			i++
			continue
		}
		code, ok := unicodeEscape(raw, i)
		if !ok {
			i += 2 // another escape, or invalid JSON, which the decoder reports
			continue
		}
		i += 6
		switch {
		case code >= 0xDC00 && utf16.IsSurrogate(code):
			return fmt.Errorf("lone low surrogate \\u%04x", code)
		case utf16.IsSurrogate(code):
			low, ok := unicodeEscape(raw, i)
			if !ok || low < 0xDC00 || !utf16.IsSurrogate(low) {
				return fmt.Errorf("lone high surrogate \\u%04x", code)
			}
			i += 6
		}
	}
	return nil
}

// unicodeEscape reads the \uXXXX escape that starts at raw[i].
func unicodeEscape(raw []byte, i int) (rune, bool) {
	if i+6 > len(raw) || raw[i] != '\\' || raw[i+1] != 'u' {
		return 0, false
	}
	code, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
	return rune(code), err == nil
}

// maxExactInteger is the largest magnitude RFC 8785 (via ES6) writes as a plain integer.
const maxExactInteger = 1 << 53

// canonicalize writes JSON in the canonical form of RFC 8785 (JCS), which is what an envelope's
// signature covers (design.md §6). It is an in-repo implementation of the subset our envelopes
// use: objects, arrays, strings, booleans, null and integers up to 2^53. Any other number is
// refused rather than formatted, because ES6 float formatting is not worth carrying for fields
// the bus never produces; a foreign message that uses one simply cannot be verified.
func canonicalize(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON value")
	}
	var out bytes.Buffer
	if err := writeCanonical(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeCanonical(out *bytes.Buffer, value any) error {
	switch value := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(value))
	case string:
		writeString(out, value)
	case json.Number:
		integer, err := strconv.ParseInt(value.String(), 10, 64)
		if err != nil || integer > maxExactInteger || integer < -maxExactInteger {
			return fmt.Errorf("number %s is not an integer within ±2^53", value)
		}
		out.WriteString(strconv.FormatInt(integer, 10))
	case []any:
		out.WriteByte('[')
		for i, element := range value {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, element); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		// RFC 8785 sorts by UTF-16 code units, which differs from byte order above U+FFFF.
		slices.SortFunc(keys, func(a, b string) int {
			return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
		})
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			writeString(out, key)
			out.WriteByte(':')
			if err := writeCanonical(out, value[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("unexpected JSON value of type %T", value)
	}
	return nil
}

// writeString escapes only what RFC 8785 requires: quote, backslash and control characters.
func writeString(out *bytes.Buffer, text string) {
	out.WriteByte('"')
	for _, r := range text {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}

// checkNoDuplicateKeys walks the JSON value in raw and fails if any object repeats a key.
func checkNoDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkNoDuplicates(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

func walkNoDuplicates(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			// encoding/json matches struct fields case-insensitively, so "body" and "Body"
			// count as the same key.
			key, _ := keyToken.(string)
			key = strings.ToLower(key)
			if seen[key] {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[key] = true
			if err := walkNoDuplicates(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := walkNoDuplicates(decoder); err != nil {
				return err
			}
		}
	}
	_, err = decoder.Token() // the closing delimiter
	return err
}
