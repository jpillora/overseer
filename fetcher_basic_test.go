package upgrade

import (
	"reflect"
	"testing"
)

func TestVersionIncs(t *testing.T) {

	tests := []struct {
		version  string
		expected []string
	}{
		{"0.1.0", []string{"0.1.1", "0.2.0", "1.0.0"}},
		{"0.3.1", []string{"0.3.2", "0.4.0", "1.0.0"}},
	}

	for i, test := range tests {

		vers, err := getAllVersionIncrements(test.version)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(vers, test.expected) {
			t.Fatalf("test %d failed:\nexpecting: %#v\n      got: %#v", i, test.expected, vers)
		}
	}
}
