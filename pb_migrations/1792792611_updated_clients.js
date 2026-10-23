/// <reference path="../pb_data/types.d.ts" />
migrate((app) => {
  const collection = app.findCollectionByNameOrId("pbc_2442875294")

  // from the DHCP lease or the neighbor table, see lookupHostInfo
  collection.fields.add(new Field({
    "autogeneratePattern": "",
    "hidden": false,
    "id": "text1792792611",
    "max": 0,
    "min": 0,
    "name": "ip_address",
    "pattern": "",
    "presentable": false,
    "primaryKey": false,
    "required": false,
    "system": false,
    "type": "text"
  }))

  // the reservation name, or else the name the client sent in its DHCP request
  collection.fields.add(new Field({
    "autogeneratePattern": "",
    "hidden": false,
    "id": "text1792792612",
    "max": 0,
    "min": 0,
    "name": "hostname",
    "pattern": "",
    "presentable": false,
    "primaryKey": false,
    "required": false,
    "system": false,
    "type": "text"
  }))

  return app.save(collection)
}, (app) => {
  const collection = app.findCollectionByNameOrId("pbc_2442875294")

  collection.fields.removeById("text1792792611")
  collection.fields.removeById("text1792792612")

  return app.save(collection)
})
