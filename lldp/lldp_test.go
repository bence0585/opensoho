package lldp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// One interface: lldpd renders a single-element list as an object, and wraps the
// chassis fields in the neighbour's SysName.
const oneInterface = `{"lldp":{"interface":{"eth0":{
	"via":"LLDP",
	"chassis":{"sw-core-01":{"id":{"type":"mac","value":"00:11:22:33:44:55"},"descr":"Core switch"}},
	"port":{"id":{"type":"ifname","value":"Gi0/1"},"descr":"GigabitEthernet0/1"}
}}}}`

// Two interfaces: lldpd renders a multi-element list as an array of single-key
// objects.
const twoInterfaces = `{"lldp":{"interface":[
	{"eth0":{"chassis":{"sw-core-01":{"id":{"type":"mac","value":"aa:bb:cc:dd:ee:ff"}}},"port":{"id":{"type":"ifname","value":"Gi0/1"}}}},
	{"eth1":{"chassis":{"ap-roof":{"id":{"type":"mac","value":"11:22:33:44:55:66"}}},"port":{"id":{"type":"ifname","value":"eth0"}}}}
]}}`

// A neighbour with no SysName: lldpd emits the chassis fields unwrapped, so no
// name is available.
const noSysName = `{"lldp":{"interface":{"eth2":{
	"chassis":{"id":{"type":"mac","value":"de:ad:be:ef:00:01"},"descr":"unknown"},
	"port":{"id":{"type":"ifname","value":"1"}}
}}}}`

func TestUnmarshalNormalized(t *testing.T) {
	tests := []struct {
		name string
		json string
		want []Neighbor
	}{
		{
			name: "one interface, object form",
			json: oneInterface,
			want: []Neighbor{{Port: "eth0", Name: "sw-core-01", Mac: "00:11:22:33:44:55"}},
		},
		{
			name: "two interfaces, array form",
			json: twoInterfaces,
			want: []Neighbor{
				{Port: "eth0", Name: "sw-core-01", Mac: "aa:bb:cc:dd:ee:ff"},
				{Port: "eth1", Name: "ap-roof", Mac: "11:22:33:44:55:66"},
			},
		},
		{
			name: "chassis without sysname",
			json: noSysName,
			want: []Neighbor{{Port: "eth2", Name: "", Mac: "de:ad:be:ef:00:01"}},
		},
		{
			name: "empty interface object",
			json: `{"lldp":{"interface":{}}}`,
			want: []Neighbor{},
		},
		{
			name: "absent interface key",
			json: `{"lldp":{}}`,
			want: []Neighbor{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var info Info
			assert.Nil(t, json.Unmarshal([]byte(tc.json), &info))
			assert.Equal(t, tc.want, info.Normalized())
		})
	}
}

// Normalized must dedup identical (port, name, mac) tuples and sort
// deterministically, with the MAC as the final tiebreak.
func TestNormalizedDedupAndSort(t *testing.T) {
	info := Info{Neighbors: []Neighbor{
		{Port: "eth1", Name: "b", Mac: "00:00:00:00:00:0b"},
		{Port: "eth0", Name: "z", Mac: "00:00:00:00:00:0z"},
		{Port: "eth0", Name: "a", Mac: "00:00:00:00:00:02"},
		{Port: "eth0", Name: "a", Mac: "00:00:00:00:00:01"},
		{Port: "eth0", Name: "a", Mac: "00:00:00:00:00:01"}, // duplicate
	}}
	assert.Equal(t, []Neighbor{
		{Port: "eth0", Name: "a", Mac: "00:00:00:00:00:01"},
		{Port: "eth0", Name: "a", Mac: "00:00:00:00:00:02"},
		{Port: "eth0", Name: "z", Mac: "00:00:00:00:00:0z"},
		{Port: "eth1", Name: "b", Mac: "00:00:00:00:00:0b"},
	}, info.Normalized())
}

// A pointer field stays nil when the key is absent (mirrors OpenSohoData.Lldp).
func TestPointerNilWhenAbsent(t *testing.T) {
	var wrapper struct {
		Lldp *Info `json:"lldp"`
	}
	assert.Nil(t, json.Unmarshal([]byte(`{"poe":{}}`), &wrapper))
	assert.Nil(t, wrapper.Lldp)
}

// Hosts like the LLDP agent of Windows network drivers identify their chassis
// by a local name instead of a MAC; the MAC is then the port id. A neighbour
// without any MAC can't be stored (the sync would fail on it) and is skipped.
func TestUnmarshalHostNeighbors(t *testing.T) {
	payload := `{"lldp":{"interface":[
		{"lan1":{"via":"LLDP","chassis":{"id":{"type":"local","value":"DESKTOP-PC"}},
		         "port":{"id":{"type":"mac","value":"02:00:00:00:00:01"},"ttl":"3601"}}},
		{"lan1":{"via":"LLDP","chassis":{"router-b":{"id":{"type":"mac","value":"02:00:00:00:00:02"}}},
		         "port":{"id":{"type":"mac","value":"02:00:00:00:00:03"}}}},
		{"lan2":{"via":"LLDP","chassis":{"id":{"type":"local","value":"NO-MAC"}},
		         "port":{"id":{"type":"ifname","value":"eth0"}}}}
	]}}`
	var info Info
	if err := json.Unmarshal([]byte(payload), &info); err != nil {
		t.Fatal(err)
	}
	got := info.Normalized()
	want := []Neighbor{
		{Port: "lan1", Name: "DESKTOP-PC", Mac: "02:00:00:00:00:01"},
		{Port: "lan1", Name: "router-b", Mac: "02:00:00:00:00:02"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("neighbour %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}
