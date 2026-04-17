package store

import (
	"reflect"
	"testing"
)

func TestDeriveStates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name             string
		sameCodes        []string
		areaDesc         string
		wantStates       []string
		wantUsedFallback bool
	}{
		{
			name:       "single_state_from_same",
			sameCodes:  []string{"055025", "055045"},
			areaDesc:   "Dane, WI; Rock, WI",
			wantStates: []string{"WI"},
		},
		{
			name:       "cross_border_from_same",
			sameCodes:  []string{"055023", "019191"},
			areaDesc:   "Crawford, WI; Allamakee, IA",
			wantStates: []string{"IA", "WI"},
		},
		{
			name:       "great_lakes_multi_from_same",
			sameCodes:  []string{"017031", "018097", "026163", "039049"},
			areaDesc:   "Cook, IL; Marion, IN; Wayne, MI; Franklin, OH",
			wantStates: []string{"IL", "IN", "MI", "OH"},
		},
		{
			name:             "fallback_to_area_desc_when_same_empty",
			sameCodes:        nil,
			areaDesc:         "Dane, WI; Rock, WI; Cook, IL",
			wantStates:       []string{"IL", "WI"},
			wantUsedFallback: true,
		},
		{
			name:             "fallback_when_same_unknown_prefix",
			sameCodes:        []string{"999999"},
			areaDesc:         "Some County, MN",
			wantStates:       []string{"MN"},
			wantUsedFallback: true,
		},
		{
			name:             "fallback_rejects_non_state_trailing_tokens",
			sameCodes:        nil,
			areaDesc:         "OFFSHORE WATERS, ZZ; Dane, WI",
			wantStates:       []string{"WI"},
			wantUsedFallback: true,
		},
		{
			name:             "no_data_returns_nil",
			sameCodes:        nil,
			areaDesc:         "",
			wantStates:       nil,
			wantUsedFallback: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, gotFallback := deriveStates(tc.sameCodes, tc.areaDesc)
			if !reflect.DeepEqual(got, tc.wantStates) {
				t.Errorf("states: got %v want %v", got, tc.wantStates)
			}
			if gotFallback != tc.wantUsedFallback {
				t.Errorf("usedFallback: got %v want %v", gotFallback, tc.wantUsedFallback)
			}
		})
	}
}
