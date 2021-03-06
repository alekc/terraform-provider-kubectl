package flatten

import (
	"fmt"
	"reflect"
)

// Flatten takes a structure and turns into a flat map[string]string.
//
// Based on the Terraform implementation at https://github.com/hashicorp/terraform/blob/master/flatmap/flatten.go
// Added more generic handling for different types
//
// Within the "thing" parameter, only primitive values are allowed. Structs are
// not supported. Therefore, it can only be slices, maps, primitives, and
// any combination of those together.
//
// See the tests for examples of what inputs are turned into.
func Flatten(thing map[string]interface{}) map[string]string {
	result := make(map[string]string)

	for k, raw := range thing {
		// when the raw value is nil, skip it to treat it like an empty map/string, i.e. it's not kept in the result map
		if raw == nil {
			continue
		}

		// ignore any keys which are empty strings, as there isn't anything reference it in the resultant map
		if k == "" {
			continue
		}

		flatten(result, k, reflect.ValueOf(raw))
	}

	return result
}

func flatten(result map[string]string, prefix string, v reflect.Value) {
	if v.Kind() == reflect.Invalid {
		return
	}

	if v.Kind() == reflect.Interface {
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			result[prefix] = "true"
		} else {
			result[prefix] = "false"
		}
	case reflect.Int:
		result[prefix] = fmt.Sprintf("%d", v.Int())
	case reflect.Map:
		flattenMap(result, prefix, v)
	case reflect.Slice:
		flattenSlice(result, prefix, v)
	case reflect.String:
		result[prefix] = v.String()
	default:
		// fallback to default string printing
		result[prefix] = fmt.Sprintf("%v", v)
	}
}

func flattenMap(result map[string]string, prefix string, v reflect.Value) {
	for _, k := range v.MapKeys() {
		if k.Kind() == reflect.Interface {
			k = k.Elem()
		}

		key := fmt.Sprintf("%v", k)
		flatten(result, fmt.Sprintf("%s.%s", prefix, key), v.MapIndex(k))
	}
}

func flattenSlice(result map[string]string, prefix string, v reflect.Value) {
	prefix = prefix + "."

	result[prefix+"#"] = fmt.Sprintf("%d", v.Len())
	for i := 0; i < v.Len(); i++ {
		flatten(result, fmt.Sprintf("%s%d", prefix, i), v.Index(i))
	}
}
