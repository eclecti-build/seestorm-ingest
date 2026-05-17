package nws

import (
	"testing"
)

// svsRadarIndicatedNarrative mirrors the real WI Severe Weather Statement
// that prompted this parser: an SVS *update* to a Tornado Warning. It
// carries the SVS narrative format (HAZARD/SOURCE/IMPACT) with NO `&&` tag
// block — the detection lives ONLY in properties.parameters. This is the
// exact shape the legacy tag-only path could not see.
const svsRadarIndicatedNarrative = `At 1137 AM CDT, a severe thunderstorm capable of producing a tornado
was located over northeastern Madison, moving east at 25 mph.

HAZARD...Tornado.

SOURCE...Radar indicated rotation.

IMPACT...Flying debris will be dangerous to those caught without
shelter. Mobile homes will be damaged or destroyed.

This dangerous storm will be near...
Eastern Madison and Sun Prairie around 1145 AM CDT.`

// svsConfirmedNarrative is an SVS narrative whose SOURCE line attributes a
// human observer but with NO structured parameter and NO `&&` block. Tier 3
// must DELIBERATELY refuse to infer OBSERVED from this — under-claiming is
// the safe failure mode.
const svsConfirmedNarrative = `At 1150 AM CDT, a confirmed tornado was located near Sun Prairie.

HAZARD...Tornado.

SOURCE...Weather spotters confirmed tornado.

IMPACT...You are in a life-threatening situation.`

func TestDetectTornado(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		params      map[string][]string
		description string
		wantNil     bool // expect (nil, nil)
		wantErr     bool // expect (nil, err)
		want        TornadoDetection
	}{
		{
			name:    "non-tornado alert: no params, no tags, no hazard",
			params:  map[string][]string{"VTEC": {"/O.NEW.KMKX.SV.W.0001.000000T0000Z-260517T1700Z/"}},
			wantNil: true,
		},
		{
			name:        "tier 1: structured RADAR INDICATED (the SVS-update case)",
			params:      map[string][]string{"tornadoDetection": {"RADAR INDICATED"}},
			description: svsRadarIndicatedNarrative,
			want: TornadoDetection{
				Detection:    DetectionRadarIndicated,
				Confirmed:    false,
				DamageThreat: DamageThreatBase,
				SourceText:   "Radar indicated rotation",
			},
		},
		{
			name:   "tier 1: structured OBSERVED",
			params: map[string][]string{"tornadoDetection": {"OBSERVED"}},
			want: TornadoDetection{
				Detection:    DetectionObserved,
				Confirmed:    true,
				DamageThreat: DamageThreatBase,
			},
		},
		{
			name: "tier 1: OBSERVED + CONSIDERABLE damage threat (PDS)",
			params: map[string][]string{
				"tornadoDetection":    {"OBSERVED"},
				"tornadoDamageThreat": {"CONSIDERABLE"},
			},
			want: TornadoDetection{
				Detection:    DetectionObserved,
				Confirmed:    true,
				DamageThreat: DamageThreatConsiderable,
			},
		},
		{
			name: "tier 1: OBSERVED + CATASTROPHIC damage threat (Tornado Emergency)",
			params: map[string][]string{
				"tornadoDetection":    {"OBSERVED"},
				"tornadoDamageThreat": {"CATASTROPHIC"},
			},
			want: TornadoDetection{
				Detection:    DetectionObserved,
				Confirmed:    true,
				DamageThreat: DamageThreatCatastrophic,
			},
		},
		{
			name:        "tier 1 precedence: param OBSERVED overrides conflicting tag RADAR INDICATED",
			params:      map[string][]string{"tornadoDetection": {"OBSERVED"}},
			description: "Prose.\n\nSOURCE...Law enforcement confirmed tornado.\n\n&&\n\nTORNADO...RADAR INDICATED\n\n&&\n",
			want: TornadoDetection{
				Detection:    DetectionObserved,
				Confirmed:    true,
				DamageThreat: DamageThreatBase,
				SourceText:   "Law enforcement confirmed tornado",
			},
		},
		{
			name:        "tier 2: && tag OBSERVED with no structured param",
			description: "At 645 PM CDT, a confirmed tornado near Janesville.\n\nSOURCE...Law enforcement confirmed tornado.\n\n&&\n\nTORNADO...OBSERVED\nTORNADO DAMAGE THREAT...CONSIDERABLE\n\n&&\n",
			want: TornadoDetection{
				Detection:    DetectionObserved,
				Confirmed:    true,
				DamageThreat: DamageThreatConsiderable,
				SourceText:   "Law enforcement confirmed tornado",
			},
		},
		{
			name:        "tier 2: TORNADO...POSSIBLE (SVR embedded) is NOT a detection",
			description: "Severe thunderstorm.\n\n&&\n\nTORNADO...POSSIBLE\nMAX WIND GUST...70 MPH\n\n&&\n",
			wantNil:     true,
		},
		{
			name:    "tier 1: structured POSSIBLE (SVR embedded) is a known non-detection, not drift",
			params:  map[string][]string{"tornadoDetection": {"POSSIBLE"}},
			wantNil: true,
		},
		{
			name:        "tier 3: conservative narrative yields RADAR_INDICATED only",
			description: svsRadarIndicatedNarrative,
			want: TornadoDetection{
				Detection:    DetectionRadarIndicated,
				Confirmed:    false,
				DamageThreat: DamageThreatBase,
				SourceText:   "Radar indicated rotation",
			},
		},
		{
			name:        "tier 3: narrative with human source does NOT infer OBSERVED",
			description: svsConfirmedNarrative,
			wantNil:     true,
		},
		{
			name:        "tier 3 gating: SVR radar-indicated narrative is not a tornado detection",
			description: "Severe thunderstorm.\n\nHAZARD...70 mph wind gusts.\n\nSOURCE...Radar indicated.\n\nIMPACT...Damaging winds.",
			wantNil:     true,
		},
		{
			name:        "tier 3 gating: SVR 'tornado possible' hazard must NOT become a detection",
			description: "Severe thunderstorm.\n\nHAZARD...60 mph wind gusts and a tornado possible.\n\nSOURCE...Radar indicated.\n\nIMPACT...Damaging winds.",
			wantNil:     true,
		},
		{
			name:        "tier 2 drift: unknown TORNADO tag (no structured param) surfaces an error",
			description: "Prose.\n\n&&\n\nTORNADO...SATELLITE DERIVED\n\n&&\n",
			wantErr:     true,
		},
		{
			name:    "drift: unrecognized structured tornadoDetection surfaces an error",
			params:  map[string][]string{"tornadoDetection": {"SATELLITE DERIVED"}},
			wantErr: true,
		},
		{
			name: "whitespace/casing tolerance on structured value",
			params: map[string][]string{
				"tornadoDetection": {"  radar   indicated "},
			},
			want: TornadoDetection{
				Detection:    DetectionRadarIndicated,
				Confirmed:    false,
				DamageThreat: DamageThreatBase,
			},
		},
		{
			name:    "blank structured entry is treated as absent, not an error",
			params:  map[string][]string{"tornadoDetection": {"   "}},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := DetectTornado(tt.params, tt.description)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got nil (det=%+v)", got)
				}
				if got != nil {
					t.Fatalf("expected nil detection on error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected (nil, nil), got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %+v, got nil", tt.want)
			}
			if *got != tt.want {
				t.Fatalf("detection mismatch:\n got  %+v\n want %+v", *got, tt.want)
			}
		})
	}
}
