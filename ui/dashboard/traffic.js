import { loadChartJs, loader, loading, PALETTE } from "./charts.js";

// The traffic page: the live rates of the internet connection, the hosts and
// the wired ports, and their history (see /api/v1/traffic/live and
// /api/v1/traffic/history in traffic.go).

const LIVE_INTERVAL = 15000;
const HISTORY_INTERVAL = 60000;
const RANGE_STORAGE_KEY = "traffic_range";
const SHOW_ALL_STORAGE_KEY = "traffic_show_all";
const TOP_HOSTS = 15;

const DOWN_COLOR = PALETTE[0];
const UP_COLOR = PALETTE[1];

const RANGES = [
    { value: "1h", label: "Last hour" },
    { value: "24h", label: "Last 24 hours" },
    { value: "7d", label: "Last 7 days" },
    { value: "30d", label: "Last 30 days" },
];

function loadSetting(key, fallback) {
    try {
        return window.localStorage?.getItem(key) || fallback;
    } catch (_) {
        return fallback;
    }
}

function saveSetting(key, value) {
    try {
        window.localStorage?.setItem(key, value);
    } catch (_) {}
}

/**
 * Formats bits per second.
 *
 * @param  {number} bps
 * @return {string}
 */
export function formatBps(bps) {
    if (!bps || bps < 1) return "0";
    const units = ["bps", "kbps", "Mbps", "Gbps"];
    const i = Math.min(units.length - 1, Math.floor(Math.log10(bps) / 3));
    const value = bps / Math.pow(1000, i);
    return `${value >= 100 ? value.toFixed(0) : value.toFixed(1)} ${units[i]}`;
}

