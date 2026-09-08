package store

import (
	"context"
	"math"
	"reflect"
	"strings"
)

func (w *runtimeWriter) admitOwned(ctx context.Context, payload []any, run func([]any) error) (*writerRequest, error) {
	return w.admitOwnedReserved(ctx, payload, 0, run)
}

func (w *runtimeWriter) admitOwnedReserved(ctx context.Context, payload []any, reserve int64, run func([]any) error) (*writerRequest, error) {
	size, err := referencedPayloadBytes(payload...)
	if err != nil {
		return nil, w.reject(err)
	}
	if reserve < 0 || reserve > math.MaxInt64-size {
		return nil, w.reject(ErrWriterInvalidPayload)
	}
	size += reserve
	return w.admitPrepared(ctx, size, func(r *writerRequest) {
		owned := make([]any, len(payload))
		for i, value := range payload {
			if value != nil {
				owned[i] = cloneWriterValue(reflect.ValueOf(value)).Interface()
			}
		}
		r.run = func() error { return run(owned) }
	})
}

func cloneWriterValue(v reflect.Value) reflect.Value {
	if isWriterUTCTime(v) {
		return v
	}
	switch v.Kind() {
	case reflect.String:
		return reflect.ValueOf(strings.Clone(v.String())).Convert(v.Type())
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(cloneWriterValue(v.Elem()))
		return out.Convert(v.Type())
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(cloneWriterValue(v.Elem()))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		if !payloadHasReferences(v.Type().Elem()) {
			reflect.Copy(out, v)
			return out
		}
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneWriterValue(v.Index(i)))
		}
		return out
	case reflect.Array, reflect.Struct:
		if !payloadHasReferences(v.Type()) {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		if v.Kind() == reflect.Array {
			for i := 0; i < v.Len(); i++ {
				out.Index(i).Set(cloneWriterValue(v.Index(i)))
			}
		} else {
			for i := 0; i < v.NumField(); i++ {
				out.Field(i).Set(cloneWriterValue(v.Field(i)))
			}
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		it := v.MapRange()
		for it.Next() {
			out.SetMapIndex(cloneWriterValue(it.Key()), cloneWriterValue(it.Value()))
		}
		return out
	default:
		return v
	}
}
