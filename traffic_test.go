package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNlbwHosts(t *testing.T) {
	var data NlbwData
	payload := `{"columns":["mac","conns","rx_bytes","rx_pkts","tx_bytes","tx_pkts"],"data":[
		["10:7C:61:D3:A7:BD",226058,10273511700,33728746,130393470298,47202937],
		["not a mac",1,2,3,4,5],
		["00:d8:61:51:e4:d6",1386,"x",5255360,803101669,3072655],
		["00:23:24:7e:87:94",17153]
	]}`
	require.NoError(t, json.Unmarshal([]byte(payload), &data))
	assert.Equal(t, []NlbwHost{{MAC: "10:7c:61:d3:a7:bd", Rx: 10273511700, Tx: 130393470298}}, data.Hosts())

	assert.Nil(t, NlbwData{Columns: []string{"ip", "rx_bytes", "tx_bytes"}}.Hosts(), "grouped by ip, not mac")
}

func TestTrafficMeterRates(t *testing.T) {
	m := newTrafficMeter()
	t0 := time.Unix(1792800000, 0)

	m.Update("wifi|aa", "lenti", 1000, 500, t0)
	assert.Empty(t, m.Rates(t0), "one report is no rate")

	m.Update("wifi|aa", "lenti", 1000+1_250_000, 500+125_000, t0.Add(10*time.Second))
	rate := m.Rates(t0.Add(10 * time.Second))["wifi|aa"]
	assert.Equal(t, 1_000_000.0, rate.RxBps)
	assert.Equal(t, 100_000.0, rate.TxBps)
	assert.Equal(t, "lenti", rate.Device)

	// roamed to another AP, which counts from zero
	m.Update("wifi|aa", "iot", 10, 10, t0.Add(20*time.Second))
	assert.Equal(t, 1_000_000.0, m.Rates(t0.Add(20 * time.Second))["wifi|aa"].RxBps, "no rate from the roam")
	m.Update("wifi|aa", "iot", 10+250_000, 10, t0.Add(30*time.Second))
	assert.Equal(t, 200_000.0, m.Rates(t0.Add(30 * time.Second))["wifi|aa"].RxBps)

	// a reboot resets the counters
	m.Update("wifi|aa", "iot", 5, 5, t0.Add(40*time.Second))
	assert.Equal(t, 200_000.0, m.Rates(t0.Add(40 * time.Second))["wifi|aa"].RxBps)

	// after a long gap there is no rate either
	m.Update("wifi|aa", "iot", 5+1000, 5, t0.Add(40*time.Second+trafficMaxInterval+time.Second))
	assert.Empty(t, m.Rates(t0.Add(40*time.Second+trafficMaxInterval+time.Second)), "the last rate is outdated")

	// the minutes hold the traffic of the rates
	buckets := m.takeBuckets(t0.Add(time.Hour))
	total := uint64(0)
	for _, bucket := range buckets {
		total += bucket["wifi|aa"].rx
	}
	assert.Equal(t, uint64(1_250_000+250_000), total)
	assert.Empty(t, m.takeBuckets(t0.Add(time.Hour)), "taken")
}

func TestTrafficHistory(t *testing.T) {
	app := newMigratedApp(t)
	m := newTrafficMeter()
	// 12:00:00 UTC, the start of an hour
	t0 := time.Unix(1792800000/3600*3600, 0)

	// 1 Mbit/s down, 0.5 Mbit/s up for 3 minutes, in reports every 15s
	rx, tx := uint64(0), uint64(0)
	for i := 0; i <= 12; i++ {
		m.Update("port|fo|lan1", "fo", rx, tx, t0.Add(time.Duration(i)*15*time.Second))
		rx += 15 * 1_000_000 / 8
		tx += 15 * 500_000 / 8
	}
	// the current minute (12:03) stays in memory
	require.NoError(t, flushTraffic(app, m, t0.Add(3*time.Minute+30*time.Second)))

	history, err := trafficHistory(app, []string{"port|fo|lan1", "port|fo|lan9"}, "1h", t0.Add(4*time.Minute))
	require.NoError(t, err)
	require.Len(t, history["port|fo|lan1"], 3)
	for i, p := range history["port|fo|lan1"] {
		assert.Equal(t, t0.Unix()+int64(i)*60, p.Time)
		assert.InDelta(t, 1_000_000, p.RxBps, 1)
		assert.InDelta(t, 500_000, p.TxBps, 1)
	}
	assert.Equal(t, []TrafficPoint{}, history["port|fo|lan9"])

	// before the rollup, the current hour is summed from the minutes
	history, err = trafficHistory(app, []string{"port|fo|lan1"}, "7d", t0.Add(4*time.Minute))
	require.NoError(t, err)
	require.Len(t, history["port|fo|lan1"], 1)
	assert.Equal(t, t0.Unix(), history["port|fo|lan1"][0].Time)
	assert.InDelta(t, 1_000_000, history["port|fo|lan1"][0].RxBps, 1)

	// rolled up, the hour comes from traffic_hour, and twice is the same
	require.NoError(t, rollupTraffic(app, t0.Add(time.Hour+5*time.Minute)))
	require.NoError(t, rollupTraffic(app, t0.Add(time.Hour+5*time.Minute)))
	history, err = trafficHistory(app, []string{"port|fo|lan1"}, "30d", t0.Add(time.Hour+5*time.Minute))
	require.NoError(t, err)
	require.Len(t, history["port|fo|lan1"], 1)
	assert.InDelta(t, 500_000, history["port|fo|lan1"][0].TxBps, 1)

	_, err = trafficHistory(app, []string{"port|fo|lan1"}, "1y", t0)
	assert.Error(t, err)

	// past the retention, the minutes and then the hours are removed
	count := func(table string) int {
		var n int
		require.NoError(t, app.DB().NewQuery("SELECT count(*) FROM "+table).Row(&n))
		return n
	}
	assert.Equal(t, 3, count("traffic_minute"))
	require.NoError(t, rollupTraffic(app, t0.Add(trafficMinuteRetention+time.Hour)))
	assert.Equal(t, 0, count("traffic_minute"))
	assert.Equal(t, 1, count("traffic_hour"))
	require.NoError(t, rollupTraffic(app, t0.Add(trafficHourRetention+2*time.Hour)))
	assert.Equal(t, 0, count("traffic_hour"))
}

