#!/usr/bin/ucode
// Deployed to /usr/share/opensoho/dump-fdb.uc and run by dump-poe.sh, which
// embeds the output as the "fdb" key of its OpenSoho payload.
//
// Prints the forwarding database of every Linux bridge as a JSON array:
// [{"bridge":"br-lan","entries":[{"mac":"aa:bb:..","port":"lan1",
//   "local":false,"age":12}, ...]}, ...]
//
// The table is read from /sys/class/net/<bridge>/brforward, which needs no
// extra packages (the `bridge` utility is only in ip-full). Each entry is a
// struct __fdb_entry (linux/if_bridge.h) of 16 bytes:
//   0-5   mac address
//   6     port_no (low byte)
//   7     is_local
//   8-11  ageing_timer_value (clock_t, 1/100 s, host endianness)
//   12    port_hi (high byte of the port number)
//   13-15 padding
// The port number is resolved to the member interface with brif/*/port_no.
//
// Only the fs module is used, which ucode always ships.

import * as fs from 'fs';

// The ageing timer is in host endianness. All supported targets are
// little-endian except the big-endian MIPS ones (ath79, ramips, lantiq...),
// which ucode doesn't expose, so detect it from the ELF header of ucode itself.
function is_big_endian() {
	let f = fs.open('/proc/self/exe', 'r');
	if (!f)
		return false;
	let hdr = f.read(6);
	f.close();
	return length(hdr) == 6 && ord(hdr, 5) == 2;
}

const big_endian = is_big_endian();

function u32(buf, off) {
	let b = map([0, 1, 2, 3], (i) => ord(buf, off + i));
	if (big_endian)
		return (b[0] << 24) | (b[1] << 16) | (b[2] << 8) | b[3];
	return (b[3] << 24) | (b[2] << 16) | (b[1] << 8) | b[0];
}

function bridge_ports(bridge) {
	let ports = {};
	for (let name in fs.lsdir(`/sys/class/net/${bridge}/brif`) || []) {
		let no = fs.readfile(`/sys/class/net/${bridge}/brif/${name}/port_no`);
		if (no != null)
			ports[hex(trim(no))] = name;
	}
	return ports;
}

function bridge_entries(bridge) {
	let ports = bridge_ports(bridge);
	let buf = fs.readfile(`/sys/class/net/${bridge}/brforward`);
	let entries = [];

	for (let off = 0; buf != null && off + 16 <= length(buf); off += 16) {
		let mac = join(':', map([0, 1, 2, 3, 4, 5], (i) => sprintf('%02x', ord(buf, off + i))));
		let port_no = ord(buf, off + 6) | (ord(buf, off + 12) << 8);
		let port = ports[port_no];

		if (port == null)
			continue;

		push(entries, {
			mac,
			port,
			local: ord(buf, off + 7) != 0,
			age: int(u32(buf, off + 8) / 100),
		});
	}

	return entries;
}

let bridges = [];
for (let name in fs.lsdir('/sys/class/net') || []) {
	if (fs.access(`/sys/class/net/${name}/bridge`))
		push(bridges, { bridge: name, entries: bridge_entries(name) });
}

printf('%J\n', bridges);
