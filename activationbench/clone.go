//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package activationbench

import "reflect"

// cloneVisit identifies reference values while cloning TaskState initial
// values.  Keeping a visited map preserves aliases and prevents recursion
// through cyclic maps/pointers from overflowing the stack.
type cloneVisit struct {
	typ      reflect.Type
	kind     reflect.Kind
	ptr      uintptr
	length   int
	capacity int
}

// cloneInitialValues makes a deep, type-preserving copy of the generic values
// supplied to NewTaskState.  JSON round-tripping is deliberately avoided: it
// would turn typed fixture structs (for example env.State) into maps and
// break handlers that use type assertions.  Values with inherently shared
// semantics such as functions and channels are retained as-is.
func cloneInitialValues(initial map[string]any) map[string]any {
	values := make(map[string]any, len(initial))
	seen := make(map[cloneVisit]reflect.Value)
	for key, value := range initial {
		if value == nil {
			values[key] = nil
			continue
		}
		cloned := cloneStateValue(reflect.ValueOf(value), seen)
		if cloned.IsValid() && cloned.CanInterface() {
			values[key] = cloned.Interface()
		} else {
			// This branch is only reachable for an unusual value containing
			// inaccessible reflection state. Preserve it rather than returning
			// a zero value and silently changing task semantics.
			values[key] = value
		}
	}
	return values
}

func cloneStateValue(value reflect.Value, seen map[cloneVisit]reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneStateValue(value.Elem(), seen)
		out := reflect.New(value.Type()).Elem()
		if cloned.IsValid() && (cloned.Type().AssignableTo(value.Type()) || cloned.Type().Implements(value.Type())) {
			out.Set(cloned)
			return out
		}
		// An interface with an inaccessible concrete value can only be
		// preserved shallowly; this is safer than panicking during setup.
		out.Set(value)
		return out

	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		key := cloneVisit{typ: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
		if cloned, ok := seen[key]; ok {
			return cloned
		}
		out := reflect.New(value.Type().Elem())
		seen[key] = out
		cloned := cloneStateValue(value.Elem(), seen)
		if cloned.IsValid() && cloned.Type().AssignableTo(out.Elem().Type()) {
			out.Elem().Set(cloned)
		} else {
			out.Elem().Set(value.Elem())
		}
		return out

	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		key := cloneVisit{typ: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
		if cloned, ok := seen[key]; ok {
			return cloned
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		seen[key] = out
		iter := value.MapRange()
		for iter.Next() {
			clonedKey := cloneStateValue(iter.Key(), seen)
			clonedValue := cloneStateValue(iter.Value(), seen)
			if !clonedKey.IsValid() || !clonedValue.IsValid() {
				continue
			}
			if !clonedKey.Type().AssignableTo(value.Type().Key()) {
				clonedKey = iter.Key()
			}
			if !clonedValue.Type().AssignableTo(value.Type().Elem()) {
				clonedValue = iter.Value()
			}
			out.SetMapIndex(clonedKey, clonedValue)
		}
		return out

	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		key := cloneVisit{
			typ:      value.Type(),
			kind:     value.Kind(),
			ptr:      value.Pointer(),
			length:   value.Len(),
			capacity: value.Cap(),
		}
		if cloned, ok := seen[key]; ok {
			return cloned
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Cap())
		seen[key] = out
		for index := 0; index < value.Len(); index++ {
			cloned := cloneStateValue(value.Index(index), seen)
			if cloned.IsValid() && cloned.Type().AssignableTo(out.Index(index).Type()) {
				out.Index(index).Set(cloned)
			} else {
				out.Index(index).Set(value.Index(index))
			}
		}
		return out

	case reflect.Struct:
		// Start with a shallow copy so unexported fields remain valid. Exported
		// fields are then recursively cloned; benchmark fixture structs use
		// exported fields, while immutable library structs can safely retain
		// their private representation.
		out := reflect.New(value.Type()).Elem()
		out.Set(value)
		for index := 0; index < value.NumField(); index++ {
			destination := out.Field(index)
			source := value.Field(index)
			if !destination.CanSet() || !source.CanInterface() {
				continue
			}
			cloned := cloneStateValue(source, seen)
			if cloned.IsValid() && cloned.Type().AssignableTo(destination.Type()) {
				destination.Set(cloned)
			}
		}
		return out

	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			cloned := cloneStateValue(value.Index(index), seen)
			if cloned.IsValid() && cloned.Type().AssignableTo(out.Index(index).Type()) {
				out.Index(index).Set(cloned)
			} else {
				out.Index(index).Set(value.Index(index))
			}
		}
		return out

	default:
		// Scalars, funcs, chans, and unsafe pointers are either immutable or
		// intentionally shared. Returning the original value is the least
		// surprising behavior for those kinds.
		return value
	}
}
