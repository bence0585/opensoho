package main

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// FdbEntry is one learned (or local) MAC address of a bridge port, as
// reported by scripts/dump-fdb.uc.
type FdbEntry struct {
	MAC   string `json:"mac"`
	Port  string `json:"port"`
	Local bool   `json:"local"`
	Age   int    `json:"age"`
}

// FdbBridge is the forwarding database of one bridge.
type FdbBridge struct {
	Bridge  string     `json:"bridge"`
	Entries []FdbEntry `json:"entries"`
}

const (
	// Devices that haven't reported for this long are left out when the
	// wired clients are located: their forwarding table is outdated.
	fdbDeviceTimeout = 10 * time.Minute
	// A MAC that was a wifi client this recently isn't a wired client. Its
	// address is still in the forwarding tables of the other routers for the
	// bridge ageing time (300s by default).
	wifiClientGracePeriod = 10 * time.Minute
	// Wired clients that weren't seen for this long are removed.
	lanClientRetention = 30 * 24 * time.Hour
)

func normalizeMAC(mac string) string {
	hw, err := net.ParseMAC(strings.TrimSpace(mac))
	if err != nil || len(hw) != 6 {
		return ""
	}
	return hw.String()
}

// isUnicastMAC is false for multicast/broadcast and the all-zero address.
func isUnicastMAC(mac string) bool {
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 || hw[0]&1 != 0 {
		return false
	}
	for _, octet := range hw {
		if octet != 0 {
			return true
		}
	}
	return false
}

func isWirelessPortName(port string) bool {
	return strings.HasPrefix(port, "phy") || strings.HasPrefix(port, "wlan")
}

