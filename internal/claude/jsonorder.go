package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// object is a JSON object that keeps its keys in the order they were read.
//
// settings.json is round-tripped through this rather than map[string]any, which
// re-sorted every key alphabetically on install - a whole-file diff in someone's
// dotfiles for adding one hook.
type object struct {
	keys []string
	vals map[string]any
}

func newObject() *object { return &object{vals: map[string]any{}} }

func (o *object) get(key string) any { return o.vals[key] }

func (o *object) set(key string, v any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

func (o *object) del(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// decodeOrdered parses JSON into *object, []any, string, json.Number, bool and nil.
// Numbers stay json.Number so they are written back exactly as they were.
func decodeOrdered(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the top-level value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		o := newObject()
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ := kt.(string)
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			o.set(key, v)
		}
		_, err := dec.Token() // }
		return o, err
	case '[':
		arr := []any{}
		for dec.More() {
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err := dec.Token() // ]
		return arr, err
	}
	return nil, fmt.Errorf("unexpected %v", delim)
}

// encodeOrdered writes v with two-space indentation. HTML characters are left as
// they are: "2>&1" in someone's hook should not come back as "2>&1".
func encodeOrdered(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := writeValue(&b, v, ""); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}

func writeValue(b *bytes.Buffer, v any, indent string) error {
	inner := indent + "  "
	switch t := v.(type) {
	case *object:
		if len(t.keys) == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteString("{\n")
		for i, k := range t.keys {
			b.WriteString(inner)
			if err := writeScalar(b, k); err != nil {
				return err
			}
			b.WriteString(": ")
			if err := writeValue(b, t.vals[k], inner); err != nil {
				return err
			}
			if i < len(t.keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteString("[\n")
		for i, e := range t {
			b.WriteString(inner)
			if err := writeValue(b, e, inner); err != nil {
				return err
			}
			if i < len(t)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "]")
	default:
		return writeScalar(b, v)
	}
	return nil
}

func writeScalar(b *bytes.Buffer, v any) error {
	if n, ok := v.(json.Number); ok {
		b.WriteString(n.String())
		return nil
	}
	var s strings.Builder
	enc := json.NewEncoder(&s)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	b.WriteString(strings.TrimSuffix(s.String(), "\n"))
	return nil
}
