let editingProfile = null;
let connectProfileId = null;
let pollTimer = null;
let lastVPNPhase = null;
let statusFailCount = 0;

function showInfo(message, type = "info") {
    const box = document.getElementById("infoBox");
    box.textContent = message;
    box.className = "info-box show " + type;
    setTimeout(() => box.classList.remove("show"), 5000);
}

async function fetchStatus() {
    const res = await fetch("/status");
    if (!res.ok) throw new Error("Failed to load status");
    return res.json();
}

function phaseLabel(phase) {
    switch (phase) {
        case "connected": return "Connected";
        case "connecting": return "Connecting...";
        case "need_input": return "Token required";
        case "error": return "Error";
        default: return "Disconnected";
    }
}

function logNeedsToken(logTail) {
    const text = logTail || "";
    const lower = text.toLowerCase();
    return lower.includes("fortitoken")
        || lower.includes("token code")
        || lower.includes("enter token")
        || lower.includes("enter otp")
        || lower.includes("one-time password")
        || /(^|\n)\s*code:\s*$/im.test(text);
}

function showTokenPanel(prompt) {
    closeConnectModal();
    const panel = document.getElementById("tokenPanel");
    document.getElementById("tokenPrompt").textContent =
        prompt || "VPN server is waiting for a one-time token.";
    panel.classList.remove("hidden");
    document.getElementById("vpnToken").focus();
}

function hideTokenPanel() {
    document.getElementById("tokenPanel").classList.add("hidden");
}

function vpnWaitingForToken(vpn, logTail, waitingFlag) {
    if (vpn.phase === "connected") return false;
    return waitingFlag
        || vpn.phase === "need_input"
        || logNeedsToken(logTail);
}

function renderVPN(vpn, logTail, waitingFlag) {
    const state = document.getElementById("vpnState");
    const details = document.getElementById("vpnDetails");
    const disconnectBtn = document.getElementById("disconnectBtn");
    const reconnectBtn = document.getElementById("reconnectBtn");

    const waitingForToken = vpnWaitingForToken(vpn, logTail, waitingFlag);

    state.textContent = waitingForToken ? "Token required" : phaseLabel(vpn.phase);
    state.className = vpn.phase === "connected" ? "ok" : (vpn.phase === "error" && !waitingForToken ? "error-text" : "muted");

    if (vpn.phase === "connected") {
        details.textContent = `${vpn.profile_name || vpn.profile_id} via ${vpn.tun_iface || "tun"} since ${vpn.since || "?"}`;
        disconnectBtn.disabled = false;
        hideTokenPanel();
    } else if (waitingForToken) {
        details.textContent = "Enter your one-time token or OTP code below.";
        disconnectBtn.disabled = false;
        showTokenPanel(vpn.input_prompt);
    } else if (vpn.phase === "connecting") {
        details.textContent = `Connecting to ${vpn.profile_name || vpn.profile_id || "VPN"}...`;
        disconnectBtn.disabled = false;
        hideTokenPanel();
    } else if (vpn.phase === "error") {
        details.textContent = vpn.last_error || "Connection failed";
        disconnectBtn.disabled = true;
        hideTokenPanel();
    } else {
        details.textContent = vpn.last_error || "LAN uses direct WAN NAT";
        disconnectBtn.disabled = true;
        hideTokenPanel();
    }

    reconnectBtn.disabled = vpn.phase === "connecting" || waitingForToken;
}

function renderProfiles(profiles, vpn, waitingFlag, logTail) {
    const list = document.getElementById("profilesList");
    if (!profiles.length) {
        list.innerHTML = "<p class='muted'>No profiles yet. Add one below.</p>";
        return;
    }

    const busy = vpn.phase === "connecting" || vpnWaitingForToken(vpn, logTail, waitingFlag);

    list.innerHTML = profiles.map(p => {
        const active = vpn.connected && vpn.profile_id === p.id;
        return `
            <div class="profile-card ${active ? "active" : ""}">
                <div>
                    <strong>${escapeHtml(p.name)}</strong>
                    <div class="muted small">${escapeHtml(p.protocol || "fortinet")} · ${escapeHtml(p.username)} @ ${escapeHtml(p.server_url)}</div>
                </div>
                <div class="button-row compact">
                    <button class="primary connect-btn" data-id="${p.id}" data-name="${escapeHtml(p.name)}" ${busy ? "disabled" : ""}>Connect</button>
                    <button class="secondary" onclick="editProfile('${p.id}')">Edit</button>
                    <button class="danger" onclick="deleteProfile('${p.id}')" ${busy ? "disabled" : ""}>Delete</button>
                </div>
            </div>
        `;
    }).join("");

    document.querySelectorAll(".connect-btn").forEach(btn => {
        btn.addEventListener("click", () => openConnectModal(btn.dataset.id, btn.dataset.name));
    });
}

