package nws

import (
	"testing"
)

func TestParseVTEC(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		params      map[string][]string
		wantNil     bool
		wantErr     bool
		wantEventID string
		wantAction  string
	}{
		{
			name:    "absent field returns nil,nil",
			params:  nil,
			wantNil: true,
		},
		{
			name:    "empty slice returns nil,nil",
			params:  map[string][]string{"VTEC": {}},
			wantNil: true,
		},
		{
			name:    "blank string returns nil,nil",
			params:  map[string][]string{"VTEC": {"   "}},
			wantNil: true,
		},
		{
			name: "well-formed NEW P-VTEC parses event id + action",
			params: map[string][]string{
				"VTEC": {"/O.NEW.KIND.FL.W.0102.000000T0000Z-260531T1800Z/"},
			},
			wantEventID: "KIND.FL.W.0102",
			wantAction:  "NEW",
		},
		{
			name: "continuation keeps the same event id (only action differs)",
			params: map[string][]string{
				"VTEC": {"/O.CON.KIND.FL.W.0102.000000T0000Z-260531T1800Z/"},
			},
			wantEventID: "KIND.FL.W.0102",
			wantAction:  "CON",
		},
		{
			name: "cancel action parses",
			params: map[string][]string{
				"VTEC": {"/O.CAN.KMKX.SV.W.0087.260530T2200Z-260530T2245Z/"},
			},
			wantEventID: "KMKX.SV.W.0087",
			wantAction:  "CAN",
		},
		{
			name: "multi-element array picks the operational P-VTEC, ignores H-VTEC",
			params: map[string][]string{
				"VTEC": {
					"/O.EXT.KIND.FL.W.0102.000000T0000Z-260603T1930Z/",
					"/00000.0.ER.000000T0000Z.000000T0000Z.000000T0000Z.OO/",
				},
			},
			wantEventID: "KIND.FL.W.0102",
			wantAction:  "EXT",
		},
		{
			name:    "present but unparseable returns error",
			params:  map[string][]string{"VTEC": {"not a vtec string"}},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseVTEC(tc.params)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (got=%v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected a VTEC, got nil")
			}
			if got.EventID() != tc.wantEventID {
				t.Fatalf("EventID: got %q want %q", got.EventID(), tc.wantEventID)
			}
			if got.Action != tc.wantAction {
				t.Fatalf("Action: got %q want %q", got.Action, tc.wantAction)
			}
		})
	}
}
