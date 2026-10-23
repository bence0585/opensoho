package main

import (
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// The traffic map: the network as a tree from the internet, through the
// gateway, the links between the routers (directly or through unmanaged
// switches) to the hosts, with the current traffic of every link.
//
// The links between the routers come from LLDP: a router port with another
// router as LLDP neighbour is linked to the port of that router where the
// first one's MAC address is learned (the bridge forwarding database). The
// ports linked together form a segment. A segment of more than two ports, or
// with other LLDP neighbours (hosts) or wired clients behind a switch, is an
// unmanaged switch. Routers without LLDP are linked with the forwarding
// database alone: the gateway port where they are learned.

// TopologyNode is a node of the traffic map, with the link to its parent.
type TopologyNode struct {
	ID     string `json:"id"`
	Parent string `json:"parent"`
	// internet, router, switch or host
	Type string `json:"type"`
	Name string `json:"name"`
	Sub  string `json:"sub"`
	// the port of the parent router the link is on
	Port string `json:"port"`
	Wifi bool   `json:"wifi"`
	// The traffic of the link to the parent: down is away from the internet.
	DownBps float64 `json:"down"`
	UpBps   float64 `json:"up"`
	// Approx means only the internet traffic of the link is known.
	Approx bool `json:"approx"`
	// the traffic series of the link, swap: its tx is the down direction
	Key  string `json:"key"`
	Swap bool   `json:"swap"`
}

type topologyPort struct {
	device string
	port   string
}

// unionFind groups the ports linked together into segments.
type unionFind map[topologyPort]topologyPort

func (u unionFind) find(p topologyPort) topologyPort {
	parent, ok := u[p]
	if !ok {
		u[p] = p
		return p
	}
	if parent == p {
		return p
	}
	root := u.find(parent)
	u[p] = root
	return root
}

func (u unionFind) union(a topologyPort, b topologyPort) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u[ra] = rb
	}
}

