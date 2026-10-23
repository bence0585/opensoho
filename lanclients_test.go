package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	_ "github.com/pocketbase/pocketbase/migrations" // registers core.SystemMigrations
	"github.com/pocketbase/pocketbase/plugins/jsvm"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	loadLanClientsMigrations sync.Once
	lanClientsMigrations     core.MigrationsList
)

// newMigratedApp returns an app backed by in-memory SQLite databases with the
// real pb_migrations applied (like tests/cascade_delete_test.go), as the code
// under test spans many collections.
func newMigratedApp(t *testing.T) core.App {
	t.Helper()

	loadLanClientsMigrations.Do(func() {
		orig := core.AppMigrations
		core.AppMigrations = core.MigrationsList{}
		jsvm.MustRegister(core.NewBaseApp(core.BaseAppConfig{}), jsvm.Config{MigrationsDir: "pb_migrations"})
		lanClientsMigrations = core.AppMigrations
		core.AppMigrations = orig
	})

	dbPrefix := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir: t.TempDir(),
		DBConnect: func(dbPath string) (*dbx.DB, error) {
			name := dbPrefix + "_" + strings.TrimSuffix(dbPath[strings.LastIndex(dbPath, "/")+1:], ".db")
			return dbx.Open("sqlite", fmt.Sprintf("file:/%s?vfs=memdb&_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)", name))
		},
	})
	t.Cleanup(func() { app.ResetBootstrapState() })
	require.NoError(t, app.Bootstrap())

	migrations := core.MigrationsList{}
	migrations.Copy(core.SystemMigrations)
	migrations.Copy(lanClientsMigrations)
	_, err := core.NewMigrationsRunner(app, migrations).Up()
	require.NoError(t, err)

	return app
}

func newTestRecord(t *testing.T, app core.App, collection string, values map[string]any) *core.Record {
	t.Helper()
	c, err := app.FindCollectionByNameOrId(collection)
	require.NoError(t, err)
	r := core.NewRecord(c)
	for k, v := range values {
		r.Set(k, v)
	}
	require.NoError(t, app.SaveNoValidate(r))
	return r
}

func lanClientByMAC(t *testing.T, app core.App, mac string) *core.Record {
	t.Helper()
	r, err := app.FindFirstRecordByData("lan_clients", "mac_address", mac)
	require.NoError(t, err, mac)
	return r
}

func TestDumpFdbPayload(t *testing.T) {
	var data OpenSohoData
	payload := `{"type":"OpenSoho","fdb":[{"bridge":"br-lan","entries":[{"mac":"AA:BB:CC:00:00:01","port":"lan1","local":false,"age":12}]}]}`
	require.NoError(t, json.Unmarshal([]byte(payload), &data))
	assert.Equal(t, []FdbBridge{{
		Bridge:  "br-lan",
		Entries: []FdbEntry{{MAC: "AA:BB:CC:00:00:01", Port: "lan1", Local: false, Age: 12}},
	}}, data.Fdb)

	// The radios dump has no fdb key, which must not wipe the stored table.
	data = OpenSohoData{}
	require.NoError(t, json.Unmarshal([]byte(`{"type":"OpenSoho","radios":[]}`), &data))
	assert.Nil(t, data.Fdb)
}

