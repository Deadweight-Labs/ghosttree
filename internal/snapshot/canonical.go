package snapshot

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	timeType          = reflect.TypeOf(time.Time{})
	rawMessageType    = reflect.TypeOf(json.RawMessage{})
	numberType        = reflect.TypeOf(json.Number(""))
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

func MarshalCanonical(value any) ([]byte, error) {
	var out bytes.Buffer
	if err := WriteCanonical(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func WriteCanonical(out io.Writer, value any) error {
	encoder := canonicalEncoder{out: out, active: make(map[canonicalVisit]bool)}
	return encoder.write(reflect.ValueOf(value))
}

func ValidateCanonical(raw []byte) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("canonical JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode canonical JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("canonical JSON contains multiple values")
		}
		return fmt.Errorf("decode canonical JSON trailer: %w", err)
	}
	canonical, err := MarshalCanonical(value)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, canonical) {
		return fmt.Errorf("JSON is valid but not canonical")
	}
	return nil
}

type canonicalVisit struct {
	typ reflect.Type
	ptr uintptr
}

type canonicalEncoder struct {
	out    io.Writer
	active map[canonicalVisit]bool
}

func (e *canonicalEncoder) writeBytes(value []byte) error {
	n, err := e.out.Write(value)
	if err == nil && n != len(value) {
		return io.ErrShortWrite
	}
	return err
}

func (e *canonicalEncoder) writeLiteral(value string) error {
	n, err := io.WriteString(e.out, value)
	if err == nil && n != len(value) {
		return io.ErrShortWrite
	}
	return err
}

func (e *canonicalEncoder) writeByte(value byte) error {
	return e.writeBytes([]byte{value})
}

func (e *canonicalEncoder) writeRune(value rune) error {
	return e.writeLiteral(string(value))
}

