package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func topologyByID(nodes []TopologyNode) map[string]TopologyNode {
	byID := map[string]TopologyNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	return byID
}

// The home network: the gateway fo has a switch on lan2 with the APs lenti
// (lan1) and iot (eth0, no LLDP of its own) and a PC. The NAS is alone on fo
// lan1, the TV alone on lenti lan2, a laptop on the wifi of lenti.
func TestBuildTopology(t *testing.T) {
	app := newMigratedApp(t)
	now := time.Now()

	fo := newTestRecord(t, app, "devices", map[string]any{"name": "fo", "mac_address": "02:00:00:00:00:f0"})
	lenti := newTestRecord(t, app, "devices", map[string]any{"name": "lenti", "mac_address": "02:00:00:00:00:a1"})
	iot := newTestRecord(t, app, "devices", map[string]any{"name": "iot", "mac_address": "02:00:00:00:00:b1"})
	const (
		pc     = "02:00:00:00:01:01"
		nas    = "02:00:00:00:01:02"
		tv     = "02:00:00:00:01:03"
		laptop = "02:00:00:00:01:04"
	)
	for _, r := range []struct{ device, port, mac string }{
		{fo.Id, "lan2", "02:00:00:00:00:a1"},
		{fo.Id, "lan2", "02:00:00:00:00:b1"},
		{fo.Id, "lan2", pc},
		{lenti.Id, "lan1", "02:00:00:00:00:f0"},
		{lenti.Id, "lan1", "02:00:00:00:00:b1"},
	} {
		newTestRecord(t, app, "lldp", map[string]any{"device": r.device, "port": r.port, "neighbor_mac_address": r.mac})
	}
	require.NoError(t, syncBridgeFdb(app, fo, []FdbBridge{{Bridge: "br-lan", Entries: []FdbEntry{
		{MAC: "02:00:00:00:00:f0", Port: "lan1", Local: true},
		{MAC: "02:00:00:00:00:a1", Port: "lan2"},
		{MAC: "02:00:00:00:00:b1", Port: "lan2"},
	}}}))
	require.NoError(t, syncBridgeFdb(app, lenti, []FdbBridge{{Bridge: "br-lan", Entries: []FdbEntry{
		{MAC: "02:00:00:00:00:a1", Port: "lan1", Local: true},
		{MAC: "02:00:00:00:00:f0", Port: "lan1"},
	}}}))
	require.NoError(t, syncBridgeFdb(app, iot, []FdbBridge{{Bridge: "br-lan", Entries: []FdbEntry{
		{MAC: "02:00:00:00:00:b1", Port: "eth0.1", Local: true},
		{MAC: "02:00:00:00:00:f0", Port: "eth0.1"},
	}}}))
	newTestRecord(t, app, "lan_clients", map[string]any{"mac_address": pc, "device": fo.Id, "port": "lan2", "behind_switch": true})
	newTestRecord(t, app, "lan_clients", map[string]any{"mac_address": nas, "device": fo.Id, "port": "lan1"})
	newTestRecord(t, app, "lan_clients", map[string]any{"mac_address": tv, "device": lenti.Id, "port": "lan2"})
	newTestRecord(t, app, "clients", map[string]any{"mac_address": laptop, "device": lenti.Id, "band": "5"})

	rate := func(device string, rx float64, tx float64) TrafficRate {
		return TrafficRate{Device: device, RxBps: rx, TxBps: tx, At: now}
	}
	nodes, err := buildTopology(app, map[string]TrafficRate{
		"port|" + fo.Id + "|wan":     rate(fo.Id, 50e6, 5e6),
		"port|" + fo.Id + "|lan1":    rate(fo.Id, 1e6, 20e6),
		"port|" + fo.Id + "|lan2":    rate(fo.Id, 4e6, 30e6),
		"port|" + lenti.Id + "|lan1": rate(lenti.Id, 9e6, 2e6),
		"port|" + lenti.Id + "|lan2": rate(lenti.Id, 1e5, 3e6),
		"port|" + iot.Id + "|eth0":   rate(iot.Id, 7e6, 1e6),
		"nlbw|" + pc:                 rate(fo.Id, 12e6, 1e6),
		"nlbw|" + nas:                rate(fo.Id, 18e6, 0.5e6),
		"wifi|" + laptop:             rate(lenti.Id, 6e6, 1e6),
	}, now)
	require.NoError(t, err)
	byID := topologyByID(nodes)

	gw := byID["device:"+fo.Id]
	assert.Equal(t, "internet", gw.Parent)
	assert.Equal(t, 50e6, gw.DownBps, "the wan receives the downloads")
	assert.Equal(t, "wan", gw.Port)

	sw := byID["switch:"+fo.Id+"|lan2"]
	assert.Equal(t, "device:"+fo.Id, sw.Parent)
	assert.Equal(t, 30e6, sw.DownBps, "fo transmits to the switch")
	assert.Equal(t, 4e6, sw.UpBps)

	l := byID["device:"+lenti.Id]
	assert.Equal(t, sw.ID, l.Parent)
	assert.Equal(t, "lan1", l.Port)
	assert.Equal(t, 9e6, l.DownBps, "lenti receives from the switch")

	i := byID["device:"+iot.Id]
	assert.Equal(t, sw.ID, i.Parent, "iot sends no LLDP, the others see it")
	assert.Equal(t, "eth0.1", i.Port, "a swconfig vlan interface")
	assert.Equal(t, 7e6, i.DownBps, "counted on its physical interface")

	p := byID["host:"+pc]
	assert.Equal(t, sw.ID, p.Parent)
	assert.True(t, p.Approx)
	assert.Equal(t, 12e6, p.DownBps)

	n := byID["host:"+nas]
	assert.Equal(t, "device:"+fo.Id, n.Parent)
	assert.Equal(t, "lan1", n.Port)
	assert.False(t, n.Approx, "alone on the port: its traffic is known")
	assert.Equal(t, 20e6, n.DownBps)

	tvNode := byID["host:"+tv]
	assert.Equal(t, "device:"+lenti.Id, tvNode.Parent)
	assert.Equal(t, 3e6, tvNode.DownBps)

	lap := byID["host:"+laptop]
	assert.Equal(t, "device:"+lenti.Id, lap.Parent)
	assert.True(t, lap.Wifi)
	assert.Equal(t, "5 GHz", lap.Port)
	assert.Equal(t, 6e6, lap.DownBps)

	for _, n := range nodes {
		assert.NotContains(t, n.ID, "switch:"+lenti.Id, "the lenti end of the switch is no second switch")
	}
}

