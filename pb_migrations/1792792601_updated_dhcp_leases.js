/// <reference path="../pb_data/types.d.ts" />
migrate((app) => {
  const collection = app.findCollectionByNameOrId("pbc_2401716254")

  // the DHCP server that handed out the lease
  collection.fields.add(new Field({
    "cascadeDelete": true,
    "collectionId": "pbc_2153001328",
    "hidden": false,
    "id": "relation1792792601",
    "maxSelect": 1,
    "minSelect": 0,
    "name": "device",
    "presentable": false,
    "required": false,
    "system": false,
    "type": "relation"
  }))

  // the static reservation of the MAC address, if any
  collection.fields.add(new Field({
    "cascadeDelete": false,
    "collectionId": "pbc_3986314751",
    "hidden": false,
    "id": "relation1792792602",
    "maxSelect": 1,
    "minSelect": 0,
    "name": "reservation",
    "presentable": false,
    "required": false,
    "system": false,
    "type": "relation"
  }))

  // set to create a reservation from the lease, reset again by OpenSOHO
  collection.fields.add(new Field({
    "hidden": false,
    "id": "bool1792792603",
    "name": "make_static",
    "presentable": false,
    "required": false,
    "system": false,
    "type": "bool"
  }))

  return app.save(collection)
}, (app) => {
  const collection = app.findCollectionByNameOrId("pbc_2401716254")

  collection.fields.removeById("relation1792792601")
  collection.fields.removeById("relation1792792602")
  collection.fields.removeById("bool1792792603")

  return app.save(collection)
})
