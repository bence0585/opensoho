// The traffic map: the tree of /api/v1/traffic/topology drawn left to right,
// the links as SVG curves as thick as their traffic, the nodes as HTML on top.

// the columns fill the width of the card, but no narrower than this
const MIN_COLUMN_WIDTH = 250;
const ROW_HEIGHT = 34;
const NODE_WIDTH = 220;
const NODE_HEIGHT = 28;
const PADDING = 12;
// below this a host counts as idle (both directions together)
const IDLE_BPS = 2000;

const ICONS = {
    internet: "ri-cloud-line",
    router: "ri-router-line",
    switch: "ri-git-merge-line",
    host: "ri-computer-line",
    wifi: "ri-wifi-line",
    idle: "ri-more-line",
};

function total(n) {
    return (n.down || 0) + (n.up || 0);
}

function linkWidth(bps) {
    return Math.max(1.5, Math.min(10, 1.5 + 2.2 * Math.log10(1 + bps / 1e5)));
}

// faster flow for more traffic, no flow for (almost) none
function flowDuration(bps) {
    if (bps < 20e3) return 0;
    return Math.max(0.35, Math.min(3, 3 - 0.9 * Math.log10(bps / 2e4)));
}

/**
 * Builds the tree, the idle hosts of a parent grouped into one node.
 */
function buildTree(nodes, showIdle) {
    const byId = new Map(nodes.map((n) => [n.id, { ...n, children: [] }]));
    let root = null;
    for (const n of byId.values()) {
        const parent = byId.get(n.parent);
        if (parent) {
            parent.children.push(n);
        } else if (n.type == "internet" || !root) {
            root = n;
        }
    }

    function order(n) {
        const infra = n.children.filter((c) => c.type != "host");
        let hosts = n.children.filter((c) => c.type == "host").sort((a, b) => total(b) - total(a));
        if (!showIdle) {
            const idle = hosts.filter((h) => total(h) < IDLE_BPS);
            hosts = hosts.filter((h) => total(h) >= IDLE_BPS);
            if (idle.length) {
                hosts.push({
                    id: n.id + ":idle",
                    type: "idle",
                    name: `+${idle.length} idle`,
                    sub: idle.slice(0, 3).map((h) => h.name).join(", ") + (idle.length > 3 ? ", …" : ""),
                    down: idle.reduce((s, h) => s + (h.down || 0), 0),
                    up: idle.reduce((s, h) => s + (h.up || 0), 0),
                    approx: true,
                    children: [],
                });
            }
        }
        n.children = [...infra, ...hosts];
        n.children.forEach(order);
    }
    if (root) order(root);
    return root;
}

/**
 * Assigns x/y: the depth is the column, the leaves get the next row and a
 * parent sits between its first and last child.
 */
function layout(root, availableWidth) {
    let row = 0;
    const all = [];
    function depthOf(n) {
        return n.children.length ? 1 + Math.max(...n.children.map(depthOf)) : 0;
    }
    const maxDepth = depthOf(root);
    const columnWidth = Math.max(MIN_COLUMN_WIDTH, (availableWidth - 2 * PADDING - NODE_WIDTH) / Math.max(1, maxDepth));
    function place(n, depth) {
        n.depth = depth;
        all.push(n);
        if (!n.children.length) {
            n.y = PADDING + row * ROW_HEIGHT;
            row++;
        } else {
            n.children.forEach((c) => place(c, depth + 1));
            n.y = (n.children[0].y + n.children[n.children.length - 1].y) / 2;
        }
        n.x = PADDING + depth * columnWidth;
    }
    place(root, 0);
    return {
        nodes: all,
        width: PADDING * 2 + maxDepth * columnWidth + NODE_WIDTH,
        height: PADDING * 2 + Math.max(1, row) * ROW_HEIGHT,
    };
}

const SVG_NS = "http://www.w3.org/2000/svg";

function svgEl(name, attrs) {
    const el = document.createElementNS(SVG_NS, name);
    for (const [k, v] of Object.entries(attrs)) {
        el.setAttribute(k, v);
    }
    return el;
}