function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, c => ({
        "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
    })[c]);
}

function schedulePoll(fast) {
    if (pollTimer) clearInterval(pollTimer);
    pollTimer = setInterval(refresh, fast ? 1000 : 10000);
}

function renderTailscale(ts) {
    const state = document.getElementById("tailscaleState");
    const details = document.getElementById("tailscaleDetails");
    const dnsHint = document.getElementById("tailscaleDNSHint");
    const checkbox = document.getElementById("tailscaleExitNode");
    const saveBtn = document.getElementById("tailscaleSaveBtn");

    if (!ts) {
        state.textContent = "Unavailable";
        details.textContent = "";
        dnsHint.textContent = "";
        checkbox.disabled = true;
        saveBtn.disabled = true;
        return;
    }

    checkbox.checked = !!ts.exit_node_enabled;
    checkbox.disabled = !ts.installed;
    saveBtn.disabled = !ts.installed;

    if (!ts.installed) {
        state.textContent = "Tailscale not installed";
        details.textContent = "Install tailscale and run tailscale up on the device first.";
        dnsHint.textContent = "";
        return;
    }

    const mode = ts.vpn_connected ? "VPN connected (split-tunnel)" : "VPN disconnected (WAN only, corp best-effort)";
    const advertised = ts.advertised ? "advertised" : "not advertised yet (approve in Tailscale admin if needed)";
    state.textContent = ts.running
        ? `Running — exit node ${ts.exit_node_enabled ? advertised : "disabled"} — ${mode}`
        : "Installed but not running (run: tailscale up)";

    const parts = [];
    if (ts.ipv4) parts.push(`Tailscale IP: ${ts.ipv4}`);
    if (ts.hostname) parts.push(`DNS: ${ts.hostname}`);
    details.textContent = parts.join(" · ");

    if (ts.exit_node_enabled && ts.ipv4 && (ts.corp_dns_domains || []).length) {
        dnsHint.textContent = `Configure Tailnet split DNS in the Tailscale admin console: domains ${ts.corp_dns_domains.join(", ")} → nameserver ${ts.ipv4}`;
    } else if (ts.exit_node_enabled && ts.ipv4) {
        dnsHint.textContent = `When VPN connects, configure Tailnet split DNS to ${ts.ipv4} for corporate domains.`;
    } else {
        dnsHint.textContent = "";
    }
}

async function saveTailscaleSetting() {
    const enabled = document.getElementById("tailscaleExitNode").checked;
    const res = await fetch("/api/tailscale/exit-node", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled })
    });
    if (!res.ok) {
        showInfo(await res.text(), "error");
        return;
    }
    showInfo(enabled ? "Tailscale filtered exit node enabled" : "Tailscale exit node disabled", "ok");
    refresh();
}

function renderWiFiAP(network) {
    const el = document.getElementById("wifiApStatus");
    const mgmt = document.getElementById("mgmtHint");
    const ap = network?.wifi_ap;
    const ips = (network?.management_ips || []).map(ip => `https://${ip}:5000/`).join(" · ");
    mgmt.textContent = ips
        ? `Management (stays on WAN): ${ips} — also use LAN gateway after VPN is up`
        : "Use LAN gateway IP for dashboard if WAN URL stops responding during VPN connect";
    if (!ap?.enabled) {
        el.textContent = "";
        return;
    }
    const parts = [
        `LAN WiFi AP: ${ap.ssid || "?"}`,
        ap.beaconing ? "beaconing" : "not beaconing",
        ap.hostapd_active ? "hostapd up" : "hostapd down"
    ];
    el.textContent = parts.join(" · ");
    if (!ap.beaconing) {
        el.textContent += ". Check country code, USB WiFi driver, and: systemctl status hostapd";
    }
}

async function refresh() {
    try {
        const data = await fetchStatus();
        statusFailCount = 0;
        const prevPhase = lastVPNPhase;
        lastVPNPhase = data.vpn.phase;
        const logTail = data.log_tail || "";
        const waiting = !!data.waiting_for_token;
        renderVPN(data.vpn, logTail, waiting);
        renderTailscale(data.tailscale);
        renderWiFiAP(data.network);
        renderProfiles(data.profiles || [], data.vpn, waiting, logTail);
        document.getElementById("logTail").textContent = logTail;

        const active = data.vpn.phase === "connecting"
            || data.vpn.phase === "need_input"
            || waiting
            || logNeedsToken(logTail);
        schedulePoll(active);

        if (data.vpn.phase === "connected" && prevPhase !== "connected") {
            showInfo("VPN connected via " + (data.vpn.tun_iface || "tunnel"), "ok");
        }
    } catch (err) {
        statusFailCount += 1;
        if (statusFailCount >= 3) {
            showInfo(err.message, "error");
        }
    }
}

