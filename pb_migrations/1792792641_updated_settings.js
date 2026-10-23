/// <reference path="../pb_data/types.d.ts" />
const baseValues = [
  "country",
  "mqtt_enabled",
  "mqtt_broker",
  "mqtt_username",
  "mqtt_password"
]

function nameField(values) {
  return new Field({
    "hidden": false,
    "id": "select1579384326",
    "maxSelect": 1,
    "name": "name",
    "presentable": true,
    "required": true,
    "system": false,
    "type": "select",
    "values": values
  })
}

migrate((app) => {
  const collection = app.findCollectionByNameOrId("pbc_2769025244")

  // extend the "name" select with the usteer tuning keys (see usteerSettings)
  collection.fields.addAt(1, nameField(baseValues.concat([
    "usteer_roam_scan_snr",
    "usteer_roam_trigger_snr",
    "usteer_signal_diff_threshold",
    "usteer_band_steering_threshold",
    "usteer_band_steering_min_snr",
    "usteer_min_snr",
    "usteer_min_snr_kick_delay",
    "usteer_min_connect_snr",
    "usteer_assoc_steering"
  ])))

  return app.save(collection)
}, (app) => {
  const collection = app.findCollectionByNameOrId("pbc_2769025244")

  collection.fields.addAt(1, nameField(baseValues))

  return app.save(collection)
})
