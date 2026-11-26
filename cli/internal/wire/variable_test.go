package wire

import "testing"

func TestIsVariable(t *testing.T) {
	for _, c := range []struct {
		name string
		want bool
	}{
		{"A", true},
		{"z", true},
		{"_", true},
		{"TOKEN", true},
		{"_token2", true},
		{"a0_Z9", true},
		{"", false},
		{"0", false},
		{"1TOKEN", false},
		{"A-B", false},
		{"A.B", false},
		{"A B", false},
		{"A=B", false},
		{"A$", false},
		{"é", false},
		{"Aé", false},
		{"A١", false},
		{"\nTOKEN", false},
		{"TOKEN\n", false},
		{"A\nB", false},
		{"A\tB", false},
		{"A\x00B", false},
		{"A\xff", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := IsVariable(c.name); got != c.want {
				t.Errorf("IsVariable(%q) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}
