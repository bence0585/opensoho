/// <reference path="../pb_data/types.d.ts" />
// Qualify the columns of the client views, so new dhcp_leases and clients
// fields with the same names don't make them ambiguous.
migrate((app) => {
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
}, (app) => {
  {
    const collection = app.findCollectionByNameOrId("pbc_1876670203")
    unmarshal({ "viewQuery": "  SELECT clients.mac_address, COALESCE(ip_address,\"\") as ip_address, COALESCE(hostname,\"\") as hostname, alias, device, ssid, frequency, channel, band, signal, tx_rate/100000 as tx_rate, rx_rate/100000 as rx_rate, tx_bytes, rx_bytes, dhcp_leases.updated AS last_seen, clients.id FROM clients LEFT JOIN dhcp_leases ON dhcp_leases.mac_address == clients.mac_address WHERE clients.updated >= datetime('now', '-30 seconds')" }, collection)
    app.save(collection)
  }
  {
    const collection = app.findCollectionByNameOrId("pbc_3440715838")
    unmarshal({ "viewQuery": "  SELECT clients.mac_address, COALESCE(ip_address,\"\") as ip_address, COALESCE(hostname,\"\") as hostname, alias, device, ssid, frequency, channel, band, signal, tx_bytes, rx_bytes, dhcp_leases.updated AS last_seen, clients.id FROM clients LEFT JOIN dhcp_leases ON dhcp_leases.mac_address == clients.mac_address WHERE clients.updated < datetime('now', '-30 seconds')" }, collection)
    app.save(collection)
  }
})
