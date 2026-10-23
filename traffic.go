package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// Traffic metering
//
// The routers report byte counters: the per-host counters of nlbwmon on the
// gateway (traffic to and from the internet), the counters of the wifi
// stations on the APs (all traffic of a wifi client) and the counters of the
// wired ports. The difference between two reports is the traffic in between.
//
// The current rates are kept in memory. The traffic per minute is kept for 7
// days, per hour for 30 days, in two plain tables (traffic_minute and
// traffic_hour, see the 1792800001 migration): with ~100 series that's ~10^6
// rows, too many for regular collections.
//
// A series is identified by a key:
//
//	nlbw|<mac>             internet traffic of a host (the gateway's nlbwmon)
//	wifi|<mac>             all traffic of a wifi client (its AP)
//	port|<device>|<port>   a wired port of a device
//
// rx/tx are always seen from the host for nlbw and wifi (rx = download), and
// from the router for ports (rx = received on the port).

const (
	trafficMinuteRetention = 7 * 24 * time.Hour
	trafficHourRetention   = 30 * 24 * time.Hour
	// Two reports further apart than this aren't turned into a rate: the
	// device was offline, the counters may have wrapped or reset.
	trafficMaxInterval = 5 * time.Minute
	// Rates older than this are no longer current.
	trafficRateTimeout = 90 * time.Second
	// The current rate is the average over this window. nlbwmon updates its
	// counters only every 30s, a rate between two 15s reports would jump
	// between zero and double.
	trafficRateWindow = 60 * time.Second
)

// NlbwData is the output of `nlbw -c json -g mac`.
type NlbwData struct {
	Columns []string `json:"columns"`
	Data    [][]any  `json:"data"`
}

// NlbwHost is the cumulated traffic of one host.
type NlbwHost struct {
	MAC string
	Rx  uint64
	Tx  uint64
}

// Hosts returns the per host counters.
func (n NlbwData) Hosts() []NlbwHost {
	index := map[string]int{}
	for i, column := range n.Columns {
		index[column] = i
	}
	macCol, okMac := index["mac"]
	rxCol, okRx := index["rx_bytes"]
	txCol, okTx := index["tx_bytes"]
	if !okMac || !okRx || !okTx {
		return nil
	}
	hosts := []NlbwHost{}
	for _, row := range n.Data {
		if len(row) <= max(macCol, rxCol, txCol) {
			continue
		}
		mac, _ := row[macCol].(string)
		rx, okRx := row[rxCol].(float64)
		tx, okTx := row[txCol].(float64)
		mac = normalizeMAC(mac)
		if mac == "" || !okRx || !okTx || rx < 0 || tx < 0 {
			continue
		}
		hosts = append(hosts, NlbwHost{MAC: mac, Rx: uint64(rx), Tx: uint64(tx)})
	}
	return hosts
}

type trafficCounter struct {
	device string
	rx     uint64
	tx     uint64
	at     time.Time
}

// trafficSeries is what is known about one series: the reports of the rate
// window, oldest first.
type trafficSeries struct {
	window []trafficCounter
}

// TrafficRate is the current traffic of a series, in bits per second.
type TrafficRate struct {
	Device string
	RxBps  float64
	TxBps  float64
	At     time.Time
}

type trafficBucket struct {
	rx      uint64
	tx      uint64
	seconds float64
}

type trafficMeter struct {
	mu     sync.Mutex
	series map[string]*trafficSeries
	rates  map[string]TrafficRate
	// minute (unix seconds) -> key -> traffic, not yet written
	buckets map[int64]map[string]*trafficBucket
}

func newTrafficMeter() *trafficMeter {
	return &trafficMeter{
		series:  map[string]*trafficSeries{},
		rates:   map[string]TrafficRate{},
		buckets: map[int64]map[string]*trafficBucket{},
	}
}

var traffic = newTrafficMeter()