function openConnectModal(id, name) {
    connectProfileId = id;
    document.getElementById("connectProfileName").textContent = name;
    document.getElementById("connectPassword").value = "";
    document.getElementById("connectModal").classList.remove("hidden");
    document.getElementById("connectPassword").focus();
}

function closeConnectModal() {
    connectProfileId = null;
    document.getElementById("connectModal").classList.add("hidden");
}

async function startConnect(profileId, password) {
    const res = await fetch("/api/vpn/connect", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ profile_id: profileId, password: password || "" })
    });
    if (!res.ok) {
        showInfo(await res.text(), "error");
        return;
    }
    closeConnectModal();
    showInfo("Connecting... enter FortiToken when the field appears", "info");
    refresh();
}

async function submitToken() {
    const token = document.getElementById("vpnToken").value.trim();
    const res = await fetch("/api/vpn/input", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token })
    });
    if (!res.ok) {
        showInfo(await res.text(), "error");
        return;
    }
    document.getElementById("vpnToken").value = "";
    hideTokenPanel();
    showInfo("Token sent", "ok");
    refresh();
}

async function deleteProfile(id) {
    if (!confirm("Delete this profile?")) return;
    const res = await fetch("/api/profiles?id=" + encodeURIComponent(id), { method: "DELETE" });
    if (!res.ok) {
        showInfo(await res.text(), "error");
        return;
    }
    refresh();
}

function editProfile(id) {
    fetch("/status").then(r => r.json()).then(data => {
        const p = (data.profiles || []).find(x => x.id === id);
        if (!p) return;
        editingProfile = p;
        document.getElementById("formTitle").textContent = "Edit Profile";
        document.getElementById("profileId").value = p.id;
        document.getElementById("profileProtocol").value = p.protocol || "fortinet";
        document.getElementById("profileName").value = p.name;
        document.getElementById("profileUser").value = p.username;
        document.getElementById("profileURL").value = p.server_url;
        document.getElementById("profilePin").value = p.servercert_pin;
        document.getElementById("savePassword").checked = p.save_password;
        document.getElementById("noDTLS").checked = !!p.no_dtls;
        document.getElementById("profilePassword").value = "";
    });
}

function clearForm() {
    editingProfile = null;
    document.getElementById("formTitle").textContent = "Add Profile";
    document.getElementById("profileForm").reset();
    document.getElementById("profileId").value = "";
}

document.getElementById("profileForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    const body = {
        id: document.getElementById("profileId").value,
        name: document.getElementById("profileName").value,
        protocol: document.getElementById("profileProtocol").value,
        username: document.getElementById("profileUser").value,
        server_url: document.getElementById("profileURL").value,
        servercert_pin: document.getElementById("profilePin").value,
        save_password: document.getElementById("savePassword").checked,
        no_dtls: document.getElementById("noDTLS").checked,
        password: document.getElementById("profilePassword").value
    };
    const res = await fetch("/api/profiles", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body)
    });
    if (!res.ok) {
        showInfo(await res.text(), "error");
        return;
    }
    showInfo("Profile saved", "ok");
    clearForm();
    refresh();
});

document.getElementById("clearFormBtn").addEventListener("click", clearForm);
document.getElementById("connectConfirmBtn").addEventListener("click", () => {
    if (!connectProfileId) return;
    startConnect(connectProfileId, document.getElementById("connectPassword").value);
});
document.getElementById("connectCancelBtn").addEventListener("click", closeConnectModal);
document.getElementById("submitTokenBtn").addEventListener("click", submitToken);
document.getElementById("vpnToken").addEventListener("keydown", (e) => {
    if (e.key === "Enter") submitToken();
});

document.getElementById("disconnectBtn").addEventListener("click", async () => {
    const res = await fetch("/api/vpn/disconnect", { method: "POST" });
    if (!res.ok) {
        showInfo(await res.text(), "error");
        return;
    }
    showInfo("VPN disconnected", "ok");
    refresh();
});

document.getElementById("reconnectBtn").addEventListener("click", async () => {
    const res = await fetch("/api/vpn/reconnect", { method: "POST" });
    if (!res.ok) {
        showInfo(await res.text(), "error");
        return;
    }
    showInfo("Reconnecting... enter FortiToken when the field appears", "info");
    refresh();
});

document.getElementById("tailscaleSaveBtn").addEventListener("click", saveTailscaleSetting);

refresh();
