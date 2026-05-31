package store

import (
	"reflect"
	"sort"
	"testing"

	"github.com/eclecti-build/seestorm-ingest/internal/nws"
)

func ref(ids ...string) []nws.AlertReference {
	out := make([]nws.AlertReference, 0, len(ids))
	for _, id := range ids {
		out = append(out, nws.AlertReference{Identifier: id})
	}
	return out
}

func alert(event string, refs []nws.AlertReference) nws.Alert {
	return nws.Alert{Properties: nws.AlertProperties{Event: event, References: refs}}
}

func TestReferencesByEventType(t *testing.T) {
	tests := []struct {
		name   string
		alerts []nws.Alert
		want   map[string][]string
	}{
		{
			name:   "no references yields empty map",
			alerts: []nws.Alert{alert("Tornado Warning", nil)},
			want:   map[string][]string{},
		},
		{
			name:   "groups identifiers under the superseding event_type",
			alerts: []nws.Alert{alert("Tornado Warning", ref("a", "b"))},
			want:   map[string][]string{"Tornado Warning": {"a", "b"}},
		},
		{
			name: "different event_types stay in separate groups",
			alerts: []nws.Alert{
				alert("Tornado Warning", ref("a")),
				alert("Flood Warning", ref("c")),
			},
			want: map[string][]string{"Tornado Warning": {"a"}, "Flood Warning": {"c"}},
		},
		{
			name:   "empty identifiers are skipped",
			alerts: []nws.Alert{alert("Flood Warning", ref("", "c", ""))},
			want:   map[string][]string{"Flood Warning": {"c"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := referencesByEventType(tt.alerts)
			for k := range got {
				sort.Strings(got[k])
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
