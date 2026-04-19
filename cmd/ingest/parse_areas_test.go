package main

import (
	"reflect"
	"testing"
)

func TestParseAreas(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "empty defaults to WI",
			raw:  "",
			want: []string{"WI"},
		},
		{
			name: "whitespace defaults to WI",
			raw:  "   ",
			want: []string{"WI"},
		},
		{
			name: "single state passthrough",
			raw:  "WI",
			want: []string{"WI"},
		},
		{
			name: "lowercase normalized",
			raw:  "wi",
			want: []string{"WI"},
		},
		{
			name: "great lakes basin",
			raw:  "MN,WI,IL,IN,MI,OH,PA,NY,IA",
			want: []string{"MN", "WI", "IL", "IN", "MI", "OH", "PA", "NY", "IA"},
		},
		{
			name: "tolerates spaces around commas",
			raw:  " MN , WI , IL ",
			want: []string{"MN", "WI", "IL"},
		},
		{
			name: "deduplicates",
			raw:  "WI,WI,IL,WI",
			want: []string{"WI", "IL"},
		},
		{
			name: "drops bogus codes that match the regex but aren't real states",
			raw:  "WI,ZZ,IL,XX",
			want: []string{"WI", "IL"},
		},
		{
			name: "drops codes that fail the regex",
			raw:  "WI,W1,X,IL,ABC,!!",
			want: []string{"WI", "IL"},
		},
		{
			name: "includes territory codes",
			raw:  "WI,PR,GU",
			want: []string{"WI", "PR", "GU"},
		},
		{
			name: "all-bogus input yields empty",
			// Caller (`run` in main.go) treats an empty result as a startup error.
			raw:  "ZZ,XX,AA",
			want: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseAreas(tc.raw)
			// Normalize: empty slice vs nil — both are equivalent inputs to
			// the caller.
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseAreas(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