// The home network: the gateway "fo" has a dumb switch on lan2 with the AP
// "lenti" (lan1) and a PC behind it. A NAS is on fo lan1, a TV on lenti lan2
// and a phone on the wifi of lenti.
func TestLocateLanClients(t *testing.T) {
	app := newMigratedApp(t)
	now := time.Now()

	fo := newTestRecord(t, app, "devices", map[string]any{"name": "fo", "mac_address": "80:AF:CA:BE:90:EC"})
	lenti := newTestRecord(t, app, "devices", map[string]any{"name": "lenti", "mac_address": "88:C3:97:EE:A6:A1"})
	newTestRecord(t, app, "interfaces", map[string]any{"device": lenti.Id, "interface": "phy1-ap0"})
	newTestRecord(t, app, "dhcp_leases", map[string]any{
		"mac_address": "00:23:24:7e:87:94", "ip_address": "192.168.1.10", "hostname": "nas", "device": fo.Id,
	})
	newTestRecord(t, app, "dhcp_reservations", map[string]any{
		"name": "teve", "mac_address": "68:07:0A:BB:F4:F1", "ip_address": "192.168.1.175", "dhcp_server": []string{fo.Id},
	})

	const (
		nas    = "00:23:24:7e:87:94"
		pc     = "00:d8:61:51:e4:d6"
		tv     = "68:07:0a:bb:f4:f1"
		phone  = "92:47:b2:61:fc:f0"
		laptop = "3c:a9:f4:6b:f0:24"
	)
	// a wifi client that has just been seen on the wifi of fo
	newTestRecord(t, app, "clients", map[string]any{"mac_address": laptop, "device": fo.Id})

	require.NoError(t, syncBridgeFdb(app, fo, []FdbBridge{{Bridge: "br-lan", Entries: []FdbEntry{
		{MAC: "80:af:ca:be:90:ec", Port: "lan1", Local: true},
		{MAC: nas, Port: "lan1", Age: 3},
		{MAC: "88:c3:97:ee:a6:a1", Port: "lan2", Age: 0},
		{MAC: pc, Port: "lan2", Age: 5},
		{MAC: tv, Port: "lan2", Age: 9},
		{MAC: phone, Port: "lan2", Age: 1},
		{MAC: laptop, Port: "phy1-ap0", Age: 1},
		{MAC: "01:80:c2:00:00:0e", Port: "lan2"},
	}}}))
	require.NoError(t, syncBridgeFdb(app, lenti, []FdbBridge{{Bridge: "br-lan", Entries: []FdbEntry{
		{MAC: "88:c3:97:ee:a6:a1", Port: "lan1", Local: true},
		{MAC: "80:af:ca:be:90:ec", Port: "lan1", Age: 0},
		{MAC: nas, Port: "lan1", Age: 3},
		{MAC: pc, Port: "lan1", Age: 5},
		{MAC: tv, Port: "lan2", Age: 9},
		{MAC: phone, Port: "phy1-ap0", Age: 1},
	}}}))

	require.NoError(t, locateLanClients(app, now))

	all, err := app.FindAllRecords("lan_clients")
	require.NoError(t, err)
	assert.Len(t, all, 3, "the routers, the wifi clients and multicast are no lan clients")

	r := lanClientByMAC(t, app, nas)
	assert.Equal(t, fo.Id, r.GetString("device"))
	assert.Equal(t, "lan1", r.GetString("port"))
	assert.False(t, r.GetBool("behind_switch"))
	assert.Equal(t, "nas", r.GetString("hostname"))
	assert.Equal(t, "192.168.1.10", r.GetString("ip_address"))

	r = lanClientByMAC(t, app, tv)
	assert.Equal(t, lenti.Id, r.GetString("device"))
	assert.Equal(t, "lan2", r.GetString("port"))
	assert.False(t, r.GetBool("behind_switch"))
	assert.Equal(t, "teve", r.GetString("hostname"), "the reservation names the client")
	assert.Equal(t, "192.168.1.175", r.GetString("ip_address"))
	assert.Equal(t, "TPVision Europe B.V", r.GetString("vendor"))

	// Only seen on the uplinks: on the switch, attributed to the DHCP server.
	r = lanClientByMAC(t, app, pc)
	assert.Equal(t, fo.Id, r.GetString("device"))
	assert.Equal(t, "lan2", r.GetString("port"))
	assert.True(t, r.GetBool("behind_switch"))

	// The neighbor table fills in the address of a client without a lease.
	applyNeighbors(app, []Neighbor{
		{MAC: "00:D8:61:51:E4:D6", IP: "192.168.1.35", State: "REACHABLE"},
		{MAC: tv, IP: "192.168.1.99", State: "FAILED"},
		{MAC: nas, IP: "fe80::1", State: "REACHABLE"},
	})
	assert.Equal(t, "192.168.1.35", lanClientByMAC(t, app, pc).GetString("ip_address"))
	assert.Equal(t, "192.168.1.175", lanClientByMAC(t, app, tv).GetString("ip_address"))
	assert.Equal(t, "192.168.1.10", lanClientByMAC(t, app, nas).GetString("ip_address"))

	// A device that stopped reporting no longer moves its clients, and
	// clients that weren't seen for a month are dropped.
	lenti.Set("last_seen", types.NowDateTime().AddDate(0, 0, -1))
	require.NoError(t, app.SaveNoValidate(lenti))
	require.NoError(t, syncBridgeFdb(app, fo, []FdbBridge{{Bridge: "br-lan", Entries: []FdbEntry{
		{MAC: nas, Port: "lan1"},
		{MAC: "88:c3:97:ee:a6:a1", Port: "lan2"},
		{MAC: pc, Port: "lan2"},
		{MAC: tv, Port: "lan2"},
	}}}))
	require.NoError(t, locateLanClients(app, now.Add(lanClientRetention+time.Hour)))
	all, err = app.FindAllRecords("lan_clients")
	require.NoError(t, err)
	assert.Len(t, all, 3, "fo still reports, so its sightings are refreshed")
	r = lanClientByMAC(t, app, tv)
	assert.Equal(t, fo.Id, r.GetString("device"), "only fo sees the tv now")
	assert.True(t, r.GetBool("behind_switch"))

	// A device that disappears from the fdb of every router ages out.
	require.NoError(t, syncBridgeFdb(app, fo, []FdbBridge{{Bridge: "br-lan", Entries: []FdbEntry{
		{MAC: nas, Port: "lan1"},
	}}}))
	require.NoError(t, locateLanClients(app, now.Add(2*lanClientRetention+2*time.Hour)))
	all, err = app.FindAllRecords("lan_clients")
	require.NoError(t, err)
	assert.Len(t, all, 1)
	assert.Equal(t, nas, all[0].GetString("mac_address"))
}