// syncBridgeFdb replaces the bridge_fdb rows of a device with the reported
// forwarding database.
func syncBridgeFdb(app core.App, device *core.Record, bridges []FdbBridge) error {
	return app.RunInTransaction(func(txApp core.App) error {
		collection, err := txApp.FindCollectionByNameOrId("bridge_fdb")
		if err != nil {
			return err
		}
		_, err = txApp.DB().Delete(collection.Name, dbx.HashExp{"device": device.Id}).Execute()
		if err != nil {
			return err
		}
		for _, bridge := range bridges {
			for _, entry := range bridge.Entries {
				mac := normalizeMAC(entry.MAC)
				if mac == "" || entry.Port == "" {
					continue
				}
				record := core.NewRecord(collection)
				record.Set("device", device.Id)
				record.Set("bridge", bridge.Bridge)
				record.Set("port", entry.Port)
				record.Set("mac_address", mac)
				record.Set("local", entry.Local)
				record.Set("age", entry.Age)
				if err := txApp.Save(record); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// fdbSighting is a MAC address seen on a bridge port of a device.
type fdbSighting struct {
	device     string
	deviceName string
	port       string
	age        int
}

// locateLanClients determines the wired clients from the forwarding
// databases of all devices, and on which device and port each of them is.
//
// A client shows up in the table of every router on its path, so it is
// attributed to the port that has no other managed router behind it (an
// "uplink"). A client on an unmanaged switch between routers is only seen on
// uplinks; it is attributed to a DHCP server device (normally the gateway)
// and marked as behind a switch.
func locateLanClients(app core.App, now time.Time) error {
	devices, err := app.FindAllRecords("devices")
	if err != nil {
		return err
	}
	fdbRows, err := app.FindAllRecords("bridge_fdb")
	if err != nil {
		return err
	}

	deviceNames := map[string]string{}
	freshDevices := map[string]bool{}
	managed := map[string]bool{}
	for _, device := range devices {
		deviceNames[device.Id] = device.GetString("name")
		lastSeen := device.GetDateTime("last_seen")
		freshDevices[device.Id] = lastSeen.IsZero() || now.Sub(lastSeen.Time()) < fdbDeviceTimeout
		if mac := normalizeMAC(device.GetString("mac_address")); mac != "" {
			managed[mac] = true
		}
	}
	for _, row := range fdbRows {
		if row.GetBool("local") {
			managed[row.GetString("mac_address")] = true
		}
	}

	wirelessPorts := map[string]bool{}
	if interfaces, err := app.FindAllRecords("interfaces"); err == nil {
		for _, iface := range interfaces {
			wirelessPorts[iface.GetString("device")+"|"+iface.GetString("interface")] = true
		}
	}
	isWirelessPort := func(device string, port string) bool {
		return wirelessPorts[device+"|"+port] || isWirelessPortName(port)
	}

	sightings := map[string][]fdbSighting{}
	uplinks := map[string]bool{}
	for _, row := range fdbRows {
		device := row.GetString("device")
		if !freshDevices[device] || row.GetBool("local") {
			continue
		}
		mac := row.GetString("mac_address")
		port := row.GetString("port")
		if managed[mac] {
			uplinks[device+"|"+port] = true
			continue
		}
		if !isUnicastMAC(mac) {
			continue
		}
		sightings[mac] = append(sightings[mac], fdbSighting{
			device:     device,
			deviceName: deviceNames[device],
			port:       port,
			age:        row.GetInt("age"),
		})
	}

	wifiClients := map[string]bool{}
	if clients, err := app.FindAllRecords("clients"); err == nil {
		for _, client := range clients {
			if now.Sub(client.GetDateTime("updated").Time()) < wifiClientGracePeriod {
				wifiClients[normalizeMAC(client.GetString("mac_address"))] = true
			}
		}
	}

	dhcpServers := map[string]bool{}
	if leases, err := app.FindAllRecords("dhcp_leases"); err == nil {
		for _, lease := range leases {
			if device := lease.GetString("device"); device != "" {
				dhcpServers[device] = true
			}
		}
	}

	collection, err := app.FindCollectionByNameOrId("lan_clients")
	if err != nil {
		return err
	}
	seen, err := types.ParseDateTime(now)
	if err != nil {
		return err
	}

	// MACs that turned out not to be wired clients. Their rows are removed:
	// they can be made from the forwarding table of one router, before the
	// table of the router that has them on its wifi or its own port came in.
	notLan := map[string]bool{}
	for mac := range managed {
		notLan[mac] = true
	}

	for mac, list := range sightings {
		if wifiClients[mac] {
			notLan[mac] = true
			continue
		}
		wireless := false
		local := []fdbSighting{}
		for _, s := range list {
			wireless = wireless || isWirelessPort(s.device, s.port)
			if !uplinks[s.device+"|"+s.port] {
				local = append(local, s)
			}
		}
		if wireless {
			notLan[mac] = true
			continue
		}

		behindSwitch := len(local) == 0
		candidates := local
		if behindSwitch {
			candidates = list
		}
		sort.Slice(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			if behindSwitch && dhcpServers[a.device] != dhcpServers[b.device] {
				return dhcpServers[a.device]
			}
			if !behindSwitch && a.age != b.age {
				return a.age < b.age
			}
			if a.deviceName != b.deviceName {
				return a.deviceName < b.deviceName
			}
			return a.port < b.port
		})
		location := candidates[0]

		record := findFirstOrNew(app, collection, "mac_address", mac)
		record.Set("mac_address", mac)
		record.Set("vendor", lookupVendor(mac))
		record.Set("device", location.device)
		record.Set("port", location.port)
		record.Set("behind_switch", behindSwitch)
		record.Set("last_seen", seen)
		ip, hostname := lookupHostInfo(app, mac)
		if ip != "" {
			record.Set("ip_address", ip)
		}
		record.Set("hostname", hostname)
		if err := app.Save(record); err != nil {
			app.Logger().Error("Failed to store lan client", "mac", mac, "error", err)
		}
	}

	existing, err := app.FindAllRecords(collection)
	if err != nil {
		return err
	}
	for _, record := range existing {
		if notLan[record.GetString("mac_address")] {
			if err := app.Delete(record); err != nil {
				app.Logger().Error("Failed to remove lan client", "mac", record.GetString("mac_address"), "error", err)
			}
		}
	}

	cutoff, err := types.ParseDateTime(now.Add(-lanClientRetention))
	if err != nil {
		return err
	}
	_, err = app.DB().Delete(collection.Name, dbx.NewExp("last_seen < {:cutoff}", dbx.Params{"cutoff": cutoff.String()})).Execute()
	return err
}

// findByMAC finds a record by MAC address, regardless of its case.
func findByMAC(app core.App, collection string, mac string) *core.Record {
	record, err := app.FindFirstRecordByFilter(
		collection,
		"mac_address = {:lower} || mac_address = {:upper}",
		dbx.Params{"lower": strings.ToLower(mac), "upper": strings.ToUpper(mac)},
	)
	if err != nil {
		return nil
	}
	return record
}

// lookupHostInfo returns the IP address and hostname of a client from its
// DHCP lease and static reservation. The reservation name wins over the name
// the client sent, the leased address over the reserved one.
func lookupHostInfo(app core.App, mac string) (ip string, hostname string) {
	if reservation := findByMAC(app, "dhcp_reservations", mac); reservation != nil {
		ip = reservation.GetString("ip_address")
		hostname = reservation.GetString("name")
	}
	if lease := findByMAC(app, "dhcp_leases", mac); lease != nil {
		if leaseIP := lease.GetString("ip_address"); leaseIP != "" {
			ip = leaseIP
		}
		if hostname == "" {
			hostname = lease.GetString("hostname")
		}
	}
	return ip, hostname
}

// applyNeighbors updates the IP address of the known (wifi and wired)
// clients from the neighbor (ARP) table of a device. This also covers clients
// with a manually configured address, which have no DHCP lease. The gateway
// sees every client, so their hostnames are refreshed here as well.
func applyNeighbors(app core.App, neighbors []Neighbor) {
	for _, neighbor := range neighbors {
		switch strings.ToUpper(neighbor.State) {
		case "FAILED", "INCOMPLETE", "NOARP":
			continue
		}
		mac := normalizeMAC(neighbor.MAC)
		ip := net.ParseIP(neighbor.IP)
		if mac == "" || ip == nil || ip.To4() == nil {
			continue
		}
		_, hostname := lookupHostInfo(app, mac)
		for _, collection := range []string{"clients", "lan_clients"} {
			record := findByMAC(app, collection, mac)
			if record == nil || (record.GetString("ip_address") == ip.String() && record.GetString("hostname") == hostname) {
				continue
			}
			record.Set("ip_address", ip.String())
			record.Set("hostname", hostname)
			if err := app.Save(record); err != nil {
				app.Logger().Error("Failed to store neighbor ip", "collection", collection, "mac", mac, "error", err)
			}
		}
	}
}

var invalidHostnameCharsRegexp = regexp.MustCompile(`[^a-z0-9-]+`)

// dhcpReservationName derives a free, valid reservation name from the
// hostname a client sent.
func dhcpReservationName(app core.App, hostname string, mac string) string {
	base := invalidHostnameCharsRegexp.ReplaceAllString(strings.ToLower(hostname), "-")
	base = strings.Trim(base, "-")
	if len(base) > 60 {
		base = strings.TrimRight(base[:60], "-")
	}
	if base == "" {
		base = "host-" + strings.ReplaceAll(normalizeMAC(mac)[9:], ":", "")
	}
	name := base
	for i := 2; i < 100; i++ {
		if _, err := app.FindFirstRecordByData("dhcp_reservations", "name", name); err != nil {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// makeLeaseStatic turns a DHCP lease into a static reservation on the DHCP
// server that handed it out, and links the lease to it. A lease that already
// has a reservation is just linked.
func makeLeaseStatic(app core.App, lease *core.Record) error {
	mac := lease.GetString("mac_address")
	if reservation := findByMAC(app, "dhcp_reservations", mac); reservation != nil {
		lease.Set("reservation", reservation.Id)
		return nil
	}
	server := lease.GetString("device")
	if server == "" {
		return errors.New("The DHCP server of this lease is not known yet, try again after the next monitoring report.")
	}
	collection, err := app.FindCollectionByNameOrId("dhcp_reservations")
	if err != nil {
		return err
	}
	reservation := core.NewRecord(collection)
	reservation.Set("name", dhcpReservationName(app, lease.GetString("hostname"), mac))
	reservation.Set("mac_address", mac)
	reservation.Set("ip_address", lease.GetString("ip_address"))
	reservation.Set("dhcp_server", []string{server})
	if err := app.Save(reservation); err != nil {
		return err
	}
	lease.Set("reservation", reservation.Id)
	return nil
}