func buildTopology(app core.App, rates map[string]TrafficRate, now time.Time) ([]TopologyNode, error) {
	overview, err := liveTrafficOverview(app, rates, now)
	if err != nil {
		return nil, err
	}
	devices, err := app.FindAllRecords("devices")
	if err != nil {
		return nil, err
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].GetString("name") < devices[j].GetString("name") })
	deviceNames := map[string]string{}
	for _, d := range devices {
		deviceNames[d.Id] = d.GetString("name")
	}

	// the MAC addresses of every device, and where each device is learned
	routerOf := map[string]string{}
	for _, d := range devices {
		if mac := normalizeMAC(d.GetString("mac_address")); mac != "" {
			routerOf[mac] = d.Id
		}
	}
	fdbRows, _ := app.FindAllRecords("bridge_fdb")
	for _, row := range fdbRows {
		if row.GetBool("local") {
			routerOf[row.GetString("mac_address")] = row.GetString("device")
		}
	}
	// learnedOn[A][B] is the port of A where device B is learned
	learnedOn := map[string]map[string]string{}
	for _, row := range fdbRows {
		owner, ok := routerOf[row.GetString("mac_address")]
		if row.GetBool("local") || !ok {
			continue
		}
		device := row.GetString("device")
		if learnedOn[device] == nil {
			learnedOn[device] = map[string]string{}
		}
		learnedOn[device][owner] = row.GetString("port")
	}

	// The gateway: the device that reports the internet traffic, or the DHCP
	// server.
	gateway := ""
	for _, rate := range rates {
		if rate.Device != "" && gatewayCandidate(rates, rate.Device) {
			gateway = rate.Device
			break
		}
	}
	if gateway == "" {
		if leases, err := app.FindAllRecords("dhcp_leases"); err == nil {
			for _, lease := range leases {
				if d := lease.GetString("device"); d != "" {
					gateway = d
					break
				}
			}
		}
	}
	if gateway == "" && len(devices) > 0 {
		gateway = devices[0].Id
	}

	segments := unionFind{}
	hostNeighbors := map[topologyPort]bool{}
	linked := map[string]bool{}
	type pendingLink struct {
		near topologyPort
		far  string
	}
	unknown := []pendingLink{}
	lldpRows, _ := app.FindAllRecords("lldp")
	for _, row := range lldpRows {
		a := topologyPort{row.GetString("device"), row.GetString("port")}
		segments.find(a)
		b, ok := routerOf[normalizeMAC(row.GetString("neighbor_mac_address"))]
		if !ok {
			hostNeighbors[a] = true
			continue
		}
		if b == a.device {
			continue
		}
		linked[a.device] = true
		linked[b] = true
		port, ok := learnedOn[b][a.device]
		if !ok {
			unknown = append(unknown, pendingLink{a, b})
			continue
		}
		segments.union(a, topologyPort{b, port})
	}
	// A link whose far port isn't known (that router didn't learn this one)
	// joins the segment through a port of the router that is known to be in
	// it already, or a placeholder.
	for _, link := range unknown {
		root := segments.find(link.near)
		port := "?"
		for p := range segments {
			if p.device == link.far && segments.find(p) == root {
				port = p.port
				break
			}
		}
		segments.union(link.near, topologyPort{link.far, port})
	}
	// routers without LLDP on either side: the gateway port they are learned on
	for _, d := range devices {
		if d.Id == gateway || linked[d.Id] {
			continue
		}
		gwPort, ok := learnedOn[gateway][d.Id]
		if !ok {
			continue
		}
		port := learnedOn[d.Id][gateway]
		if port == "" {
			port = "?"
		}
		segments.union(topologyPort{gateway, gwPort}, topologyPort{d.Id, port})
	}

	members := map[topologyPort][]topologyPort{}
	for p := range segments {
		root := segments.find(p)
		members[root] = append(members[root], p)
	}
	for root := range members {
		sort.Slice(members[root], func(i, j int) bool {
			a, b := members[root][i], members[root][j]
			if a.device != b.device {
				return deviceNames[a.device] < deviceNames[b.device]
			}
			return a.port < b.port
		})
	}

	// wired hosts: behind a switch, or per router port
	type wiredPlace struct {
		port         topologyPort
		behindSwitch bool
	}
	wired := map[string]wiredPlace{}
	if lanClients, err := app.FindAllRecords("lan_clients"); err == nil {
		for _, c := range lanClients {
			wired[c.GetString("mac_address")] = wiredPlace{
				port:         topologyPort{c.GetString("device"), c.GetString("port")},
				behindSwitch: c.GetBool("behind_switch"),
			}
		}
	}
	switchHosts := map[topologyPort]bool{}
	for _, place := range wired {
		if place.behindSwitch {
			switchHosts[segments.find(place.port)] = true
		}
	}
	isSwitch := func(root topologyPort) bool {
		if len(members[root]) > 2 || switchHosts[root] {
			return true
		}
		for _, p := range members[root] {
			if hostNeighbors[p] {
				return true
			}
		}
		return false
	}

	portRate := func(p topologyPort, swap bool) (string, float64, float64) {
		key := portTrafficKey(p.device, p.port)
		rate, ok := rates[key]
		if base, _, isVlan := strings.Cut(p.port, "."); !ok && isVlan {
			// A VLAN interface (swconfig targets: eth0.1) has no counters of
			// its own here, its physical interface does.
			key = portTrafficKey(p.device, base)
			rate = rates[key]
		}
		if swap {
			return key, rate.TxBps, rate.RxBps
		}
		return key, rate.RxBps, rate.TxBps
	}

	nodes := []TopologyNode{{ID: "internet", Type: "internet", Name: "Internet"}}
	gwNode := TopologyNode{ID: "device:" + gateway, Parent: "internet", Type: "router", Name: deviceNames[gateway], Port: "wan"}
	if len(overview.Wan) > 0 {
		gwNode.Key, gwNode.DownBps, gwNode.UpBps = overview.Wan[0].Key, overview.Wan[0].RxBps, overview.Wan[0].TxBps
		gwNode.Port = overview.Wan[0].Port
	}
	nodes = append(nodes, gwNode)

	// From the gateway, through the segments, to every router.
	placed := map[string]bool{gateway: true}
	switchOf := map[topologyPort]string{}
	queue := []string{gateway}
	for len(queue) > 0 {
		device := queue[0]
		queue = queue[1:]
		roots := []topologyPort{}
		for root, ports := range members {
			for _, p := range ports {
				if p.device == device {
					roots = append(roots, root)
					break
				}
			}
		}
		sort.Slice(roots, func(i, j int) bool { return members[roots[i]][0].port < members[roots[j]][0].port })
		for _, root := range roots {
			if _, done := switchOf[root]; done {
				continue
			}
			var own topologyPort
			for _, p := range members[root] {
				if p.device == device {
					own = p
					break
				}
			}
			parentID := "device:" + device
			if isSwitch(root) {
				switchID := "switch:" + own.device + "|" + own.port
				switchOf[root] = switchID
				key, down, up := portRate(own, true)
				nodes = append(nodes, TopologyNode{
					ID: switchID, Parent: parentID, Type: "switch", Name: "Switch",
					Sub: deviceNames[own.device] + " " + own.port, Port: own.port,
					DownBps: down, UpBps: up, Key: key, Swap: true,
				})
				parentID = switchID
			} else {
				switchOf[root] = ""
			}
			for _, p := range members[root] {
				if placed[p.device] {
					continue
				}
				placed[p.device] = true
				queue = append(queue, p.device)
				node := TopologyNode{ID: "device:" + p.device, Parent: parentID, Type: "router", Name: deviceNames[p.device]}
				if parentID == "device:"+device {
					// a direct link: the port of the parent
					node.Port = own.port
					node.Key, node.DownBps, node.UpBps = portRate(own, true)
					node.Swap = true
				} else {
					// through a switch: its own port, which receives the down traffic
					node.Port = p.port
					node.Key, node.DownBps, node.UpBps = portRate(p, false)
				}
				nodes = append(nodes, node)
			}
		}
	}
	// routers that couldn't be placed hang from the gateway, link unknown
	for _, d := range devices {
		if !placed[d.Id] {
			nodes = append(nodes, TopologyNode{ID: "device:" + d.Id, Parent: "device:" + gateway, Type: "router", Name: deviceNames[d.Id], Approx: true})
		}
	}

	// Hosts. Wired hosts that share a port that isn't a known segment are on
	// a switch of their own.
	hostsPerPort := map[topologyPort]int{}
	for _, h := range overview.Clients {
		if place, ok := wired[h.MAC]; ok && !place.behindSwitch && !h.Router {
			hostsPerPort[place.port]++
		}
	}
	extraSwitches := map[topologyPort]bool{}
	for _, h := range overview.Clients {
		if h.Router {
			continue
		}
		node := TopologyNode{ID: "host:" + h.MAC, Type: "host", Name: h.Name, Sub: h.IP}
		if node.Name == "" {
			node.Name = h.MAC
		}
		internet := func() {
			node.Key, node.DownBps, node.UpBps, node.Approx = h.InternetKey, h.InternetRxBps, h.InternetTxBps, true
		}
		place, isWired := wired[h.MAC]
		switch {
		case isWired && place.behindSwitch:
			if switchID := switchOf[segments.find(place.port)]; switchID != "" {
				node.Parent = switchID
			} else {
				node.Parent = "device:" + place.port.device
				node.Port = place.port.port
			}
			internet()
		case isWired && hostsPerPort[place.port] > 1:
			switchID := "switch:" + place.port.device + "|" + place.port.port
			if !extraSwitches[place.port] {
				extraSwitches[place.port] = true
				key, down, up := portRate(place.port, true)
				nodes = append(nodes, TopologyNode{
					ID: switchID, Parent: "device:" + place.port.device, Type: "switch", Name: "Switch",
					Sub: deviceNames[place.port.device] + " " + place.port.port, Port: place.port.port,
					DownBps: down, UpBps: up, Key: key, Swap: true,
				})
			}
			node.Parent = switchID
			internet()
		case isWired:
			node.Parent = "device:" + place.port.device
			node.Port = place.port.port
			if h.Key != "" {
				node.Key, node.DownBps, node.UpBps, node.Swap = h.Key, h.RxBps, h.TxBps, true
			} else {
				internet()
			}
		case h.Key != "":
			// a wifi client, on its AP
			node.Wifi = true
			node.Port = h.Where
			if rate, ok := rates[h.Key]; ok && rate.Device != "" {
				node.Parent = "device:" + rate.Device
			}
			node.Key, node.DownBps, node.UpBps = h.Key, h.RxBps, h.TxBps
		default:
			// only the gateway knows it
			internet()
		}
		if node.Parent == "" {
			node.Parent = "device:" + gateway
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// gatewayCandidate reports whether the device reports nlbwmon data.
func gatewayCandidate(rates map[string]TrafficRate, device string) bool {
	for key, rate := range rates {
		if rate.Device == device && len(key) > 5 && key[:5] == "nlbw|" {
			return true
		}
	}
	return false
}

func apiTrafficTopology(e *core.RequestEvent) error {
	now := time.Now()
	nodes, err := buildTopology(e.App, traffic.Rates(now), now)
	if err != nil {
		return e.InternalServerError("Failed to build the traffic map", err)
	}
	return e.JSON(200, map[string]any{"time": now.Unix(), "nodes": nodes})
}
