/// <reference path="../pb_data/types.d.ts" />
// The client views prefer the ip_address and hostname of the client itself
// (reservation name, neighbor table) over the DHCP lease.
migrate((app) => {
  {
    const collection = app.findCollectionByNameOrId("pbc_1876670203")
    unmarshal({ "viewQuery": "  SELECT clients.mac_address, COALESCE(NULLIF(clients.ip_address,\"\"), dhcp_leases.ip_address, \"\") as ip_address, COALESCE(NULLIF(clients.hostname,\"\"), dhcp_leases.hostname, \"\") as hostname, clients.alias, clients.device, clients.ssid, clients.frequency, clients.channel, clients.band, clients.signal, tx_rate/100000 as tx_rate, rx_rate/100000 as rx_rate, clients.tx_bytes, clients.rx_bytes, dhcp_leases.updated AS last_seen, clients.id FROM clients LEFT JOIN dhcp_leases ON dhcp_leases.mac_address == clients.mac_address WHERE clients.updated >= datetime('now', '-30 seconds')" }, collection)
    app.save(collection)
  }
  {
    const collection = app.findCollectionByNameOrId("pbc_3440715838")
    unmarshal({ "viewQuery": "  SELECT clients.mac_address, COALESCE(NULLIF(clients.ip_address,\"\"), dhcp_leases.ip_address, \"\") as ip_address, COALESCE(NULLIF(clients.hostname,\"\"), dhcp_leases.hostname, \"\") as hostname, clients.alias, clients.device, clients.ssid, clients.frequency, clients.channel, clients.band, clients.signal, clients.tx_bytes, clients.rx_bytes, dhcp_leases.updated AS last_seen, clients.id FROM clients LEFT JOIN dhcp_leases ON dhcp_leases.mac_address == clients.mac_address WHERE clients.updated < datetime('now', '-30 seconds')" }, collection)
    app.save(collection)
  }
}, (app) => {
  {
    const collection = app.findCollectionByNameOrId("pbc_1876670203")
    unmarshal({ "viewQuery": "  SELECT clients.mac_address, COALESCE(dhcp_leases.ip_address,\"\") as ip_address, COALESCE(dhcp_leases.hostname,\"\") as hostname, clients.alias, clients.device, clients.ssid, clients.frequency, clients.channel, clients.band, clients.signal, tx_rate/100000 as tx_rate, rx_rate/100000 as rx_rate, clients.tx_bytes, clients.rx_bytes, dhcp_leases.updated AS last_seen, clients.id FROM clients LEFT JOIN dhcp_leases ON dhcp_leases.mac_address == clients.mac_address WHERE clients.updated >= datetime('now', '-30 seconds')" }, collection)
    app.save(collection)
  }
  {
    const collection = app.findCollectionByNameOrId("pbc_3440715838")
    unmarshal({ "viewQuery": "  SELECT clients.mac_address, COALESCE(dhcp_leases.ip_address,\"\") as ip_address, COALESCE(dhcp_leases.hostname,\"\") as hostname, clients.alias, clients.device, clients.ssid, clients.frequency, clients.channel, clients.band, clients.signal, clients.tx_bytes, clients.rx_bytes, dhcp_leases.updated AS last_seen, clients.id FROM clients LEFT JOIN dhcp_leases ON dhcp_leases.mac_address == clients.mac_address WHERE clients.updated < datetime('now', '-30 seconds')" }, collection)
    app.save(collection)
  }
})
