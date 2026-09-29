package metrics

import (
	"math"
	"testing"
)

func TestParseSMI(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		wantErr bool
		check   func(*testing.T, *GPUStats)
	}{
		{
			name: "verified live output",
			out:  "NVIDIA GB10, 96, 67, 30.33, 2398\n",
			check: func(t *testing.T, g *GPUStats) {
				if g.Name != "NVIDIA GB10" || *g.UtilPct != 96 || *g.TempC != 67 {
					t.Fatalf("bad fields: %+v", g)
				}
				if math.Abs(*g.PowerW-30.33) > 1e-9 || *g.SMClockMHz != 2398 {
					t.Fatalf("bad power/clock: %v %v", *g.PowerW, *g.SMClockMHz)
				}
			},
		},
		{
			name: "comma-decimal locale (power splits the CSV)",
			out:  "NVIDIA GB10, 96, 67, 30,33, 2398\n",
			check: func(t *testing.T, g *GPUStats) {
				if math.Abs(*g.PowerW-30.33) > 1e-9 {
					t.Fatalf("power = %v, want 30.33", *g.PowerW)
				}
				if *g.SMClockMHz != 2398 {
					t.Fatalf("clock = %v, want 2398 (not the decimal fragment)", *g.SMClockMHz)
				}
			},
		},
		{
			name: "N/A field stays nil, rest kept",
			out:  "NVIDIA GB10, 0, 45, [N/A], 600\n",
			check: func(t *testing.T, g *GPUStats) {
				if g.PowerW != nil {
					t.Fatalf("power = %v, want nil", *g.PowerW)
				}
				if *g.UtilPct != 0 || *g.TempC != 45 || *g.SMClockMHz != 600 {
					t.Fatalf("bad fields: %+v", g)
				}
			},
		},
		{
			name:    "garbage",
			out:     "hello world\n",
			wantErr: true,
		},
		{
			name:    "empty",
			out:     "\n\n",
			wantErr: true,
		},
		{
			name: "multi-line takes first (single GPU)",
			out:  "NVIDIA GB10, 5, 40, 10.5, 500\nNVIDIA GB10, 6, 41, 11.5, 501\n",
			check: func(t *testing.T, g *GPUStats) {
				if *g.UtilPct != 5 || *g.SMClockMHz != 500 {
					t.Fatalf("bad fields: %+v", g)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, err := parseSMI(tc.out)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", g)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tc.check(t, g)
		})
	}
}

func TestParseSMINum(t *testing.T) {
	if v := parseSMINum(" 30,33 "); v == nil || *v != 30.33 {
		t.Fatalf("comma decimal: %v", v)
	}
	if v := parseSMINum("42"); v == nil || *v != 42 {
		t.Fatalf("int: %v", v)
	}
	for _, s := range []string{"[N/A]", "N/A", "", "abc", "1.2.3", "--5", "96 %"} {
		if v := parseSMINum(s); v != nil {
			t.Fatalf("%q: want nil, got %v", s, *v)
		}
	}
}
