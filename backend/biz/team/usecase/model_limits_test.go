package usecase

import "testing"

func TestModelLimitsValidatePartialUpdates(t *testing.T) {
	for _, c := range []struct {
		context, output int
		bad             bool
	}{{128000, 32000, false}, {0, 32000, true}, {128000, -1, true}, {32000, 64000, true}} {
		if bad := validateModelLimits(&c.context, &c.output, 200000, 64000) != nil; bad != c.bad {
			t.Fatalf("limits %+v bad=%v", c, bad)
		}
	}
	small := 32000
	if validateModelLimits(&small, nil, 200000, 64000) == nil {
		t.Fatal("partial update ignored existing output limit")
	}
	if validateModelLimits(nil, nil, 200000, 64000) != nil {
		t.Fatal("defaults rejected")
	}
}