func TestSyncBridgeFdbReplacesDeviceRows(t *testing.T) {
	app := newMigratedApp(t)
	fo := newTestRecord(t, app, "devices", map[string]any{"name": "fo"})
	lenti := newTestRecord(t, app, "devices", map[string]any{"name": "lenti"})

	require.NoError(t, syncBridgeFdb(app, lenti, []FdbBridge{{Bridge: "br-lan", Entries: []FdbEntry{{MAC: "aa:00:00:00:00:01", Port: "lan1"}}}}))
	require.NoError(t, syncBridgeFdb(app, fo, []FdbBridge{{Bridge: "br-lan", Entries: []FdbEntry{
		{MAC: "aa:00:00:00:00:02", Port: "lan1"},
		{MAC: "not a mac", Port: "lan1"},
		{MAC: "aa:00:00:00:00:03", Port: ""},
	}}}))
	require.NoError(t, syncBridgeFdb(app, fo, []FdbBridge{{Bridge: "br-lan", Entries: []FdbEntry{{MAC: "AA:00:00:00:00:04", Port: "lan3"}}}}))

	rows, err := app.FindAllRecords("bridge_fdb", dbx.HashExp{"device": fo.Id})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "aa:00:00:00:00:04", rows[0].GetString("mac_address"))
	assert.Equal(t, "lan3", rows[0].GetString("port"))

	rows, err = app.FindAllRecords("bridge_fdb", dbx.HashExp{"device": lenti.Id})
	require.NoError(t, err)
	assert.Len(t, rows, 1, "the rows of other devices are kept")
}