func TestLiveTrafficOverview(t *testing.T) {
	app := newMigratedApp(t)
	now := time.Now()

	fo := newTestRecord(t, app, "devices", map[string]any{"name": "FoRouter", "mac_address": "80:AF:CA:BE:90:EC"})
	lenti := newTestRecord(t, app, "devices", map[string]any{"name": "LentiRouter", "mac_address": "88:C3:97:EE:A6:A1"})
	newTestRecord(t, app, "ethernet", map[string]any{"device": fo.Id, "name": "wan", "speed": "1000F"})
	newTestRecord(t, app, "dhcp_reservations", map[string]any{"name": "homeassistant", "mac_address": "00:23:24:7E:87:94", "ip_address": "192.168.1.10"})
	newTestRecord(t, app, "dhcp_leases", map[string]any{"mac_address": "3c:a9:f4:6b:f0:24", "ip_address": "192.168.1.170", "hostname": "gb-dell"})
	newTestRecord(t, app, "lan_clients", map[string]any{"mac_address": "00:23:24:7e:87:94", "device": fo.Id, "port": "lan1"})
	newTestRecord(t, app, "lan_clients", map[string]any{"mac_address": "00:d8:61:51:e4:d6", "device": fo.Id, "port": "lan2", "behind_switch": true, "alias": "Gergő gépe"})
	newTestRecord(t, app, "clients", map[string]any{"mac_address": "3c:a9:f4:6b:f0:24", "device": lenti.Id, "band": "5"})

	rate := func(device string, rx float64, tx float64) TrafficRate {
		return TrafficRate{Device: device, RxBps: rx, TxBps: tx, At: now}
	}
	overview, err := liveTrafficOverview(app, map[string]TrafficRate{
		"port|" + fo.Id + "|wan":    rate(fo.Id, 50e6, 5e6),
		"port|" + fo.Id + "|lan1":   rate(fo.Id, 2e6, 30e6),
		"port|" + lenti.Id + "|wan": rate(lenti.Id, 1e6, 1e6),
		"nlbw|00:23:24:7e:87:94":    rate(fo.Id, 20e6, 1e6),
		"nlbw|00:d8:61:51:e4:d6":    rate(fo.Id, 25e6, 2e6),
		"nlbw|3c:a9:f4:6b:f0:24":    rate(fo.Id, 4e6, 1e6),
		"nlbw|88:c3:97:ee:a6:a1":    rate(fo.Id, 1e3, 1e3),
		"wifi|3c:a9:f4:6b:f0:24":    rate(lenti.Id, 8e6, 1e6),
	}, now)
	require.NoError(t, err)

	require.Len(t, overview.Wan, 1, "only the wan of the gateway")
	assert.Equal(t, "FoRouter", overview.Wan[0].Device)
	assert.Equal(t, "1000F", overview.Wan[0].Speed)
	assert.Len(t, overview.Ports, 3)

	require.Len(t, overview.Clients, 4)
	ha := overview.Clients[0]
	assert.Equal(t, "homeassistant", ha.Name)
	assert.Equal(t, "192.168.1.10", ha.IP)
	assert.True(t, ha.Wired)
	assert.Equal(t, "FoRouter", ha.Device)
	assert.Equal(t, "lan1", ha.Where)
	assert.Equal(t, "port|"+fo.Id+"|lan1", ha.Key, "alone on its port: the port is its total traffic")
	assert.Equal(t, 30e6, ha.RxBps)
	assert.Equal(t, 2e6, ha.TxBps)
	assert.Equal(t, 20e6, ha.InternetRxBps)

	pc := overview.Clients[1]
	assert.Equal(t, "Gergő gépe", pc.Name, "the alias wins")
	assert.Equal(t, "lan2 (switch)", pc.Where)
	assert.Equal(t, "", pc.Key, "behind a switch only the internet traffic is known")

	laptop := overview.Clients[2]
	assert.Equal(t, "gb-dell", laptop.Name)
	assert.False(t, laptop.Wired)
	assert.Equal(t, "LentiRouter", laptop.Device)
	assert.Equal(t, "5 GHz", laptop.Where)
	assert.Equal(t, 8e6, laptop.RxBps)
	assert.Equal(t, 4e6, laptop.InternetRxBps)

	router := overview.Clients[3]
	assert.True(t, router.Router)
	assert.Equal(t, "LentiRouter", router.Name)
}