function formatTime(ts, rangeName) {
    const d = new Date(ts * 1000);
    if (rangeName == "1h" || rangeName == "24h") {
        return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    }
    return d.toLocaleString([], { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
}

/**
 * A download/upload line chart of one series.
 *
 * @return {Element} The canvas, with an update(points, rangeName, swap) method.
 *                   swap shows tx as the download (router ports).
 */
function trafficLineChart() {
    let chart;
    let pending = null;

    function apply() {
        if (!chart || !pending) return;
        const { points, rangeName, swap } = pending;
        chart.data.labels = points.map((p) => p.t);
        chart.data.datasets[0].data = points.map((p) => (swap ? p.tx : p.rx));
        chart.data.datasets[1].data = points.map((p) => (swap ? p.rx : p.tx));
        // a line of a few points (a fresh install, the 30 day view) needs its dots
        for (const dataset of chart.data.datasets) {
            dataset.pointRadius = points.length < 30 ? 3 : 0;
        }
        chart.options.scales.x.ticks.callback = function(value) {
            return formatTime(this.getLabelForValue(value), rangeName);
        };
        chart.options.plugins.tooltip.callbacks.title = (items) => formatTime(items[0].label, rangeName);
        chart.update("none");
    }

    const canvas = t.canvas({
        className: "traffic-canvas",
        onmount: (el) => {
            chart = new Chart(el, {
                type: "line",
                data: {
                    labels: [],
                    datasets: [
                        { label: "Download", data: [], borderColor: DOWN_COLOR, backgroundColor: DOWN_COLOR + "33", fill: true },
                        { label: "Upload", data: [], borderColor: UP_COLOR, backgroundColor: UP_COLOR + "33", fill: true },
                    ],
                },
                options: {
                    responsive: true,
                    maintainAspectRatio: false,
                    animation: false,
                    interaction: { mode: "index", intersect: false },
                    elements: { point: { radius: 0 }, line: { borderWidth: 1.5, tension: 0.2 } },
                    scales: {
                        x: { ticks: { color: "#617079", maxTicksLimit: 8, autoSkip: true }, grid: { display: false } },
                        y: {
                            beginAtZero: true,
                            // a sensible scale for an idle or empty chart
                            suggestedMax: 1e6,
                            ticks: { color: "#617079", callback: (v) => formatBps(v) },
                        },
                    },
                    plugins: {
                        legend: { position: "bottom", labels: { color: "#617079", boxWidth: 12 } },
                        tooltip: {
                            callbacks: {
                                label: (ctx) => ` ${ctx.dataset.label}: ${formatBps(ctx.parsed.y)}`,
                            },
                        },
                    },
                },
            });
            apply();
        },
        onunmount: () => {
            chart?.destroy();
            chart = null;
        },
    });

    canvas.update = (points, rangeName, swap = false) => {
        pending = { points, rangeName, swap };
        apply();
    };

    return canvas;
}

/**
 * A tiny download/upload chart of the last hour, as SVG.
 */
function sparkline(points, swap) {
    const width = 120;
    const height = 28;
    if (!points?.length) {
        return t.span({ className: "net-muted" }, "—");
    }

    const down = points.map((p) => (swap ? p.tx : p.rx));
    const up = points.map((p) => (swap ? p.rx : p.tx));
    const maxValue = Math.max(1, ...down, ...up);
    const first = points[0].t;
    const span = Math.max(60, points[points.length - 1].t - first);

    function path(values) {
        return values
            .map((v, i) => {
                const x = ((points[i].t - first) / span) * width;
                const y = height - (v / maxValue) * (height - 2) - 1;
                return `${i ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`;
            })
            .join(" ");
    }

    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
    svg.setAttribute("width", width);
    svg.setAttribute("height", height);
    svg.classList.add("traffic-spark");
    for (const [values, color] of [[up, UP_COLOR], [down, DOWN_COLOR]]) {
        const p = document.createElementNS("http://www.w3.org/2000/svg", "path");
        p.setAttribute("d", path(values));
        p.setAttribute("fill", "none");
        p.setAttribute("stroke", color);
        p.setAttribute("stroke-width", "1.5");
        svg.appendChild(p);
    }
    return svg;
}

function rateCell(down, up) {
    return [
        t.td({ className: "traffic-rate traffic-down" }, "↓ ", formatBps(down)),
        t.td({ className: "traffic-rate traffic-up" }, "↑ ", formatBps(up)),
    ];
}

/**
 * The bar of a port's load relative to its link speed ("1000F" etc).
 */
function loadBar(bps, speed) {
    const mbps = parseInt(speed, 10);
    if (!mbps) {
        return null;
    }
    const fraction = Math.min(1, bps / (mbps * 1e6));
    return t.span(
        { className: "traffic-bar", title: `${(fraction * 100).toFixed(1)}% of ${mbps} Mbps` },
        t.span({ style: `width:${Math.max(fraction * 100, bps > 0 ? 1 : 0)}%` }),
    );
}

export async function pageTraffic() {
    app.store.title = "Traffic";

    await loadChartJs();

    const state = store({
        isLoading: false,
        range: loadSetting(RANGE_STORAGE_KEY, "1h"),
        showAll: loadSetting(SHOW_ALL_STORAGE_KEY, "0") == "1",
        selected: null, // the selected host or port: { key, title, swap }
    });

    let live = { wan: [], clients: [], ports: [] };
    let sparks = {};

    const wanNow = t.div({ className: "traffic-now" });
    const wanChart = trafficLineChart();
    const hostsTable = t.div();
    const portsTable = t.div();
    const selectedTitle = t.span();
    const selectedChart = trafficLineChart();

    function select(key, title, swap) {
        state.selected = { key, title, swap };
        selectedTitle.textContent = title;
        loadSelected();
        selectedCard.scrollIntoView({ behavior: "smooth", block: "nearest" });
    }

    function renderWan() {
        if (!live.wan.length) {
            wanNow.replaceChildren(t.span({ className: "net-muted" }, "No internet traffic reported yet. The gateway needs nlbwmon and the OpenSOHO scripts."));
            return;
        }
        // the wan port receives what is downloaded
        const down = live.wan.reduce((s, p) => s + p.rx, 0);
        const up = live.wan.reduce((s, p) => s + p.tx, 0);
        wanNow.replaceChildren(
            t.div({ className: "traffic-big traffic-down" }, "↓ ", formatBps(down)),
            t.div({ className: "traffic-big traffic-up" }, "↑ ", formatBps(up)),
            t.div({ className: "net-muted" }, live.wan.map((p) => `${p.device} ${p.port}`).join(", ")),
        );
    }

    function renderHosts() {
        const hosts = state.showAll ? live.clients : live.clients.slice(0, TOP_HOSTS);
        if (!hosts.length) {
            hostsTable.replaceChildren(t.div({ className: "net-empty" }, "No traffic reported yet."));
            return;
        }

        hostsTable.replaceChildren(t.div(
            { className: "net-scroll" },
            t.table(
                { className: "net-table traffic-table" },
                t.thead(
                    null,
                    t.tr(
                        null,
                        t.th(null, "Host"),
                        t.th(null, "Where"),
                        t.th({ colSpan: 2, title: "All traffic: of the wifi station, or of the port when the host is alone on it" }, "Total"),
                        t.th({ colSpan: 2, title: "Internet traffic, measured on the gateway" }, "Internet"),
                        t.th(null, "Last hour"),
                    ),
                ),
                t.tbody(null, hosts.map((h) => {
                    const key = h.key || h.internetKey;
                    const name = h.name || h.mac;
                    return t.tr(
                        {
                            className: "traffic-row",
                            onclick: () => select(key, `${name}${h.key ? "" : " (internet)"}`, false),
                        },
                        t.td(
                            null,
                            t.div({ className: "traffic-name" }, name, h.router ? t.span({ className: "label sm" }, "router") : null),
                            t.div({ className: "net-muted traffic-sub" }, [h.ip, h.name ? h.mac : null, h.vendor].filter(Boolean).join(" · ")),
                        ),
                        t.td(
                            { className: "net-muted" },
                            h.device ? `${h.device}` : "—",
                            h.where ? t.div({ className: "traffic-sub" }, (h.wired ? "" : "wifi ") + h.where) : null,
                        ),
                        h.key ? rateCell(h.rx, h.tx) : [t.td({ className: "net-muted" }, "—"), t.td()],
                        h.internetKey ? rateCell(h.internetRx, h.internetTx) : [t.td({ className: "net-muted" }, "—"), t.td()],
                        t.td(null, sparkline(sparks[key], key.startsWith("port|"))),
                    );
                })),
            ),
        ));
    }

    function renderPorts() {
        // ports without a link, unless they still carried traffic
        const ports = live.ports.filter((p) => p.speed || p.rx + p.tx > 0);
        if (!ports.length) {
            portsTable.replaceChildren(t.div({ className: "net-empty" }, "No port traffic reported yet."));
            return;
        }
        portsTable.replaceChildren(t.div(
            { className: "net-scroll" },
            t.table(
                { className: "net-table traffic-table" },
                t.thead(
                    null,
                    t.tr(
                        null,
                        t.th(null, "Device"),
                        t.th(null, "Port"),
                        t.th(null, "Link"),
                        t.th({ title: "Transmitted by the router on this port" }, "Out"),
                        t.th({ title: "Received by the router on this port" }, "In"),
                        t.th(null, "Last hour"),
                    ),
                ),
                t.tbody(null, ports.map((p) => t.tr(
                    {
                        className: "traffic-row",
                        onclick: () => select(p.key, `${p.device} ${p.port}`, !p.wan),
                    },
                    t.td(null, p.device),
                    t.td({ className: "net-port" }, p.port, p.wan ? t.span({ className: "label sm" }, "WAN") : null),
                    t.td({ className: "net-muted" }, p.speed || "—"),
                    t.td({ className: "traffic-rate traffic-down" }, formatBps(p.tx), loadBar(p.tx, p.speed)),
                    t.td({ className: "traffic-rate traffic-up" }, formatBps(p.rx), loadBar(p.rx, p.speed)),
                    t.td(null, sparkline(sparks[p.key], true)),
                ))),
            ),
        ));
    }

    async function loadLive() {
        await loading(state, async () => {
            live = await app.pb.send("/api/v1/traffic/live", { method: "GET", requestKey: "traffic_live" });
            renderWan();
            renderHosts();
            renderPorts();
        });
    }

    async function history(keys, rangeName, requestKey) {
        if (!keys.length) return {};
        return app.pb.send("/api/v1/traffic/history", {
            method: "GET",
            query: { keys: keys.join(","), range: rangeName },
            requestKey,
        });
    }

    async function loadWanHistory() {
        const key = live.wan[0]?.key;
        if (!key) return;
        const res = await history([key], state.range, "traffic_wan");
        wanChart.update(res[key] || [], state.range, false);
    }

    async function loadSparks() {
        const hosts = state.showAll ? live.clients : live.clients.slice(0, TOP_HOSTS);
        const keys = [
            ...hosts.map((h) => h.key || h.internetKey),
            ...live.ports.map((p) => p.key),
        ].filter(Boolean).slice(0, 100);
        sparks = await history(keys, "1h", "traffic_sparks");
        renderHosts();
        renderPorts();
    }

    async function loadSelected() {
        if (!state.selected) return;
        const { key, swap } = state.selected;
        const res = await history([key], state.range, "traffic_selected");
        selectedChart.update(res[key] || [], state.range, swap);
    }

    async function loadHistory() {
        try {
            await Promise.all([loadWanHistory(), loadSparks(), loadSelected()]);
        } catch (err) {
            if (!err?.isAbort) {
                app.checkApiError(err);
            }
        }
    }

    async function refreshAll() {
        await loadLive();
        await loadHistory();
    }

    const rangeSelect = t.select(
        {
            onchange: (e) => {
                state.range = e.target.value;
                saveSetting(RANGE_STORAGE_KEY, state.range);
                loadWanHistory();
                loadSelected();
            },
        },
        RANGES.map((r) => t.option({ value: r.value, selected: r.value == state.range }, r.label)),
    );

    const showAllId = "traffic_show_all_" + app.utils.randomString();
    const showAllToggle = t.div(
        { className: "field" },
        t.input({
            type: "checkbox",
            className: "switch sm",
            id: showAllId,
            checked: () => state.showAll,
            onchange: (e) => {
                state.showAll = e.target.checked;
                saveSetting(SHOW_ALL_STORAGE_KEY, state.showAll ? "1" : "0");
                renderHosts();
                loadSparks();
            },
        }),
        t.label({ htmlFor: showAllId }, "Show all hosts"),
    );

    function card(title, body, headerExtra = null) {
        return t.div(
            { className: "dashboard-card wide" },
            t.div({ className: "card-header" }, t.h6({ className: "card-title" }, title), headerExtra),
            t.div({ className: "card-body" }, body),
        );
    }

    const selectedCard = t.div(
        { className: "dashboard-card wide", hidden: () => !state.selected },
        t.div(
            { className: "card-header" },
            t.h6({ className: "card-title" }, selectedTitle),
            t.button(
                {
                    type: "button",
                    className: "btn sm secondary transparent circle",
                    title: "Close",
                    onclick: () => (state.selected = null),
                },
                t.i({ className: "ri-close-line", ariaHidden: true }),
            ),
        ),
        t.div({ className: "card-body" }, t.div({ className: "traffic-chart" }, selectedChart)),
    );

    let liveInterval;
    let historyInterval;

    return t.div(
        {
            pbEvent: "pageTraffic",
            className: "page page-dashboard page-traffic",
            onmount: () => {
                refreshAll();
                liveInterval = setInterval(loadLive, LIVE_INTERVAL);
                historyInterval = setInterval(loadHistory, HISTORY_INTERVAL);
            },
            onunmount: () => {
                clearInterval(liveInterval);
                clearInterval(historyInterval);
            },
        },
        t.div(
            { className: "page-content full-height" },
            t.header(
                { className: "page-header" },
                t.nav({ className: "breadcrumbs" }, t.div(null, "Traffic")),
                t.div(
                    { className: "inline-flex gap-sm" },
                    t.label({ className: "inline-select" }, t.span(null, "Charts"), rangeSelect),
                    app.components.refreshButton({ onclick: refreshAll }),
                ),
            ),
            t.div(
                { className: "dashboard-grid" },
                card("Internet", t.div(
                    { className: "traffic-wan" },
                    loader(state),
                    wanNow,
                    t.div({ className: "traffic-chart" }, wanChart),
                )),
                selectedCard,
                card("Hosts", hostsTable, showAllToggle),
                card("Ports", portsTable),
            ),
            t.footer({ className: "page-footer" }, app.components.credits()),
        ),
    );
}
