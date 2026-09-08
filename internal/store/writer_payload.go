package store

import (
	"math"
	"reflect"
	"time"
)

type payloadCounter struct {
	bytes int64
	nodes int
}

func referencedPayloadBytes(values ...any) (int64, error) {
	c := payloadCounter{}
	for _, value := range values {
		v := reflect.ValueOf(value)
		if !v.IsValid() {
			continue
		}
		if err := c.add(uint64(v.Type().Size())); err != nil {
			return 0, err
		}
		if err := c.references(v, 0); err != nil {
			return 0, err
		}
	}
	return c.bytes, nil
}

func (c *payloadCounter) add(n uint64) error {
	if n > uint64(math.MaxInt64-c.bytes) {
		return ErrWriterInvalidPayload
	}
	c.bytes += int64(n)
	return nil
}

func (c *payloadCounter) backing(count int, size uintptr) error {
	if count < 0 || (size > 0 && uint64(count) > uint64(math.MaxInt64)/uint64(size)) {
		return ErrWriterInvalidPayload
	}
	return c.add(uint64(count) * uint64(size))
}

func (c *payloadCounter) references(v reflect.Value, depth int) error {
	c.nodes++
	if depth > 64 || c.nodes > 1<<20 {
		return ErrWriterInvalidPayload
	}
	if isWriterUTCTime(v) {
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		return c.add(uint64(v.Len()))
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		e := v.Elem()
		if err := c.add(uint64(e.Type().Size())); err != nil {
			return err
		}
		return c.references(e, depth+1)
	case reflect.Slice:
		if err := c.backing(v.Len(), v.Type().Elem().Size()); err != nil {
			return err
		}
		if !payloadHasReferences(v.Type().Elem()) {
			return nil
		}
		for i := 0; i < v.Len(); i++ {
			if err := c.references(v.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Array:
		if !payloadHasReferences(v.Type().Elem()) {
			return nil
		}
		for i := 0; i < v.Len(); i++ {
			if err := c.references(v.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath != "" {
				return ErrWriterInvalidPayload
			}
			if err := c.references(v.Field(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String || v.Type().Elem().Kind() != reflect.String {
			return ErrWriterInvalidPayload
		}
		if v.IsNil() {
			return nil
		}
		if err := c.add(64); err != nil {
			return err
		}
		for _, size := range []uintptr{v.Type().Key().Size(), v.Type().Elem().Size(), 32} {
			if err := c.backing(v.Len(), size); err != nil {
				return err
			}
			if err := c.backing(v.Len(), size); err != nil {
				return err
			}
		}
		it := v.MapRange()
		for it.Next() {
			if err := c.references(it.Key(), depth+1); err != nil {
				return err
			}
			if err := c.references(it.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return ErrWriterInvalidPayload
	}
	return nil
}

func isWriterUTCTime(v reflect.Value) bool {
	return v.Type() == reflect.TypeFor[time.Time]() && v.CanInterface() && v.Interface().(time.Time).Location() == time.UTC
}

func payloadHasReferences(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return false
	case reflect.Array:
		return payloadHasReferences(t.Elem())
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if payloadHasReferences(t.Field(i).Type) {
				return true
			}
		}
		return false
	default:
		return true
	}
}