// Update records the counters of a series reported by a device.
func (m *trafficMeter) Update(key string, device string, rx uint64, tx uint64, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	current := trafficCounter{device: device, rx: rx, tx: tx, at: at}
	series := m.series[key]
	if series == nil {
		series = &trafficSeries{}
		m.series[key] = series
	}
	var prev trafficCounter
	ok := len(series.window) > 0
	if ok {
		prev = series.window[len(series.window)-1]
	}
	interval := at.Sub(prev.at)
	// A wifi client that roamed is counted from zero by its new AP, and
	// counters go back on a reboot. After a long gap they may have wrapped.
	if !ok || prev.device != device || rx < prev.rx || tx < prev.tx || interval <= 0 || interval > trafficMaxInterval {
		series.window = []trafficCounter{current}
		return
	}

	// the oldest report still in the window is where the rate starts
	series.window = append(series.window, current)
	for len(series.window) > 2 && at.Sub(series.window[1].at) >= trafficRateWindow {
		series.window = series.window[1:]
	}
	first := series.window[0]
	windowSeconds := at.Sub(first.at).Seconds()
	m.rates[key] = TrafficRate{
		Device: device,
		RxBps:  float64(rx-first.rx) * 8 / windowSeconds,
		TxBps:  float64(tx-first.tx) * 8 / windowSeconds,
		At:     at,
	}

	drx, dtx := rx-prev.rx, tx-prev.tx
	seconds := interval.Seconds()

	minute := at.Unix() / 60 * 60
	bucket := m.buckets[minute]
	if bucket == nil {
		bucket = map[string]*trafficBucket{}
		m.buckets[minute] = bucket
	}
	b := bucket[key]
	if b == nil {
		b = &trafficBucket{}
		bucket[key] = b
	}
	b.rx += drx
	b.tx += dtx
	b.seconds += seconds
}

// Rates returns the current rates.
func (m *trafficMeter) Rates(now time.Time) map[string]TrafficRate {
	m.mu.Lock()
	defer m.mu.Unlock()
	rates := map[string]TrafficRate{}
	for key, rate := range m.rates {
		if now.Sub(rate.At) <= trafficRateTimeout {
			rates[key] = rate
		}
	}
	return rates
}

// takeBuckets removes and returns the minutes before the one of now.
func (m *trafficMeter) takeBuckets(now time.Time) map[int64]map[string]*trafficBucket {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := now.Unix() / 60 * 60
	done := map[int64]map[string]*trafficBucket{}
	for minute, bucket := range m.buckets {
		if minute < current {
			done[minute] = bucket
			delete(m.buckets, minute)
		}
	}
	return done
}

func nlbwTrafficKey(mac string) string { return "nlbw|" + mac }
func wifiTrafficKey(mac string) string { return "wifi|" + mac }
func portTrafficKey(device string, port string) string {
	return "port|" + device + "|" + port
}

// recordMonitoringTraffic meters the wifi clients and wired ports of a
// DeviceMonitoring report.
func recordMonitoringTraffic(meter *trafficMeter, device *core.Record, payload MonitoringData, at time.Time) {
	bridgeMembers := bridgeMemberNames(payload.Interfaces)
	for _, iface := range payload.Interfaces {
		if iface.Type == "wireless" && iface.Wireless != nil {
			for _, client := range iface.Wireless.Clients {
				mac := normalizeMAC(client.MAC)
				if client.Assoc && mac != "" {
					// the AP transmits what the client downloads
					meter.Update(wifiTrafficKey(mac), device.Id, client.Bytes.Tx, client.Bytes.Rx, at)
				}
			}
		}
		if iface.Statistics != nil && isWiredPort(iface, bridgeMembers) {
			meter.Update(portTrafficKey(device.Id, iface.Name), device.Id, iface.Statistics.RxBytes, iface.Statistics.TxBytes, at)
		}
	}
}

// recordNlbwTraffic meters the hosts of the nlbwmon report of a gateway.
func recordNlbwTraffic(meter *trafficMeter, device *core.Record, data NlbwData, at time.Time) {
	for _, host := range data.Hosts() {
		meter.Update(nlbwTrafficKey(host.MAC), device.Id, host.Rx, host.Tx, at)
	}
}

// trafficKeyIDs maps the series keys to the ids used in the traffic tables.
type trafficKeyIDs struct {
	mu  sync.Mutex
	ids map[string]int64
}

var trafficKeys = &trafficKeyIDs{ids: map[string]int64{}}

func (k *trafficKeyIDs) id(app core.App, key string) (int64, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if id, ok := k.ids[key]; ok {
		return id, nil
	}
	var id int64
	err := app.DB().NewQuery("SELECT id FROM traffic_keys WHERE key = {:key}").Bind(dbx.Params{"key": key}).Row(&id)
	if err != nil {
		if _, err := app.DB().NewQuery("INSERT INTO traffic_keys (key) VALUES ({:key})").Bind(dbx.Params{"key": key}).Execute(); err != nil {
			return 0, err
		}
		if err := app.DB().NewQuery("SELECT id FROM traffic_keys WHERE key = {:key}").Bind(dbx.Params{"key": key}).Row(&id); err != nil {
			return 0, err
		}
	}
	k.ids[key] = id
	return id, nil
}

