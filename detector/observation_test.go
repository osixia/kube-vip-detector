package detector

import (
	"errors"
	"testing"
)

func TestHysteresis(t *testing.T) {
	s := observation{}
	for i, x := range []struct {
		node   string
		err    error
		apply  bool
		winner string
	}{
		{"a", nil, false, "a"}, {"b", nil, false, "b"}, {"b", nil, true, "b"},
		{"", errors.New("timeout"), false, ""}, {"", errors.New("timeout"), false, ""}, {"", errors.New("timeout"), true, ""},
		{"b", nil, false, "b"}, {"b", nil, true, "b"},
	} {
		apply, winner := s.update(x.node, x.err, 2, 3)
		if apply != x.apply || winner != x.winner {
			t.Fatalf("step %d: %v %q", i, apply, winner)
		}
	}
}
