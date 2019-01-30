package util

import (
	"fmt"
	"reflect"

	"github.com/google/go-cmp/cmp"
)

// ElementsEqual return true if listX items match listY items ignoring the order of the items.
// In case of duplicate items, the number of appearances of each item in both lists should match.
// listX and listY must be an array or slice.
func ElementsEqual(listX, listY interface{}, opts []cmp.Option) (bool, error) {
	if listX == nil && listY == nil {
		return true, nil
	} else if listX == nil || listY == nil {
		return false, nil
	}

	xKind := reflect.TypeOf(listX).Kind()
	yKind := reflect.TypeOf(listY).Kind()
	if xKind != reflect.Array && xKind != reflect.Slice {
		return false, fmt.Errorf("unsupported type %s for %q", xKind, listX)
	}
	if yKind != reflect.Array && yKind != reflect.Slice {
		return false, fmt.Errorf("unsupported type %s for %q", yKind, listY)
	}

	xValue := reflect.ValueOf(listX)
	yValue := reflect.ValueOf(listY)
	xLen := xValue.Len()
	yLen := yValue.Len()
	if xLen != yLen {
		return false, nil
	}

	visited := make([]bool, yLen)
	for i := 0; i < xLen; i++ {
		found := false
		for j := 0; j < yLen; j++ {
			if visited[j] {
				continue
			}
			if cmp.Equal(xValue.Index(i).Interface(), yValue.Index(j).Interface(), opts...) {
				visited[j] = true
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}

	return true, nil
}