// flushTraffic writes the completed minutes to traffic_minute.
func flushTraffic(app core.App, meter *trafficMeter, now time.Time) error {
	done := meter.takeBuckets(now)
	if len(done) == 0 {
		return nil
	}
	return app.RunInTransaction(func(txApp core.App) error {
		for minute, bucket := range done {
			for key, b := range bucket {
				id, err := trafficKeys.id(txApp, key)
				if err != nil {
					return err
				}
				_, err = txApp.DB().NewQuery(`
					INSERT INTO traffic_minute (key_id, ts, rx, tx, secs) VALUES ({:id}, {:ts}, {:rx}, {:tx}, {:secs})
					ON CONFLICT (key_id, ts) DO UPDATE SET rx = rx + excluded.rx, tx = tx + excluded.tx, secs = secs + excluded.secs
				`).Bind(dbx.Params{"id": id, "ts": minute, "rx": b.rx, "tx": b.tx, "secs": b.seconds}).Execute()
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// rollupTraffic sums the minutes of the last hours into traffic_hour and
// removes the data past its retention. Rolling up an hour again (it runs
// every hour, for the last two) just recomputes it.
func rollupTraffic(app core.App, now time.Time) error {
	hour := now.Unix() / 3600 * 3600
	from := hour - 2*3600
	return app.RunInTransaction(func(txApp core.App) error {
		_, err := txApp.DB().NewQuery(`
			INSERT OR REPLACE INTO traffic_hour (key_id, ts, rx, tx, secs)
			SELECT key_id, ts / 3600 * 3600, sum(rx), sum(tx), sum(secs) FROM traffic_minute
			WHERE ts >= {:from} AND ts < {:to} GROUP BY key_id, ts / 3600
		`).Bind(dbx.Params{"from": from, "to": hour}).Execute()
		if err != nil {
			return err
		}
		_, err = txApp.DB().NewQuery("DELETE FROM traffic_minute WHERE ts < {:ts}").
			Bind(dbx.Params{"ts": now.Add(-trafficMinuteRetention).Unix()}).Execute()
		if err != nil {
			return err
		}
		_, err = txApp.DB().NewQuery("DELETE FROM traffic_hour WHERE ts < {:ts}").
			Bind(dbx.Params{"ts": now.Add(-trafficHourRetention).Unix()}).Execute()
		return err
	})
}

// TrafficPoint is the average traffic over one minute or hour.
type TrafficPoint struct {
	Time  int64   `json:"t"`
	RxBps float64 `json:"rx"`
	TxBps float64 `json:"tx"`
}

var trafficRanges = map[string]struct {
	duration time.Duration
	table    string
	step     int64
}{
	"1h":  {time.Hour, "traffic_minute", 60},
	"24h": {24 * time.Hour, "traffic_minute", 60},
	"7d":  {7 * 24 * time.Hour, "traffic_hour", 3600},
	"30d": {30 * 24 * time.Hour, "traffic_hour", 3600},
}

// trafficHistory returns the traffic of the series in the range, per minute
// (1h, 24h) or hour (7d, 30d). Minutes and hours without traffic data are
// left out, the current hour comes from the minutes.
func trafficHistory(app core.App, keys []string, rangeName string, now time.Time) (map[string][]TrafficPoint, error) {
	r, ok := trafficRanges[rangeName]
	if !ok {
		return nil, errors.New("unknown range")
	}
	from := now.Add(-r.duration).Unix() / r.step * r.step
	history := map[string][]TrafficPoint{}

	for _, key := range keys {
		points := []TrafficPoint{}
		var rows []struct {
			Ts   int64   `db:"ts"`
			Rx   float64 `db:"rx"`
			Tx   float64 `db:"tx"`
			Secs float64 `db:"secs"`
		}
		query := fmt.Sprintf(`
			SELECT t.ts AS ts, t.rx AS rx, t.tx AS tx, t.secs AS secs FROM %s t
			JOIN traffic_keys k ON k.id = t.key_id
			WHERE k.key = {:key} AND t.ts >= {:from} ORDER BY t.ts`, r.table)
		if r.table == "traffic_hour" {
			// the current hour isn't rolled up yet
			query = `
				SELECT ts, rx, tx, secs FROM (
					SELECT t.ts AS ts, t.rx AS rx, t.tx AS tx, t.secs AS secs FROM traffic_hour t
					JOIN traffic_keys k ON k.id = t.key_id
					WHERE k.key = {:key} AND t.ts >= {:from} AND t.ts < {:hour}
					UNION ALL
					SELECT m.ts / 3600 * 3600, sum(m.rx), sum(m.tx), sum(m.secs) FROM traffic_minute m
					JOIN traffic_keys k ON k.id = m.key_id
					WHERE k.key = {:key} AND m.ts >= {:hour} GROUP BY m.ts / 3600
				) ORDER BY ts`
		}
		err := app.DB().NewQuery(query).Bind(dbx.Params{
			"key":  key,
			"from": from,
			"hour": now.Unix() / 3600 * 3600,
		}).All(&rows)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Secs <= 0 {
				continue
			}
			points = append(points, TrafficPoint{Time: row.Ts, RxBps: row.Rx * 8 / row.Secs, TxBps: row.Tx * 8 / row.Secs})
		}
		history[key] = points
	}
	return history, nil
}

// TrafficClient is a host in the live traffic overview.
type TrafficClient struct {
	MAC    string `json:"mac"`
	Name   string `json:"name"`
	IP     string `json:"ip"`
	Vendor string `json:"vendor"`
	// where the host is: the AP and band, or the device and port
	Device string `json:"device"`
	Where  string `json:"where"`
	Wired  bool   `json:"wired"`
	Router bool   `json:"router"`
	// The total traffic of the host: of its wifi station, or of its port
	// when it is the only host on it. Otherwise only the internet traffic
	// is known.
	Key   string  `json:"key"`
	RxBps float64 `json:"rx"`
	TxBps float64 `json:"tx"`
	// the internet traffic of the host, from the gateway
	InternetKey   string  `json:"internetKey"`
	InternetRxBps float64 `json:"internetRx"`
	InternetTxBps float64 `json:"internetTx"`
}

// TrafficPort is a wired port in the live traffic overview.
type TrafficPort struct {
	Key    string  `json:"key"`
	Device string  `json:"device"`
	Port   string  `json:"port"`
	Speed  string  `json:"speed"`
	RxBps  float64 `json:"rx"`
	TxBps  float64 `json:"tx"`
	Wan    bool    `json:"wan"`
}

// TrafficOverview is the response of /api/v1/traffic/live.
type TrafficOverview struct {
	Time    int64           `json:"time"`
	Wan     []TrafficPort   `json:"wan"`
	Clients []TrafficClient `json:"clients"`
	Ports   []TrafficPort   `json:"ports"`
}

// liveTrafficOverview combines the current rates with what is known about
// the hosts and ports.
func liveTrafficOverview(app core.App, rates map[string]TrafficRate, now time.Time) (TrafficOverview, error) {
	overview := TrafficOverview{Time: now.Unix(), Wan: []TrafficPort{}, Clients: []TrafficClient{}, Ports: []TrafficPort{}}

	devices, err := app.FindAllRecords("devices")
	if err != nil {
		return overview, err
	}
	deviceNames := map[string]string{}
	routerMACs := map[string]string{}
	for _, device := range devices {
		deviceNames[device.Id] = device.GetString("name")
		if mac := normalizeMAC(device.GetString("mac_address")); mac != "" {
			routerMACs[mac] = device.GetString("name")
		}
	}
	if rows, err := app.FindAllRecords("bridge_fdb", dbx.HashExp{"local": true}); err == nil {
		for _, row := range rows {
			routerMACs[row.GetString("mac_address")] = deviceNames[row.GetString("device")]
		}
	}

	speeds := map[string]string{}
	if rows, err := app.FindAllRecords("ethernet"); err == nil {
		for _, row := range rows {
			speeds[portTrafficKey(row.GetString("device"), row.GetString("name"))] = row.GetString("speed")
		}
	}

	// The gateways are the devices that report nlbwmon data.
	gateways := map[string]bool{}
	for key, rate := range rates {
		if strings.HasPrefix(key, "nlbw|") {
			gateways[rate.Device] = true
		}
	}

	hosts := map[string]*TrafficClient{}
	host := func(mac string) *TrafficClient {
		if h, ok := hosts[mac]; ok {
			return h
		}
		h := &TrafficClient{MAC: mac}
		hosts[mac] = h
		return h
	}

	for key, rate := range rates {
		parts := strings.Split(key, "|")
		switch parts[0] {
		case "nlbw":
			h := host(parts[1])
			h.InternetKey = key
			h.InternetRxBps = rate.RxBps
			h.InternetTxBps = rate.TxBps
		case "wifi":
			h := host(parts[1])
			h.Key = key
			h.RxBps = rate.RxBps
			h.TxBps = rate.TxBps
		case "port":
			if len(parts) != 3 {
				continue
			}
			port := TrafficPort{
				Key:    key,
				Device: deviceNames[parts[1]],
				Port:   parts[2],
				Speed:  speeds[key],
				RxBps:  rate.RxBps,
				TxBps:  rate.TxBps,
				Wan:    gateways[parts[1]] && parts[2] == "wan",
			}
			if port.Wan {
				overview.Wan = append(overview.Wan, port)
			}
			overview.Ports = append(overview.Ports, port)
		}
	}

	// Where the hosts are, and the ports with a single wired host.
	lanClients, _ := app.FindAllRecords("lan_clients")
	hostsPerPort := map[string]int{}
	for _, c := range lanClients {
		if !c.GetBool("behind_switch") {
			hostsPerPort[portTrafficKey(c.GetString("device"), c.GetString("port"))]++
		}
	}
	for _, c := range lanClients {
		portKey := portTrafficKey(c.GetString("device"), c.GetString("port"))
		_, portRated := rates[portKey]
		h, ok := hosts[c.GetString("mac_address")]
		if !ok {
			// a host without internet traffic is still known by its own port
			if !portRated || c.GetBool("behind_switch") || hostsPerPort[portKey] != 1 {
				continue
			}
			h = host(c.GetString("mac_address"))
		}
		h.Wired = true
		h.Device = deviceNames[c.GetString("device")]
		h.Where = c.GetString("port")
		if c.GetBool("behind_switch") {
			h.Where += " (switch)"
		}
		h.Name = c.GetString("alias")
		if rate, ok := rates[portKey]; ok && h.Key == "" && !c.GetBool("behind_switch") && hostsPerPort[portKey] == 1 {
			// the router transmits what the host downloads
			h.Key = portKey
			h.RxBps = rate.TxBps
			h.TxBps = rate.RxBps
		}
	}
	if clients, err := app.FindAllRecords("clients"); err == nil {
		for _, c := range clients {
			h, ok := hosts[normalizeMAC(c.GetString("mac_address"))]
			if !ok || h.Wired {
				continue
			}
			h.Device = deviceNames[c.GetString("device")]
			if band := c.GetString("band"); band != "" {
				h.Where = band + " GHz"
			}
			if h.Name == "" {
				h.Name = c.GetString("alias")
			}
		}
	}

	for mac, h := range hosts {
		if name, ok := routerMACs[mac]; ok {
			h.Router = true
			if h.Name == "" {
				h.Name = name
			}
		}
		ip, hostname := lookupHostInfo(app, mac)
		h.IP = ip
		if h.Name == "" {
			h.Name = hostname
		}
		h.Vendor = lookupVendor(mac)
		overview.Clients = append(overview.Clients, *h)
	}

	sort.Slice(overview.Clients, func(i, j int) bool {
		a, b := overview.Clients[i], overview.Clients[j]
		ta := max(a.RxBps+a.TxBps, a.InternetRxBps+a.InternetTxBps)
		tb := max(b.RxBps+b.TxBps, b.InternetRxBps+b.InternetTxBps)
		if ta != tb {
			return ta > tb
		}
		return a.MAC < b.MAC
	})
	sort.Slice(overview.Ports, func(i, j int) bool {
		a, b := overview.Ports[i], overview.Ports[j]
		if a.Device != b.Device {
			return a.Device < b.Device
		}
		return a.Port < b.Port
	})
	return overview, nil
}

func apiTrafficLive(e *core.RequestEvent) error {
	now := time.Now()
	overview, err := liveTrafficOverview(e.App, traffic.Rates(now), now)
	if err != nil {
		return e.InternalServerError("Failed to build the traffic overview", err)
	}
	return e.JSON(200, overview)
}

func apiTrafficHistory(e *core.RequestEvent) error {
	keys := []string{}
	for _, key := range strings.Split(e.Request.URL.Query().Get("keys"), ",") {
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 || len(keys) > 100 {
		return e.BadRequestError("Pass 1 to 100 comma separated keys", nil)
	}
	rangeName := e.Request.URL.Query().Get("range")
	if rangeName == "" {
		rangeName = "1h"
	}
	history, err := trafficHistory(e.App, keys, rangeName, time.Now())
	if err != nil {
		return e.BadRequestError("Failed to load the traffic history", err)
	}
	return e.JSON(200, history)
}