func TestRecordMonitoringTraffic(t *testing.T) {
	app := newMigratedApp(t)
	lenti := newTestRecord(t, app, "devices", map[string]any{"name": "lenti"})
	m := newTrafficMeter()
	t0 := time.Unix(1792800000, 0)

	report := func(clientRx, clientTx, portRx, portTx uint64) MonitoringData {
		var payload MonitoringData
		require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{"type":"DeviceMonitoring","interfaces":[
			{"name":"phy1-ap0","type":"wireless","wireless":{"clients":[
				{"mac":"3C:A9:F4:6B:F0:24","assoc":true,"bytes":{"rx":%d,"tx":%d}},
				{"mac":"92:47:b2:61:fc:f0","assoc":false,"bytes":{"rx":0,"tx":0}}]}},
			{"name":"lan2","type":"ethernet","statistics":{"rx_bytes":%d,"tx_bytes":%d}},
			{"name":"br-lan","type":"bridge","bridge_members":["lan2","phy1-ap0"],"statistics":{"rx_bytes":0,"tx_bytes":0}}
		]}`, clientRx, clientTx, portRx, portTx)), &payload))
		return payload
	}
	recordMonitoringTraffic(m, lenti, report(0, 0, 0, 0), t0)
	recordMonitoringTraffic(m, lenti, report(10_000, 1_000_000, 300, 3_000), t0.Add(10*time.Second))

	rates := m.Rates(t0.Add(10 * time.Second))
	assert.Len(t, rates, 2, "the associated client and the wired port, not the bridge")
	client := rates["wifi|3c:a9:f4:6b:f0:24"]
	assert.Equal(t, 800_000.0, client.RxBps, "what the AP transmits is the client's download")
	assert.Equal(t, 8_000.0, client.TxBps)
	port := rates["port|"+lenti.Id+"|lan2"]
	assert.Equal(t, 240.0, port.RxBps)
	assert.Equal(t, 2_400.0, port.TxBps)

	var nlbw NlbwData
	require.NoError(t, json.Unmarshal([]byte(`{"columns":["mac","rx_bytes","tx_bytes"],"data":[["00:23:24:7e:87:94",1000,100]]}`), &nlbw))
	recordNlbwTraffic(m, lenti, nlbw, t0)
	nlbw.Data[0][1] = float64(1000 + 125_000)
	recordNlbwTraffic(m, lenti, nlbw, t0.Add(time.Second))
	assert.Equal(t, 1_000_000.0, m.Rates(t0.Add(time.Second))["nlbw|00:23:24:7e:87:94"].RxBps)
}

// nlbwmon updates its counters every 30s, the routers report every 15s.
func TestTrafficMeterSmoothsSteppedCounters(t *testing.T) {
	m := newTrafficMeter()
	t0 := time.Unix(1792800000, 0)
	const bps = 8_000_000 // 1 MB/s

	for i := 0; i <= 12; i++ {
		at := t0.Add(time.Duration(i) * 15 * time.Second)
		stepped := uint64(i/2) * 30 * bps / 8
		m.Update("nlbw|aa", "fo", stepped, 0, at)
		if i >= 4 {
			assert.Equal(t, float64(bps), m.Rates(at)["nlbw|aa"].RxBps, "report %d", i)
		}
	}

	// the minutes still hold exactly what was counted
	total := uint64(0)
	for _, bucket := range m.takeBuckets(t0.Add(time.Hour)) {
		total += bucket["nlbw|aa"].rx
	}
	assert.Equal(t, uint64(6*30*bps/8), total)
}
