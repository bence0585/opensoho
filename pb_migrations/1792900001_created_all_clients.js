/// <reference path="../pb_data/types.d.ts" />
// Every client in one list: the wifi clients (online when an AP reported
// them in the last 30 seconds) and the wired clients located with the bridge
// forwarding databases (online when seen in the last 10 minutes, see
// lanclients.go). A MAC that is in both lists shows up once, as whichever was
// seen last. The union sits in a subquery because the view query parser
// doesn't accept a UNION at the top level.
migrate((app) => {
  const collection = new Collection({
    "createRule": null,
    "deleteRule": null,
    "id": "pbc_1792900001",
    "listRule": null,
    "name": "all_clients",
    "system": false,
    "type": "view",
    "updateRule": null,
    "viewQuery": "SELECT u.id, CAST(u.status AS TEXT) AS status, CAST(u.kind AS TEXT) AS kind, CAST(u.name AS TEXT) AS name, CAST(u.ip_address AS TEXT) AS ip_address, CAST(u.mac_address AS TEXT) AS mac_address, d.id AS device, CAST(u.ssid AS TEXT) AS ssid, CAST(u.band AS TEXT) AS band, CAST(u.port AS TEXT) AS port, CAST(CASE WHEN u.signal IS NULL THEN '' ELSE u.signal || ' dBm' END AS TEXT) AS signal, CAST(datetime(u.last_seen, 'localtime') AS TEXT) AS last_seen FROM ( SELECT c.id AS id, 'wifi' AS kind, CASE WHEN c.updated >= datetime('now', '-30 seconds') THEN 'online' ELSE 'offline' END AS status, COALESCE(NULLIF(c.alias, ''), NULLIF(c.hostname, ''), NULLIF(dl.hostname, ''), '') AS name, c.mac_address AS mac_address, COALESCE(NULLIF(c.ip_address, ''), dl.ip_address, '') AS ip_address, c.device AS device, COALESCE(c.ssid, '') AS ssid, COALESCE(c.band, '') AS band, '' AS port, c.signal AS signal, c.updated AS last_seen FROM clients c LEFT JOIN dhcp_leases dl ON dl.mac_address == c.mac_address WHERE NOT EXISTS (SELECT 1 FROM lan_clients l2 WHERE lower(l2.mac_address) == lower(c.mac_address) AND l2.last_seen > c.updated) UNION ALL SELECT l.id, 'wired', CASE WHEN l.last_seen >= datetime('now', '-10 minutes') THEN 'online' ELSE 'offline' END, COALESCE(NULLIF(l.alias, ''), NULLIF(l.hostname, ''), ''), l.mac_address, COALESCE(l.ip_address, ''), l.device, '', '', COALESCE(l.port, ''), NULL, l.last_seen FROM lan_clients l WHERE NOT EXISTS (SELECT 1 FROM clients c2 WHERE lower(c2.mac_address) == lower(l.mac_address) AND c2.updated >= l.last_seen) ) u LEFT JOIN devices d ON d.id == u.device ORDER BY u.status DESC, u.kind, lower(u.name)",
    "viewRule": null
  });

  return app.save(collection);
}, (app) => {
  const collection = app.findCollectionByNameOrId("pbc_1792900001");

  return app.delete(collection);
})