// Without LLDP, a router is linked to the gateway port it is learned on.
func TestBuildTopologyWithoutLldp(t *testing.T) {
	app := newMigratedApp(t)
	now := time.Now()
	fo := newTestRecord(t, app, "devices", map[string]any{"name": "fo", "mac_address": "02:00:00:00:00:f0"})
	ap := newTestRecord(t, app, "devices", map[string]any{"name": "ap", "mac_address": "02:00:00:00:00:a1"})
	require.NoError(t, syncBridgeFdb(app, fo, []FdbBridge{{Bridge: "br-lan", Entries: []FdbEntry{
		{MAC: "02:00:00:00:00:a1", Port: "lan3"},
	}}}))
	require.NoError(t, syncBridgeFdb(app, ap, []FdbBridge{{Bridge: "br-lan", Entries: []FdbEntry{
		{MAC: "02:00:00:00:00:f0", Port: "wan"},
	}}}))

	nodes, err := buildTopology(app, map[string]TrafficRate{
		"nlbw|02:00:00:00:01:01":  {Device: fo.Id, At: now},
		"port|" + fo.Id + "|lan3": {Device: fo.Id, RxBps: 1e6, TxBps: 8e6, At: now},
	}, now)
	require.NoError(t, err)
	byID := topologyByID(nodes)
	a := byID["device:"+ap.Id]
	assert.Equal(t, "device:"+fo.Id, a.Parent, "a direct link")
	assert.Equal(t, "lan3", a.Port)
	assert.Equal(t, 8e6, a.DownBps)
	assert.Equal(t, "device:"+fo.Id, byID["host:02:00:00:00:01:01"].Parent, "unknown hosts hang from the gateway")
}
