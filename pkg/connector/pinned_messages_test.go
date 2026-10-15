package connector

import (
	"reflect"
	"testing"
)

func TestMergePinnedItems(t *testing.T) {
	for _, tt := range []struct {
		current, wanted, want []string
	}{
		{nil, []string{"a"}, []string{"a"}},
		{[]string{"b", "a"}, []string{"a", "b", "c"}, []string{"c", "b", "a"}},
		{[]string{"b", "a"}, []string{"a"}, []string{"a"}},
		{[]string{"b", "a"}, []string{"a", "b", "c", "d"}, []string{"d", "c", "b", "a"}},
		{[]string{"b", "a"}, nil, nil},
	} {
		if got := mergePinnedItems(tt.current, tt.wanted); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("mergePinnedItems(%v, %v) = %v, want %v", tt.current, tt.wanted, got, tt.want)
		}
	}
}