func TestStoreDHCPLeasesLinksServerAndReservation(t *testing.T) {
	app := newMigratedApp(t)
	fo := newTestRecord(t, app, "devices", map[string]any{"name": "fo"})
	reservation := newTestRecord(t, app, "dhcp_reservations", map[string]any{
		"name": "xbox", "mac_address": "C4:CB:76:07:01:BA", "ip_address": "192.168.1.34",
	})

	storeDHCPLeases(app, fo, []DHCPLease{
		{MACAddress: "c4:cb:76:07:01:ba", IPAddress: "192.168.1.34", Hostname: "XboxOne", Expiry: int(time.Now().Unix()) + 3600},
		{MACAddress: "3c:a9:f4:6b:f0:24", IPAddress: "192.168.1.170", Hostname: "gb-dell", Expiry: int(time.Now().Unix()) + 3600},
	}, types.NowDateTime())

	lease, err := app.FindFirstRecordByData("dhcp_leases", "mac_address", "c4:cb:76:07:01:ba")
	require.NoError(t, err)
	assert.Equal(t, fo.Id, lease.GetString("device"))
	assert.Equal(t, reservation.Id, lease.GetString("reservation"))

	lease, err = app.FindFirstRecordByData("dhcp_leases", "mac_address", "3c:a9:f4:6b:f0:24")
	require.NoError(t, err)
	assert.Equal(t, fo.Id, lease.GetString("device"))
	assert.Equal(t, "", lease.GetString("reservation"))

	// The wifi clients get the reservation name and the leased address.
	ip, hostname := lookupHostInfo(app, "C4:CB:76:07:01:BA")
	assert.Equal(t, "192.168.1.34", ip)
	assert.Equal(t, "xbox", hostname)
	ip, hostname = lookupHostInfo(app, "3c:a9:f4:6b:f0:24")
	assert.Equal(t, "192.168.1.170", ip)
	assert.Equal(t, "gb-dell", hostname)
	ip, hostname = lookupHostInfo(app, "02:00:00:00:00:01")
	assert.Equal(t, "", ip)
	assert.Equal(t, "", hostname)
}

func TestMakeLeaseStatic(t *testing.T) {
	app := newMigratedApp(t)
	app.OnRecordValidate("dhcp_reservations").BindFunc(func(e *core.RecordEvent) error {
		if err := validateDhcpReservation(e.Record); err != nil {
			return err
		}
		return e.Next()
	})
	fo := newTestRecord(t, app, "devices", map[string]any{"name": "fo"})
	newTestRecord(t, app, "dhcp_reservations", map[string]any{
		"name": "gb-dell", "mac_address": "11:22:33:44:55:66", "ip_address": "192.168.1.50",
	})

	lease := newTestRecord(t, app, "dhcp_leases", map[string]any{
		"mac_address": "3c:a9:f4:6b:f0:24", "ip_address": "192.168.1.170", "hostname": "GB-Dell", "device": fo.Id,
	})
	require.NoError(t, makeLeaseStatic(app, lease))

	reservation, err := app.FindRecordById("dhcp_reservations", lease.GetString("reservation"))
	require.NoError(t, err)
	assert.Equal(t, "gb-dell-2", reservation.GetString("name"), "the hostname is taken")
	assert.Equal(t, "3C:A9:F4:6B:F0:24", reservation.GetString("mac_address"))
	assert.Equal(t, "192.168.1.170", reservation.GetString("ip_address"))
	assert.Equal(t, []string{fo.Id}, reservation.GetStringSlice("dhcp_server"))

	// Again: the existing reservation is linked, no new one is made.
	lease.Set("reservation", nil)
	require.NoError(t, makeLeaseStatic(app, lease))
	assert.Equal(t, reservation.Id, lease.GetString("reservation"))
	total, err := app.CountRecords("dhcp_reservations")
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)

	// The server must be known, it is where the reservation is configured.
	unknown := newTestRecord(t, app, "dhcp_leases", map[string]any{
		"mac_address": "c4:cb:76:07:01:ba", "ip_address": "192.168.1.120", "hostname": "Xbox One",
	})
	assert.Error(t, makeLeaseStatic(app, unknown))

	// A reservation that fails validation isn't made.
	invalid := newTestRecord(t, app, "dhcp_leases", map[string]any{
		"mac_address": "c4:cb:76:07:01:ba", "ip_address": "not an ip", "hostname": "Xbox One", "device": fo.Id,
	})
	assert.Error(t, makeLeaseStatic(app, invalid))
}

