let editingProfile = null;
let connectProfileId = null;
let pollTimer = null;
let lastVPNPhase = null;
let statusFailCount = 0;
let currentInputKind = "token";

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

function phaseLabel(phase, inputKind) {
    switch (phase) {
        case "connected": return "Connected";
        case "connecting": return "Connecting...";
        case "need_input": return inputKind === "password" ? "Password required" : "Token required";
        case "error": return "Error";
        default: return "Disconnected";
    }
}

function renderVPN(vpn) {
    const state = document.getElementById("vpnState");
    const details = document.getElementById("vpnDetails");
    const disconnectBtn = document.getElementById("disconnectBtn");
    const reconnectBtn = document.getElementById("reconnectBtn");
    const tokenPanel = document.getElementById("tokenPanel");
    const tokenPrompt = document.getElementById("tokenPrompt");
    const vpnToken = document.getElementById("vpnToken");
    const submitTokenBtn = document.getElementById("submitTokenBtn");

    const inputKind = vpn.input_kind || "token";
    currentInputKind = inputKind;
    const isPassword = inputKind === "password";

    if (state) {
        state.textContent = phaseLabel(vpn.phase, inputKind);
        state.className = vpn.phase === "connected" ? "ok" : (vpn.phase === "error" ? "error-text" : "muted");
    }

    if (vpn.phase === "connected") {
        if (details) details.textContent = `${vpn.profile_name || vpn.profile_id} via ${vpn.tun_iface || "tun"} since ${vpn.since || "?"}`;
        if (disconnectBtn) disconnectBtn.disabled = false;
        if (tokenPanel) tokenPanel.classList.add("hidden");
    } else if (vpn.phase === "connecting") {
        if (details) details.textContent = `Connecting to ${vpn.profile_name || vpn.profile_id || "VPN"}...`;
        if (disconnectBtn) disconnectBtn.disabled = false;
        if (tokenPanel) tokenPanel.classList.add("hidden");
    } else if (vpn.phase === "need_input") {
        if (details) {
            details.textContent = isPassword
                ? "Re-enter your VPN password below."
                : "Enter your one-time token or OTP code.";
        }
        if (tokenPrompt) {
            tokenPrompt.textContent = vpn.input_prompt || (isPassword
                ? "Password was not accepted — try again."
                : "VPN server is waiting for a one-time token.");
        }
        if (vpnToken) {
            vpnToken.type = isPassword ? "password" : "text";
            vpnToken.inputMode = isPassword ? "" : "numeric";
            vpnToken.autocomplete = isPassword ? "current-password" : "one-time-code";
            vpnToken.placeholder = isPassword ? "" : "123456";
        }
        if (submitTokenBtn) submitTokenBtn.textContent = isPassword ? "Submit password" : "Submit token";
        if (tokenPanel) tokenPanel.classList.remove("hidden");
        if (disconnectBtn) disconnectBtn.disabled = false;
        if (vpnToken) vpnToken.focus();
    } else if (vpn.phase === "error") {
        if (details) details.textContent = vpn.last_error || "Connection failed";
        if (disconnectBtn) disconnectBtn.disabled = true;
        if (tokenPanel) tokenPanel.classList.add("hidden");
    } else {
        if (details) details.textContent = vpn.last_error || "LAN uses direct WAN NAT";
        if (disconnectBtn) disconnectBtn.disabled = true;
        if (tokenPanel) tokenPanel.classList.add("hidden");
    }

    if (reconnectBtn) reconnectBtn.disabled = vpn.phase === "connecting" || vpn.phase === "need_input";
}

function renderProfiles(profiles, vpn) {
    const list = document.getElementById("profilesList");
    if (!list) return;
    if (!profiles.length) {
        list.innerHTML = "<p class='muted'>No profiles yet. Add one below.</p>";
        return;
    }

    const busy = vpn.phase === "connecting" || vpn.phase === "need_input";

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
    pollTimer = setInterval(refresh, fast ? 1500 : 10000);
}

function renderWiFiAP(network) {
    const el = document.getElementById("wifiApStatus");
    const mgmt = document.getElementById("mgmtHint");
    if (!mgmt) return;
    const ap = network?.wifi_ap;
    const ips = (network?.management_ips || []).map(ip => `http://${ip}:5000/`).join(" · ");
    mgmt.textContent = ips
        ? `Management (stays on WAN): ${ips} — also use LAN gateway after VPN is up`
        : "Use LAN gateway IP for dashboard if WAN URL stops responding during VPN connect";
    if (!el) return;
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
        renderVPN(data.vpn);
        renderWiFiAP(data.network);
        renderProfiles(data.profiles || [], data.vpn);
        const logEl = document.getElementById("logTail");
        if (logEl) logEl.textContent = data.log_tail || "(no log output yet)";

        const active = data.vpn.phase === "connecting" || data.vpn.phase === "need_input";
        schedulePoll(active);

        if (data.vpn.phase === "connected" && prevPhase !== "connected") {
            showInfo("VPN connected via " + (data.vpn.tun_iface || "tunnel"), "ok");
        }
    } catch (err) {
        statusFailCount += 1;
        if (statusFailCount === 1) {
            const state = document.getElementById("vpnState");
            if (state) {
                state.textContent = "Error";
                state.className = "error-text";
            }
            const details = document.getElementById("vpnDetails");
            if (details) details.textContent = "Could not load status: " + err.message;
        }
        if (statusFailCount >= 2) {
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
    showInfo("Connecting... enter token/OTP if prompted", "info");
    refresh();
}

async function submitToken() {
    const vpnToken = document.getElementById("vpnToken");
    if (!vpnToken) return;
    const value = vpnToken.value.trim();
    if (!value) {
        showInfo(currentInputKind === "password" ? "Enter your password" : "Enter the token code", "error");
        return;
    }
    const body = currentInputKind === "password" ? { password: value } : { token: value };
    const res = await fetch("/api/vpn/input", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body)
    });
    if (!res.ok) {
        showInfo(await res.text(), "error");
        return;
    }
    vpnToken.value = "";
    showInfo(currentInputKind === "password" ? "Password sent" : "Token sent", "ok");
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
    showInfo("Reconnecting... enter FortiToken if prompted", "info");
    refresh();
});

refresh();
