package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Protocol limits for untrusted provider output (V2 provider_runner parity).
const (
	MaxRecords         = 1024
	MaxItemFields      = 64
	MaxTextChars       = 1_000_000
	MaxMetadataEntries = 128
	MaxContainerItems  = 1024
	MaxNestingDepth    = 8
	MaxFieldChars      = 4096
	MaxStringChars     = 1_000_000
	MaxLine            = 2_147_483_647
)

var errSyntax = errors.New("invalid JSON")

// decodeStrict parses exactly one JSON value, rejecting duplicate keys,
// excessive nesting/size, non-int64 integers and non-finite numbers.
func decodeStrict(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := strictValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing data", errSyntax)
	}
	return v, nil
}

func strictValue(dec *json.Decoder, depth int) (any, error) {
	if depth > MaxNestingDepth {
		return nil, errors.New("provider JSON exceeds maximum nesting depth")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errSyntax, err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := map[string]any{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("%w: %v", errSyntax, err)
				}
				k, _ := kt.(string)
				if k == "" {
					return nil, errors.New("provider JSON keys must be non-empty strings")
				}
				if len(k) > MaxFieldChars {
					return nil, errors.New("provider JSON key exceeds protocol limit")
				}
				if _, dup := obj[k]; dup {
					return nil, fmt.Errorf("provider JSON contains duplicate key: %s", k)
				}
				if len(obj) >= MaxMetadataEntries {
					return nil, errors.New("provider JSON object exceeds protocol limit")
				}
				v, err := strictValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				obj[k] = v
			}
			_, err := dec.Token()
			return obj, err
		case '[':
			arr := []any{}
			for dec.More() {
				if len(arr) >= MaxContainerItems {
					return nil, errors.New("provider JSON array exceeds protocol limit")
				}
				v, err := strictValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			_, err := dec.Token()
			return arr, err
		}
		return nil, errSyntax
	case json.Number:
		return checkNumber(t)
	case string:
		if len(t) > MaxStringChars {
			return nil, errors.New("provider JSON string exceeds protocol limit")
		}
		return t, nil
	default: // bool or nil
		return t, nil
	}
}

func checkNumber(n json.Number) (any, error) {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		bi, ok := new(big.Int).SetString(s, 10)
		if !ok || !bi.IsInt64() {
			return nil, errors.New("provider integer exceeds signed 64-bit range")
		}
		return bi.Int64(), nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, errors.New("provider JSON contains non-finite number")
	}
	return f, nil
}

// parseRecords accepts a JSON array, an {"items": [...]} envelope, or JSONL objects.
func parseRecords(raw []byte) ([]map[string]any, error) {
	payload, err := decodeStrict(raw)
	if errors.Is(err, errSyntax) {
		rows := []map[string]any{}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if len(rows) >= MaxRecords {
				return nil, errors.New("provider payload exceeds maximum record count")
			}
			v, err := decodeStrict([]byte(line))
			if err != nil {
				return nil, errors.New("provider returned invalid JSON/JSONL payload")
			}
			row, ok := v.(map[string]any)
			if !ok {
				return nil, errors.New("provider JSONL rows must be objects")
			}
			rows = append(rows, row)
		}
		if len(rows) == 0 {
			return nil, errors.New("provider returned invalid JSON/JSONL payload")
		}
		return rows, nil
	}
	if err != nil {
		return nil, err
	}
	if obj, ok := payload.(map[string]any); ok {
		items, present := obj["items"]
		if !present {
			return nil, errors.New("provider object payload must contain an items array")
		}
		payload = items
	}
	arr, ok := payload.([]any)
	if !ok {
		return nil, errors.New("provider payload must be a JSON array or an object with an items array")
	}
	if len(arr) > MaxRecords {
		return nil, errors.New("provider payload exceeds maximum record count")
	}
	rows := make([]map[string]any, 0, len(arr))
	for _, it := range arr {
		row, ok := it.(map[string]any)
		if !ok {
			return nil, errors.New("provider payload items must all be objects")
		}
		rows = append(rows, row)
	}
	return rows, nil
}
