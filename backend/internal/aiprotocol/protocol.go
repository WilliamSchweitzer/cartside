// Package aiprotocol defines the local simulated provider's versioned contract.
package aiprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const Model = "cartside-mock-v1"
const MaxRequestBytes = 8192
const MaxResponseBytes = 2048

type Candidate struct {
	ProductID string `json:"product_id"`
	Name      string `json:"name"`
	Package   string `json:"package_label"`
}
type Input struct {
	Version    int         `json:"version"`
	Scenario   string      `json:"scenario"`
	Candidates []Candidate `json:"candidates"`
}
type Suggestion struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
	Reason    string `json:"reason"`
}
type Output struct {
	Version     int          `json:"version"`
	Text        string       `json:"text"`
	Suggestions []Suggestion `json:"suggestions"`
}

// Strict rejects duplicate keys at every depth, nulls, unknown fields and trailing JSON.
func Strict(b []byte, dst any) error {
	if !utf8.Valid(b) {
		return errors.New("invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	var walk func() error
	walk = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		if t == nil {
			return errors.New("null field")
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					k, e := d.Token()
					if e != nil {
						return e
					}
					s, ok := k.(string)
					if !ok || seen[s] {
						return errors.New("duplicate field")
					}
					seen[s] = true
					if e = walk(); e != nil {
						return e
					}
				}
				_, e = d.Token()
				return e
			case '[':
				for d.More() {
					if e := walk(); e != nil {
						return e
					}
				}
				_, e = d.Token()
				return e
			default:
				return errors.New("invalid delimiter")
			}
		}
		return nil
	}
	if e := walk(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing content")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	// Require exact field names and required fields: encoding/json alone accepts
	// case-insensitive names and missing fields.
	canonical, err := json.Marshal(dst)
	if err != nil {
		return err
	}
	var original, typed any
	if err = json.Unmarshal(b, &original); err != nil {
		return err
	}
	if err = json.Unmarshal(canonical, &typed); err != nil {
		return err
	}
	if !sameShape(original, typed) {
		return errors.New("invalid fields")
	}
	return nil
}

func sameShape(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			other, exists := y[k]
			if !exists || !sameShape(v, other) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i, v := range x {
			if !sameShape(v, y[i]) {
				return false
			}
		}
		return true
	default:
		return true
	}
}
