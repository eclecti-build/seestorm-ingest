package nws

import (
	"testing"
)

// wiTornadoWarningSample mirrors a real Wisconsin Tornado Warning from NWS
// Milwaukee/Sullivan. Prose + TIME...MOT...LOC + `&&`-delimited IBW tag block.
// The trailing `&&` is present — real NWS products close the tag section.
const wiTornadoWarningSample = `At 645 PM CDT, a confirmed tornado was located near Janesville, moving northeast at 40 mph.

HAZARD...Damaging tornado.

SOURCE...Law enforcement confirmed tornado.

IMPACT...Flying debris will be dangerous to those caught without
shelter. Mobile homes will be damaged or destroyed.

&&

TIME...MOT...LOC 2345Z 230DEG 35KT 4268 8895

TORNADO...OBSERVED
TORNADO DAMAGE THREAT...CONSIDERABLE
HAIL...1.75IN
MAX HAIL SIZE...1.75 IN

&&

LAT...LON 4268 8895 4272 8880
`

// wiSVRWarningSample mirrors a real WI Severe Thunderstorm Warning. Uses the
// THUNDERSTORM DAMAGE THREAT tag family and MAX WIND GUST.
const wiSVRWarningSample = `At 715 PM CDT, a severe thunderstorm was located over Madison, moving east at 45 mph.

HAZARD...70 mph wind gusts and quarter size hail.

SOURCE...Radar indicated.

&&

MAX HAIL SIZE...1.00 IN
MAX WIND GUST...70 MPH
THUNDERSTORM DAMAGE THREAT...CONSIDERABLE
`

// wiFlashFloodWarningSample mirrors the Flash Flood family tags.
const wiFlashFloodWarningSample = `At 820 PM CDT, Doppler radar indicated thunderstorms producing heavy rain.

&&

FLASH FLOOD...RADAR INDICATED
FLASH FLOOD DAMAGE THREAT...CONSIDERABLE
EXPECTED RAINFALL RATE...2 INCHES IN 1 HOUR
`

func TestParseWarningTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		description string
		wantNil     bool // expect (nil, nil)
		want        WarningTags
	}{
		{
			name:        "no delimiter returns (nil, nil)",
			description: "FLOOD WATCH for central Wisconsin. No structured tags here.",
			wantNil:     true,
		},
		{
			name:        "delimiter but no recognized tags returns (nil, nil)",
			description: "Prose.\n\n&&\n\nUNRECOGNIZED...FOO\nALSO UNRECOGNIZED...BAR\n\n&&\n",
			wantNil:     true,
		},
		{
			name: "tornado warning full tag set",
			description: "Prose.\n\n&&\n\n" +
				"TORNADO...OBSERVED\n" +
				"TORNADO DAMAGE THREAT...CONSIDERABLE\n" +
				"HAIL...1.75IN\n" +
				"MAX HAIL SIZE...1.75 IN\n" +
				"\n&&\n",
			want: WarningTags{
				Tornado:             "OBSERVED",
				TornadoDamageThreat: "CONSIDERABLE",
				Hail:                "1.75IN",
				MaxHailSize:         "1.75 IN",
			},
		},
		{
			name:        "real WI tornado warning sample",
			description: wiTornadoWarningSample,
			want: WarningTags{
				Tornado:             "OBSERVED",
				TornadoDamageThreat: "CONSIDERABLE",
				Hail:                "1.75IN",
				MaxHailSize:         "1.75 IN",
			},
		},
		{
			name:        "real WI SVR warning sample",
			description: wiSVRWarningSample,
			want: WarningTags{
				MaxHailSize:              "1.00 IN",
				MaxWindGust:              "70 MPH",
				ThunderstormDamageThreat: "CONSIDERABLE",
			},
		},
		{
			name:        "flash flood warning sample",
			description: wiFlashFloodWarningSample,
			want: WarningTags{
				FlashFlood:             "RADAR INDICATED",
				FlashFloodDamageThreat: "CONSIDERABLE",
				ExpectedRainfallRate:   "2 INCHES IN 1 HOUR",
			},
		},
		{
			name: "tornado RADAR INDICATED without damage threat",
			description: "Prose.\n\n&&\n\n" +
				"TORNADO...RADAR INDICATED\n" +
				"HAIL...<.75IN\n" +
				"MAX HAIL SIZE...0.50 IN\n\n&&",
			want: WarningTags{
				Tornado:     "RADAR INDICATED",
				Hail:        "<.75IN",
				MaxHailSize: "0.50 IN",
			},
		},
		{
			name: "damage threat DESTRUCTIVE",
			description: "&&\n" +
				"TORNADO...OBSERVED\n" +
				"TORNADO DAMAGE THREAT...DESTRUCTIVE\n" +
				"&&",
			want: WarningTags{
				Tornado:             "OBSERVED",
				TornadoDamageThreat: "DESTRUCTIVE",
			},
		},
		{
			name: "damage threat CATASTROPHIC",
			description: "&&\n" +
				"TORNADO...OBSERVED\n" +
				"TORNADO DAMAGE THREAT...CATASTROPHIC\n" +
				"&&",
			want: WarningTags{
				Tornado:             "OBSERVED",
				TornadoDamageThreat: "CATASTROPHIC",
			},
		},
		{
			name: "unrecognized tag silently skipped",
			description: "&&\n" +
				"TORNADO...OBSERVED\n" +
				"SOMETHING NEW...YES\n" +
				"MAX HAIL SIZE...1.00 IN\n" +
				"&&",
			want: WarningTags{
				Tornado:     "OBSERVED",
				MaxHailSize: "1.00 IN",
			},
		},
		{
			name: "tabs and extra whitespace tolerated",
			description: "&&\n" +
				"TORNADO...OBSERVED\n" +
				"  MAX HAIL SIZE...1.75 IN  \n" +
				"&&",
			want: WarningTags{
				Tornado:     "OBSERVED",
				MaxHailSize: "1.75 IN",
			},
		},
		{
			name: "no closing delimiter still parses",
			description: "Prose.\n\n&&\n\n" +
				"TORNADO...POSSIBLE\n" +
				"SOURCE...TRAINED WEATHER SPOTTERS\n",
			want: WarningTags{
				Tornado: "POSSIBLE",
				Source:  "TRAINED WEATHER SPOTTERS",
			},
		},
		{
			name: "value with multiple dots preserved",
			description: "&&\n" +
				"MAX WIND GUST...80 MPH\n" +
				"EXPECTED RAINFALL RATE...2-3 INCHES IN 1 HOUR\n" +
				"&&",
			want: WarningTags{
				MaxWindGust:          "80 MPH",
				ExpectedRainfallRate: "2-3 INCHES IN 1 HOUR",
			},
		},
		{
			name: "spotter activation requested",
			description: "&&\n" +
				"TORNADO...OBSERVED\n" +
				"SPOTTER ACTIVATION...REQUESTED\n" +
				"&&",
			want: WarningTags{
				Tornado:           "OBSERVED",
				SpotterActivation: "REQUESTED",
			},
		},
		{
			name: "empty value after delimiter skipped",
			description: "&&\n" +
				"TORNADO...\n" +
				"MAX HAIL SIZE...1.00 IN\n" +
				"&&",
			want: WarningTags{
				MaxHailSize: "1.00 IN",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseWarningTags(tt.description)
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
				t.Errorf("tags mismatch:\n got:  %+v\n want: %+v", *got, tt.want)
			}
		})
	}
}

func TestWarningTagsIsEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		t    *WarningTags
		want bool
	}{
		{"nil is empty", nil, true},
		{"zero-value is empty", &WarningTags{}, true},
		{"one field populated is not empty", &WarningTags{Tornado: "OBSERVED"}, false},
		{"hail-only is not empty", &WarningTags{MaxHailSize: "1.00 IN"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.t.IsEmpty(); got != tt.want {
				t.Errorf("IsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}
