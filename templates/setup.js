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

function renderAccessUrls(snapshot) {
    const el = document.getElementById("accessUrls");
    const ips = snapshot.management_ips || [];
    const forwarding = snapshot.routing?.ip_forwarding ? "on" : "off";
    const lan = document.getElementById("lanAddress")?.value;
    const links = [];
    const seen = new Set();
    const add = (ip) => {
        if (!ip || seen.has(ip)) return;
        seen.add(ip);
        links.push(`<a href="http://${ip}:5000/setup" target="_blank" rel="noopener">http://${ip}:5000/</a>`);
    };
    ips.forEach(add);
    add(lan);
    const list = links.length
        ? links.join("<br>")
        : "<span class='muted'>Device IP not detected yet. Connect Ethernet or WiFi WAN first.</span>";
    el.innerHTML = `<h3>Management access</h3><p>${list}</p><p class="muted small">IP forwarding: <strong>${forwarding}</strong> (enabled during setup)</p>`;
}

function renderNetworkSummary(snapshot) {
    const el = document.getElementById("networkSummary");
    const ifaces = usableInterfaces(snapshot);
    const lines = ifaces.map(i => `<div>${ifaceLabel(i)}</div>`).join("");
    const note = ifaces.length >= 3
        ? `<p class="muted small">Choose WAN and LAN from ${ifaces.length} interfaces. ${ifaces.length - 2} will stay unused.</p>`
        : "";
    el.innerHTML = `<h3>Detected interfaces</h3>${lines || "<p class='muted'>No usable interfaces found</p>"}${note}`;
    renderAccessUrls(snapshot);
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
        renderAccessUrls(lastSnapshot);
    });

    document.getElementById("lanAddress").addEventListener("input", () => {
        renderAccessUrls(lastSnapshot);
    });
}

function appendLogLine(text, cssClass = "") {
    const log = document.getElementById("setupLog");
    const panel = document.getElementById("setupLogPanel");
    panel.hidden = false;
    const span = document.createElement("span");
    if (cssClass) {
        span.className = cssClass;
    }
    span.textContent = text + "\n";
    log.appendChild(span);
    log.scrollTop = log.scrollHeight;
}

function formatLogEvent(evt) {
    const step = evt.step ? `[${evt.step}] ` : "";
    return `${step}${evt.detail || evt.status}`;
}

function appendAccessUrls(urls) {
    if (!urls || !urls.length) return;
    appendLogLine("Management URLs:", "log-warn");
    urls.forEach((url) => appendLogLine("  " + url, "log-warn"));
}

document.getElementById("setupForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    const btn = document.getElementById("applyBtn");
    const form = document.getElementById("setupForm");
    btn.disabled = true;
    btn.textContent = "Installing...";
    document.getElementById("setupLog").textContent = "";
    form.style.opacity = "0.55";

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

    if (!res.ok && !res.body) {
        appendLogLine(await res.text() || "Setup failed", "log-error");
        btn.disabled = false;
        btn.textContent = "Apply configuration";
        form.style.opacity = "1";
        return;
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    let failed = false;

    while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        const parts = buffer.split("\n\n");
        buffer = parts.pop() || "";
        for (const part of parts) {
            const line = part.split("\n").find(l => l.startsWith("data: "));
            if (!line) continue;
            let payload;
            try {
                payload = JSON.parse(line.slice(6));
            } catch {
                continue;
            }

            if (payload.status === "running") {
                appendLogLine(formatLogEvent(payload), "log-running");
            } else if (payload.status === "ok") {
                appendLogLine("✓ " + formatLogEvent(payload), "log-ok");
                if (payload.ip_forwarding === true) {
                    appendLogLine("  IP forwarding: on", "log-ok");
                }
            } else if (payload.status === "warn") {
                appendLogLine("! " + formatLogEvent(payload), "log-warn");
                appendAccessUrls(payload.access_urls);
            } else if (payload.status === "error") {
                appendLogLine("✗ " + (payload.detail || payload.step || "error"), "log-error");
                failed = true;
            } else if (payload.status === "done") {
                appendLogLine("✓ " + payload.detail, "log-done");
                if (payload.ip_forwarding === true) {
                    appendLogLine("  IP forwarding: on", "log-done");
                }
                appendAccessUrls(payload.access_urls);
                setTimeout(() => { window.location.href = "/login"; }, 8000);
                return;
            }
        }
    }

    if (failed) {
        btn.disabled = false;
        btn.textContent = "Apply configuration";
        form.style.opacity = "1";
    }
});

init().catch(err => {
    document.getElementById("networkSummary").innerHTML = `<p class="error">${err.message}</p>`;
});
