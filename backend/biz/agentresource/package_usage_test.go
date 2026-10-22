package agentresource

import "testing"

func TestPackageUsageEnforcesAggregateByteAndFileBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b PackageUsage
		ok   bool
	}{
		{"bytes exact", PackageUsage{1, 128 << 20}, PackageUsage{1, 128 << 20}, true},
		{"bytes exceeded", PackageUsage{1, 128 << 20}, PackageUsage{1, (128 << 20) + 1}, false},
		{"files exact", PackageUsage{600, 1}, PackageUsage{400, 1}, true},
		{"files exceeded", PackageUsage{600, 1}, PackageUsage{401, 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total := tc.a
			err := total.Add(tc.b)
			if (err == nil) != tc.ok {
				t.Fatalf("usage: %+v + %+v: %v", tc.a, tc.b, err)
			}
			if err != nil && total != tc.a {
				t.Fatal("rejected addition changed usage")
			}
		})
	}
}