/**
 * @param  {Function} onselect Called with (key, title, swap) for a clicked node.
 * @param  {Function} formatBps
 * @return {{el: Element, update: Function, setShowIdle: Function}}
 */
export function trafficMap(onselect, formatBps) {
    let showIdle = false;
    let lastNodes = [];

    const canvas = t.div({ className: "traffic-map-canvas" });
    const el = t.div({ className: "traffic-map" }, canvas);

    function render() {
        const root = buildTree(lastNodes, showIdle);
        if (!root) {
            canvas.replaceChildren(t.div({ className: "net-empty" }, "No topology known yet."));
            return;
        }
        const { nodes, width, height } = layout(root, el.clientWidth || 1200);
        canvas.style.width = width + "px";
        canvas.style.height = height + "px";

        const svg = svgEl("svg", { width, height, class: "traffic-map-links" });
        const labels = [];
        for (const n of nodes) {
            for (const c of n.children) {
                const x1 = n.x + NODE_WIDTH;
                const y1 = n.y + NODE_HEIGHT / 2;
                const x2 = c.x;
                const y2 = c.y + NODE_HEIGHT / 2;
                const mid = (x1 + x2) / 2;
                const d = `M${x1},${y1} C${mid},${y1} ${mid},${y2} ${x2},${y2}`;
                const bps = total(c);
                svg.appendChild(svgEl("path", {
                    d,
                    class: `traffic-link${c.approx ? " approx" : ""}${bps < IDLE_BPS ? " idle" : ""}`,
                    "stroke-width": linkWidth(bps),
                }));
                const duration = flowDuration(bps);
                if (duration) {
                    svg.appendChild(svgEl("path", {
                        d,
                        class: "traffic-flow",
                        "stroke-width": Math.max(1, linkWidth(bps) / 3),
                        style: `animation-duration:${duration.toFixed(2)}s`,
                    }));
                }
                // the port of the link, just before the child
                if (c.port && c.type != "idle") {
                    labels.push(t.div(
                        { className: "traffic-map-port", style: `left:${x2 - 74}px;top:${y2 - 14}px` },
                        c.wifi ? [t.i({ className: ICONS.wifi, ariaHidden: true }), " ", c.port] : c.port,
                    ));
                }
            }
        }

        const boxes = nodes.map((n) => {
            const icon = n.type == "host" && n.wifi ? ICONS.wifi : ICONS[n.type] || ICONS.host;
            const clickable = n.key || n.type == "idle";
            return t.div(
                {
                    className: `traffic-map-node type-${n.type}${clickable ? " clickable" : ""}`,
                    style: `left:${n.x}px;top:${n.y}px;width:${NODE_WIDTH}px;height:${NODE_HEIGHT}px`,
                    title: [n.name, n.sub, n.approx && n.type != "idle" ? "only the internet traffic is known" : ""]
                        .filter(Boolean)
                        .join("\n"),
                    onclick: () => {
                        if (n.type == "idle") {
                            setShowIdle(true);
                        } else if (n.key) {
                            onselect(n.key, `${n.name}${n.approx ? " (internet)" : ""}`, n.swap);
                        }
                    },
                },
                t.i({ className: icon, ariaHidden: true }),
                t.div(
                    { className: "traffic-map-text" },
                    t.div({ className: "traffic-map-name" }, n.name),
                    n.sub ? t.div({ className: "traffic-map-sub" }, n.sub) : null,
                ),
                // the traffic of the link to the parent
                n.type != "internet"
                    ? t.div(
                        { className: `traffic-map-rate${total(n) < IDLE_BPS ? " idle" : ""}` },
                        t.div({ className: "traffic-down" }, "↓" + formatBps(n.down)),
                        t.div({ className: "traffic-up" }, "↑" + formatBps(n.up)),
                    )
                    : null,
            );
        });

        canvas.replaceChildren(svg, ...labels, ...boxes);
    }

    function update(nodes) {
        lastNodes = nodes || [];
        render();
    }

    function setShowIdle(value) {
        showIdle = value;
        el.dispatchEvent(new CustomEvent("showidle", { detail: value }));
        render();
    }

    return { el, update, setShowIdle };
}