func (e *canonicalEncoder) write(value reflect.Value) error {
	if !value.IsValid() {
		return e.writeLiteral("null")
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return e.writeLiteral("null")
		}
		return e.write(value.Elem())
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return e.writeLiteral("null")
		}
		return e.withVisit(value, func() error { return e.write(value.Elem()) })
	}
	if value.Type() == timeType {
		return e.writeString(value.Interface().(time.Time).UTC().Format(time.RFC3339Nano))
	}
	if value.Type() == rawMessageType {
		raw := value.Interface().(json.RawMessage)
		if err := ValidateCanonical(raw); err != nil {
			return fmt.Errorf("raw JSON: %w", err)
		}
		return e.writeBytes(raw)
	}
	if value.Type() == numberType {
		return e.writeNumber(value.Interface().(json.Number).String())
	}
	if value.CanInterface() && value.Type().Implements(textMarshalerType) {
		text, err := value.Interface().(encoding.TextMarshaler).MarshalText()
		if err != nil {
			return err
		}
		return e.writeString(string(text))
	}

	switch value.Kind() {
	case reflect.Bool:
		return e.writeLiteral(strconv.FormatBool(value.Bool()))
	case reflect.String:
		return e.writeString(value.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return e.writeLiteral(strconv.FormatInt(value.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return e.writeLiteral(strconv.FormatUint(value.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		return fmt.Errorf("canonical JSON does not permit floating-point values")
	case reflect.Slice:
		if value.IsNil() {
			return e.writeLiteral("null")
		}
		return e.withVisit(value, func() error { return e.writeArray(value) })
	case reflect.Array:
		return e.writeArray(value)
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("canonical JSON object key type is %s, want string", value.Type().Key())
		}
		if value.IsNil() {
			return e.writeLiteral("null")
		}
		return e.withVisit(value, func() error { return e.writeMap(value) })
	case reflect.Struct:
		return e.writeStruct(value)
	default:
		return fmt.Errorf("canonical JSON does not support %s", value.Kind())
	}
}

func (e *canonicalEncoder) writeNumber(value string) error {
	negative := strings.HasPrefix(value, "-")
	digits := value
	if negative {
		digits = value[1:]
	}
	if digits == "" || (len(digits) > 1 && digits[0] == '0') {
		return fmt.Errorf("canonical JSON number %q is not an integer in shortest form", value)
	}
	for _, digit := range []byte(digits) {
		if digit < '0' || digit > '9' {
			return fmt.Errorf("canonical JSON does not permit number %q", value)
		}
	}
	if negative && digits == "0" {
		return e.writeByte('0')
	}
	return e.writeLiteral(value)
}

func (e *canonicalEncoder) withVisit(value reflect.Value, write func() error) error {
	visit := canonicalVisit{typ: value.Type(), ptr: value.Pointer()}
	if visit.ptr != 0 && e.active[visit] {
		return fmt.Errorf("canonical JSON contains a cycle through %s", value.Type())
	}
	if visit.ptr != 0 {
		e.active[visit] = true
		defer delete(e.active, visit)
	}
	return write()
}

func (e *canonicalEncoder) writeArray(value reflect.Value) error {
	if err := e.writeByte('['); err != nil {
		return err
	}
	for i := 0; i < value.Len(); i++ {
		if i > 0 {
			if err := e.writeByte(','); err != nil {
				return err
			}
		}
		if err := e.write(value.Index(i)); err != nil {
			return err
		}
	}
	return e.writeByte(']')
}

func (e *canonicalEncoder) writeMap(value reflect.Value) error {
	keys := value.MapKeys()
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	if err := e.writeByte('{'); err != nil {
		return err
	}
	for i, key := range keys {
		if !utf8.ValidString(key.String()) {
			return fmt.Errorf("canonical JSON object key is not valid UTF-8")
		}
		if i > 0 {
			if err := e.writeByte(','); err != nil {
				return err
			}
		}
		if err := e.writeString(key.String()); err != nil {
			return err
		}
		if err := e.writeByte(':'); err != nil {
			return err
		}
		if err := e.write(value.MapIndex(key)); err != nil {
			return err
		}
	}
	return e.writeByte('}')
}

type canonicalField struct {
	name  string
	value reflect.Value
}

func (e *canonicalEncoder) writeStruct(value reflect.Value) error {
	fields := make([]canonicalField, 0, value.NumField())
	seen := make(map[string]bool)
	for i := 0; i < value.NumField(); i++ {
		fieldType := value.Type().Field(i)
		if fieldType.PkgPath != "" {
			continue
		}
		name, options, _ := strings.Cut(fieldType.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = fieldType.Name
		}
		fieldValue := value.Field(i)
		if strings.Contains(options, "omitempty") && fieldValue.IsZero() {
			continue
		}
		if seen[name] {
			return fmt.Errorf("canonical JSON struct has duplicate field %q", name)
		}
		seen[name] = true
		fields = append(fields, canonicalField{name: name, value: fieldValue})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].name < fields[j].name })
	if err := e.writeByte('{'); err != nil {
		return err
	}
	for i, field := range fields {
		if i > 0 {
			if err := e.writeByte(','); err != nil {
				return err
			}
		}
		if err := e.writeString(field.name); err != nil {
			return err
		}
		if err := e.writeByte(':'); err != nil {
			return err
		}
		if err := e.write(field.value); err != nil {
			return err
		}
	}
	return e.writeByte('}')
}

func (e *canonicalEncoder) writeString(value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("canonical JSON string is not valid UTF-8")
	}
	if err := e.writeByte('"'); err != nil {
		return err
	}
	for _, r := range value {
		switch r {
		case '"', '\\':
			if err := e.writeByte('\\'); err != nil {
				return err
			}
			if err := e.writeRune(r); err != nil {
				return err
			}
		case '\b':
			if err := e.writeLiteral(`\b`); err != nil {
				return err
			}
		case '\t':
			if err := e.writeLiteral(`\t`); err != nil {
				return err
			}
		case '\n':
			if err := e.writeLiteral(`\n`); err != nil {
				return err
			}
		case '\f':
			if err := e.writeLiteral(`\f`); err != nil {
				return err
			}
		case '\r':
			if err := e.writeLiteral(`\r`); err != nil {
				return err
			}
		default:
			if r < 0x20 {
				const hexDigits = "0123456789abcdef"
				if err := e.writeLiteral(`\u00`); err != nil {
					return err
				}
				if err := e.writeByte(hexDigits[byte(r)>>4]); err != nil {
					return err
				}
				if err := e.writeByte(hexDigits[byte(r)&0x0f]); err != nil {
					return err
				}
			} else {
				if err := e.writeRune(r); err != nil {
					return err
				}
			}
		}
	}
	return e.writeByte('"')
}
