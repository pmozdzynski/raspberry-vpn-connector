let lastSnapshot = null;

async function loadStatus(wan) {
    const url = wan ? `/setup/status?wan=${encodeURIComponent(wan)}` : "/setup/status";
    const res = await fetch(url);
    if (!res.ok) throw new Error("Failed to load network status");
    return res.json();
}

function kindLabel(kind) {
    if (kind === "wireless") return "WiFi";
    if (kind === "ethernet") return "Ethernet";
    return kind || "other";
}

function ifaceLabel(i) {
    const ips = (i.ipv4 || []).join(", ") || "no IPv4";
    const route = i.is_default_route ? " [default route]" : "";
    return `${i.name} (${kindLabel(i.kind)}, ${i.state}) ${ips}${route}`;
}

function usableInterfaces(snapshot) {
    return (snapshot.interfaces || []).filter(i => i.kind === "wireless" || i.kind === "ethernet");
}

function renderNetworkSummary(snapshot) {
    const el = document.getElementById("networkSummary");
    const ifaces = usableInterfaces(snapshot);
    const unused = (snapshot.interfaces || []).length - ifaces.length;
    const lines = ifaces.map(i => `<div>${ifaceLabel(i)}</div>`).join("");
    const note = ifaces.length >= 3
        ? `<p class="muted small">Choose WAN and LAN from ${ifaces.length} interfaces. ${ifaces.length - 2} will stay unused.</p>`
        : "";
    el.innerHTML = `<h3>Detected interfaces</h3>${lines || "<p class='muted'>No usable interfaces found</p>"}${note}`;
}

function fillInterfaceSelects(snapshot, keepWan, keepLan) {
    const wan = document.getElementById("wanInterface");
    const lan = document.getElementById("lanInterface");
    const ifaces = usableInterfaces(snapshot);
    const defaultWan = snapshot.routing.default_interface || "";

    wan.innerHTML = ifaces.map(i =>
        `<option value="${i.name}">${ifaceLabel(i)}</option>`
    ).join("");
    if (keepWan && [...wan.options].some(o => o.value === keepWan)) {
        wan.value = keepWan;
    } else if (defaultWan && [...wan.options].some(o => o.value === defaultWan)) {
        wan.value = defaultWan;
    }

    const lanCandidates = ifaces.filter(i => i.name !== wan.value);
    lan.innerHTML = lanCandidates.map(i =>
        `<option value="${i.name}">${ifaceLabel(i)}</option>`
    ).join("");
    if (keepLan && [...lan.options].some(o => o.value === keepLan)) {
        lan.value = keepLan;
    }
    updateRoleHints(snapshot);
    updateLANMode(snapshot);
}

function updateRoleHints(snapshot) {
    const ifaces = snapshot.interfaces || [];
    const wanName = document.getElementById("wanInterface").value;
    const lanName = document.getElementById("lanInterface").value;
    const wan = ifaces.find(i => i.name === wanName);
    const lan = ifaces.find(i => i.name === lanName);

    const wanHint = document.getElementById("wanHint");
    const lanHint = document.getElementById("lanHint");

    if (wan?.kind === "wireless") {
        wanHint.textContent = "WiFi WAN: connect this adapter to your home/office WiFi before routing (wpa_supplicant / dhcpcd). Setup does not change it.";
    } else if (wan?.kind === "ethernet") {
        wanHint.textContent = "Ethernet WAN: plug in the cable. Setup leaves DHCP as-is.";
    } else {
        wanHint.textContent = "";
    }

    if (lan?.kind === "wireless") {
        lanHint.textContent = "WiFi LAN: this adapter becomes an access point for your devices.";
    } else if (lan?.kind === "ethernet") {
        lanHint.textContent = "Ethernet LAN: plug a switch or PC into this port. No WiFi AP is configured.";
    } else {
        lanHint.textContent = "";
    }
}

function updateLANMode(snapshot) {
    const lanName = document.getElementById("lanInterface").value;
    const lan = (snapshot.interfaces || []).find(i => i.name === lanName);
    const isWifi = lan?.kind === "wireless";
    document.getElementById("wifiApSection").style.display = isWifi ? "block" : "none";
}

function applySuggested(snapshot) {
    const s = snapshot.suggested_lan || {};
    document.getElementById("lanAddress").value = s.address || "192.168.50.1";
    document.getElementById("lanPrefix").value = s.prefix || 24;
    document.getElementById("dhcpStart").value = s.dhcp_start || "192.168.50.100";
    document.getElementById("dhcpEnd").value = s.dhcp_end || "192.168.50.200";
}

async function init() {
    lastSnapshot = await loadStatus();
    renderNetworkSummary(lastSnapshot);
    fillInterfaceSelects(lastSnapshot);
    applySuggested(lastSnapshot);

    document.getElementById("wanInterface").addEventListener("change", async (e) => {
        const wan = e.target.value;
        const lan = document.getElementById("lanInterface").value;
        lastSnapshot = await loadStatus(wan);
        renderNetworkSummary(lastSnapshot);
        fillInterfaceSelects(lastSnapshot, wan, lan);
        applySuggested(lastSnapshot);
    });

    document.getElementById("lanInterface").addEventListener("change", () => {
        updateRoleHints(lastSnapshot);
        updateLANMode(lastSnapshot);
    });
}

function appendProgress(status, step, detail) {
    const log = document.getElementById("progressLog");
    const line = document.createElement("div");
    line.className = "progress-line " + status;
    line.textContent = [step, detail].filter(Boolean).join(": ");
    log.appendChild(line);
    log.scrollTop = log.scrollHeight;
}

document.getElementById("setupForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    const btn = document.getElementById("applyBtn");
    btn.disabled = true;
    document.getElementById("progressLog").innerHTML = "";

    const body = {
        wan_interface: document.getElementById("wanInterface").value,
        lan_interface: document.getElementById("lanInterface").value,
        lan_address: document.getElementById("lanAddress").value,
        lan_prefix: parseInt(document.getElementById("lanPrefix").value, 10),
        dhcp_range_start: document.getElementById("dhcpStart").value,
        dhcp_range_end: document.getElementById("dhcpEnd").value,
        ap_ssid: document.getElementById("apSSID").value,
        ap_password: document.getElementById("apPassword").value,
        wifi_country: document.getElementById("wifiCountry").value,
        admin_username: document.getElementById("adminUser").value,
        admin_password: document.getElementById("adminPass").value
    };

    const res = await fetch("/setup/apply?stream=1", {
        method: "POST",
        headers: {
            "Content-Type": "application/json",
            "Accept": "text/event-stream"
        },
        body: JSON.stringify(body)
    });

    if (!res.ok) {
        appendProgress("error", "", await res.text());
        btn.disabled = false;
        return;
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";

    while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        const parts = buffer.split("\n\n");
        buffer = parts.pop();
        for (const part of parts) {
            const line = part.split("\n").find(l => l.startsWith("data: "));
            if (!line) continue;
            const payload = JSON.parse(line.slice(6));
            appendProgress(payload.status, payload.step, payload.detail);
            if (payload.status === "done") {
                setTimeout(() => { window.location.href = "/login"; }, 1500);
            }
            if (payload.status === "error") {
                btn.disabled = false;
            }
        }
    }
});

init().catch(err => {
    document.getElementById("networkSummary").innerHTML = `<p class="error">${err.message}</p>`;
});
