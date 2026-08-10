// TOON (Token-Oriented Object Notation) encoding for LLM-facing content —
// see https://github.com/toon-format/spec. Tool results are stored as JSON
// for the UI but re-encoded as TOON when fed to the model: it preserves the
// JSON data model while cutting tokens on uniform structures (tabular forms,
// declared lengths). Encoding only — no decoder is needed.
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// tField is one JSON object field. Objects decode to []tField so key order
// (encounter order, which TOON must preserve) survives Go's map ordering.
type tField struct {
	key   string
	value any
}

var (
	toonKeyRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
	toonNumericRe = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?(?:e[+-]?[0-9]+)?$`)
)

// decodeOrdered decodes a JSON document preserving object key order.
func decodeOrdered(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return buildOrdered(dec, tok)
}

func buildOrdered(dec *json.Decoder, tok json.Token) (any, error) {
	if d, ok := tok.(json.Delim); ok {
		switch d {
		case '{':
			obj := []tField{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := kt.(string)
				vt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := buildOrdered(dec, vt)
				if err != nil {
					return nil, err
				}
				obj = append(obj, tField{key: key, value: v})
			}
			if _, err := dec.Token(); err != nil { // consume '}'
				return nil, err
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				vt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := buildOrdered(dec, vt)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return nil, err
			}
			return arr, nil
		}
	}
	switch t := tok.(type) {
	case string:
		return t, nil
	case json.Number:
		return t, nil
	case bool:
		return t, nil
	case nil:
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected token %T", tok)
}

// toonJSON encodes a JSON document as TOON for the LLM; the input is
// returned unchanged when it is not valid JSON.
func toonJSON(s string) string {
	if out, err := toonEncode([]byte(s)); err == nil {
		return out
	}
	return s
}

func toonEncode(data []byte) (string, error) {
	v, err := decodeOrdered(data)
	if err != nil {
		return "", err
	}
	var e toonEncoder
	switch val := v.(type) {
	case []tField:
		if cols, ok := keyedColumns(val); ok {
			e.renderKeyed("", val, cols)
		} else {
			for _, f := range val {
				e.renderField(f.key, f.value, "")
			}
		}
	case []any:
		if len(val) == 0 {
			return "[]", nil
		}
		e.renderArray("", val)
	case nil:
		return "null", nil
	default:
		return primToken(v), nil
	}
	return strings.TrimSuffix(e.sb.String(), "\n"), nil
}

// toonEncoder writes TOON with two-space indentation and the comma delimiter.
type toonEncoder struct {
	sb     strings.Builder
	indent int
}

func (e *toonEncoder) pad() {
	for i := 0; i < e.indent; i++ {
		e.sb.WriteString("  ")
	}
}

func (e *toonEncoder) writeLine(s string) {
	e.pad()
	e.sb.WriteString(s)
	e.sb.WriteByte('\n')
}

// renderField writes "key: <value>" at the current depth (with a leading
// prefix, used for the list-item "- " hyphen line), followed by any block
// content (nested fields, rows) at deeper depths.
func (e *toonEncoder) renderField(key string, v any, prefix string) {
	e.pad()
	e.sb.WriteString(prefix)
	switch val := v.(type) {
	case nil:
		e.sb.WriteString(toonKey(key) + ": null\n")
	case string:
		e.sb.WriteString(toonKey(key) + ": " + toonString(val) + "\n")
	case json.Number:
		e.sb.WriteString(toonKey(key) + ": " + string(val) + "\n")
	case bool:
		e.sb.WriteString(toonKey(key) + ": " + strconv.FormatBool(val) + "\n")
	case []any:
		e.renderArray(key, val)
	case []tField:
		e.renderObject(key, val)
	}
}

func (e *toonEncoder) renderObject(key string, obj []tField) {
	if cols, ok := keyedColumns(obj); ok {
		e.renderKeyed(key, obj, cols)
		return
	}
	e.writeLine(toonKey(key) + ":")
	e.indent++
	for _, f := range obj {
		e.renderField(f.key, f.value, "")
	}
	e.indent--
}

// renderKeyed writes the keyed tabular form (§9.5): header with the entry
// count and field list, then one "entryKey: c1,c2" row per entry.
func (e *toonEncoder) renderKeyed(key string, obj []tField, cols []toonCol) {
	if key != "" {
		e.sb.WriteString(toonKey(key))
	}
	e.sb.WriteString(fmt.Sprintf("[%d:]{", len(obj)))
	e.writeFieldsHeader(cols)
	e.sb.WriteString("}:\n")
	e.indent++
	for _, f := range obj {
		e.pad()
		e.sb.WriteString(toonKey(f.key) + ": ")
		e.writeCells(f.value.([]tField), cols)
		e.sb.WriteByte('\n')
	}
	e.indent--
}

func (e *toonEncoder) renderArray(key string, arr []any) {
	if len(arr) == 0 {
		e.writeLine(toonKey(key) + ": []")
		return
	}
	if allPrimitives(arr) {
		if key != "" {
			e.sb.WriteString(toonKey(key))
		}
		e.sb.WriteString(fmt.Sprintf("[%d]: ", len(arr)))
		e.writeInline(arr)
		e.sb.WriteByte('\n')
		return
	}
	if cols, ok := tabularColumns(arr); ok {
		if key != "" {
			e.sb.WriteString(toonKey(key))
		}
		e.sb.WriteString(fmt.Sprintf("[%d]{", len(arr)))
		e.writeFieldsHeader(cols)
		e.sb.WriteString("}:\n")
		e.indent++
		for _, el := range arr {
			e.pad()
			e.writeCells(el.([]tField), cols)
			e.sb.WriteByte('\n')
		}
		e.indent--
		return
	}
	// list form (§9.4)
	e.writeLine(toonKey(key) + fmt.Sprintf("[%d]:", len(arr)))
	e.indent++
	for _, el := range arr {
		e.renderListItem(el)
	}
	e.indent--
}

func (e *toonEncoder) renderListItem(v any) {
	switch val := v.(type) {
	case nil:
		e.writeLine("- null")
	case string:
		e.writeLine("- " + toonString(val))
	case json.Number:
		e.writeLine("- " + string(val))
	case bool:
		e.writeLine("- " + strconv.FormatBool(val))
	case []any:
		if len(val) == 0 {
			e.writeLine("- [0]:")
			return
		}
		if allPrimitives(val) {
			e.pad()
			e.sb.WriteString(fmt.Sprintf("- [%d]: ", len(val)))
			e.writeInline(val)
			e.sb.WriteByte('\n')
			return
		}
		e.writeLine(fmt.Sprintf("- [%d]:", len(val)))
		e.indent++
		for _, el := range val {
			e.renderListItem(el)
		}
		e.indent--
	case []tField:
		if len(val) == 0 {
			e.writeLine("-")
			return
		}
		// First field rides the hyphen line (the "- " marker provides its
		// offset); siblings and the first field's scope content sit deeper.
		e.renderField(val[0].key, val[0].value, "- ")
		e.indent++
		for _, f := range val[1:] {
			e.renderField(f.key, f.value, "")
		}
		e.indent--
	}
}

// writeInline emits a primitive array joined by commas.
func (e *toonEncoder) writeInline(arr []any) {
	for i, v := range arr {
		if i > 0 {
			e.sb.WriteByte(',')
		}
		e.sb.WriteString(primToken(v))
	}
}

// writeCells emits one tabular row / keyed entry's leaf cells, depth-first
// over the column tree.
func (e *toonEncoder) writeCells(obj []tField, cols []toonCol) {
	first := true
	var walk func(o []tField, cs []toonCol)
	walk = func(o []tField, cs []toonCol) {
		for _, c := range cs {
			var val any
			for _, f := range o {
				if f.key == c.name {
					val = f.value
					break
				}
			}
			if len(c.sub) > 0 {
				walk(val.([]tField), c.sub)
				continue
			}
			if !first {
				e.sb.WriteByte(',')
			}
			first = false
			e.sb.WriteString(primToken(val))
		}
	}
	walk(obj, cols)
}

// writeFieldsHeader writes the {f1,f2{sub1,sub2},f3} field list.
func (e *toonEncoder) writeFieldsHeader(cols []toonCol) {
	for i, c := range cols {
		if i > 0 {
			e.sb.WriteByte(',')
		}
		e.sb.WriteString(toonKey(c.name))
		if len(c.sub) > 0 {
			e.sb.WriteByte('{')
			e.writeFieldsHeader(c.sub)
			e.sb.WriteByte('}')
		}
	}
}

// toonCol is one tabular column; sub != nil marks a nested field group.
type toonCol struct {
	name string
	sub  []toonCol
}

func isPrimitive(v any) bool {
	switch v.(type) {
	case nil, string, json.Number, bool:
		return true
	}
	return false
}

func allPrimitives(arr []any) bool {
	for _, v := range arr {
		if !isPrimitive(v) {
			return false
		}
	}
	return true
}

// columnOf classifies the value sequence at one key: a leaf column when all
// values are primitives, a nested field group when all values are objects
// with the same key set and uniform sub-columns. ok=false otherwise.
func columnOf(name string, els []any) (toonCol, bool) {
	if isPrimitive(els[0]) {
		for _, v := range els[1:] {
			if !isPrimitive(v) {
				return toonCol{}, false
			}
		}
		return toonCol{name: name}, true
	}
	first, ok := els[0].([]tField)
	if !ok || len(first) == 0 {
		return toonCol{}, false
	}
	firstKeys := map[string]bool{}
	for _, f := range first {
		firstKeys[f.key] = true
	}
	sub := make([]toonCol, 0, len(first))
	for _, f := range first {
		vals := make([]any, 0, len(els))
		vals = append(vals, f.value)
		for _, el := range els[1:] {
			obj, ok := el.([]tField)
			if !ok || len(obj) != len(first) {
				return toonCol{}, false
			}
			var found any
			has := false
			for _, of := range obj {
				if of.key == f.key {
					found = of.value
					has = true
				} else if !firstKeys[of.key] {
					return toonCol{}, false
				}
			}
			if !has {
				return toonCol{}, false
			}
			vals = append(vals, found)
		}
		subCol, ok := columnOf(f.key, vals)
		if !ok {
			return toonCol{}, false
		}
		sub = append(sub, subCol)
	}
	return toonCol{name: name, sub: sub}, true
}

// tabularColumns classifies an array of objects for tabular form (§9.3):
// every element a non-empty object, identical key sets, uniform columns.
func tabularColumns(arr []any) ([]toonCol, bool) {
	first, ok := arr[0].([]tField)
	if !ok || len(first) == 0 {
		return nil, false
	}
	cols := make([]toonCol, 0, len(first))
	for _, f := range first {
		vals := make([]any, 0, len(arr))
		for _, el := range arr {
			obj, ok := el.([]tField)
			if !ok {
				return nil, false
			}
			var found any
			has := false
			for _, of := range obj {
				if of.key == f.key {
					found = of.value
					has = true
					break
				}
			}
			if !has {
				return nil, false
			}
			vals = append(vals, found)
		}
		c, ok := columnOf(f.key, vals)
		if !ok {
			return nil, false
		}
		cols = append(cols, c)
	}
	firstKeys := map[string]bool{}
	for _, f := range first {
		firstKeys[f.key] = true
	}
	for _, el := range arr[1:] {
		obj := el.([]tField)
		if len(obj) != len(first) {
			return nil, false
		}
		for _, of := range obj {
			if !firstKeys[of.key] {
				return nil, false
			}
		}
	}
	return cols, true
}

// keyedColumns classifies an object of uniform objects for keyed tabular
// form (§9.5): at least two entries, all non-empty uniform objects.
func keyedColumns(obj []tField) ([]toonCol, bool) {
	if len(obj) < 2 {
		return nil, false
	}
	vals := make([]any, 0, len(obj))
	for _, f := range obj {
		sub, ok := f.value.([]tField)
		if !ok || len(sub) == 0 {
			return nil, false
		}
		vals = append(vals, sub)
	}
	return tabularColumns(vals)
}

// primToken renders one primitive cell/value.
func primToken(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return toonString(t)
	case json.Number:
		return string(t)
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

// toonKey renders an object key: unquoted when it matches the encoder key
// pattern (§7.3), quoted and escaped otherwise.
func toonKey(k string) string {
	if toonKeyRe.MatchString(k) {
		return k
	}
	return `"` + toonEscape(k) + `"`
}

// toonString renders a string value, quoting it when required (§7.2) and
// escaping per §7.1. The active delimiter is comma.
func toonString(s string) string {
	if !toonNeedsQuotes(s) {
		return s
	}
	return `"` + toonEscape(s) + `"`
}

func toonNeedsQuotes(s string) bool {
	if s == "" {
		return true
	}
	if s[0] == ' ' || s[0] == '\t' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t' {
		return true
	}
	if s == "true" || s == "false" || s == "null" {
		return true
	}
	if toonNumericRe.MatchString(s) {
		return true
	}
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "#") {
		return true
	}
	// colon, double quote, backslash, brackets, braces, and the comma
	// delimiter all force quoting
	if strings.ContainsAny(s, `:"\[]{},`) {
		return true
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 {
			return true
		}
	}
	return false
}

// toonEscape escapes a quoted string per §7.1.
func toonEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