func TestDhcpReservationName(t *testing.T) {
	app := newMigratedApp(t)
	newTestRecord(t, app, "dhcp_reservations", map[string]any{
		"name": "xbox-one", "mac_address": "11:22:33:44:55:66", "ip_address": "192.168.1.50",
	})

	assert.Equal(t, "galaxy-a20", dhcpReservationName(app, "Galaxy-A20", "d8:0b:9a:cd:73:2c"))
	assert.Equal(t, "xbox-one-2", dhcpReservationName(app, "Xbox One", "c4:cb:76:07:01:ba"))
	assert.Equal(t, "esp-c0f853", dhcpReservationName(app, "_esp_c0f853_", "c0:f8:53:a9:9b:ad"))
	assert.Equal(t, "host-a99bad", dhcpReservationName(app, "", "c0:f8:53:a9:9b:ad"))
	assert.Equal(t, "host-a99bad", dhcpReservationName(app, "*", "c0:f8:53:a9:9b:ad"))
	assert.Equal(t, strings.Repeat("a", 60), dhcpReservationName(app, strings.Repeat("a", 70), "c0:f8:53:a9:9b:ad"))
}

// The forwarding table of the router that has a client on its wifi (or that
// is the client itself) can come in after the one of another router, which
// only sees the address on an uplink.
func TestLocateLanClientsReportOrder(t *testing.T) {
	app := newMigratedApp(t)
	now := time.Now()

	fo := newTestRecord(t, app, "devices", map[string]any{"name": "fo", "mac_address": "80:AF:CA:BE:90:EC"})
	iot := newTestRecord(t, app, "devices", map[string]any{"name": "iot"})
	const (
		foPort  = "80:af:ca:be:90:ec"
		iotPort = "88:c3:97:84:03:19"
		esp     = "48:f6:ee:e2:d2:5c"
		pc      = "00:d8:61:51:e4:d6"
	)

	require.NoError(t, syncBridgeFdb(app, fo, []FdbBridge{{Bridge: "br-lan", Entries: []FdbEntry{
		{MAC: iotPort, Port: "lan2"},
		{MAC: esp, Port: "lan2"},
		{MAC: pc, Port: "lan2"},
	}}}))
	require.NoError(t, locateLanClients(app, now))
	total, err := app.CountRecords("lan_clients")
	require.NoError(t, err)
	assert.Equal(t, int64(3), total, "without the table of iot, these all look wired")

	require.NoError(t, syncBridgeFdb(app, iot, []FdbBridge{{Bridge: "br-lan", Entries: []FdbEntry{
		{MAC: iotPort, Port: "eth0.1", Local: true},
		{MAC: foPort, Port: "eth0.1"},
		{MAC: esp, Port: "phy1-ap0"},
		{MAC: pc, Port: "eth0.1"},
	}}}))
	require.NoError(t, locateLanClients(app, now))
	all, err := app.FindAllRecords("lan_clients")
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, pc, all[0].GetString("mac_address"))
	assert.True(t, all[0].GetBool("behind_switch"))
}

func TestApplyNeighborsFillsWifiClients(t *testing.T) {
	app := newMigratedApp(t)
	fo := newTestRecord(t, app, "devices", map[string]any{"name": "fo"})
	client := newTestRecord(t, app, "clients", map[string]any{"mac_address": "b8:06:0d:cc:fe:ef", "device": fo.Id})
	newTestRecord(t, app, "dhcp_leases", map[string]any{
		"mac_address": "b8:06:0d:cc:fe:ef", "ip_address": "192.168.1.152", "hostname": "termosztat", "device": fo.Id,
	})

	applyNeighbors(app, []Neighbor{{MAC: "B8:06:0D:CC:FE:EF", IP: "192.168.1.152", State: "STALE"}})

	client, err := app.FindRecordById("clients", client.Id)
	require.NoError(t, err)
	assert.Equal(t, "192.168.1.152", client.GetString("ip_address"))
	assert.Equal(t, "termosztat", client.GetString("hostname"))
}
