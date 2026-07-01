let editingProfile = null;
let connectProfileId = null;
let pollTimer = null;

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

function renderVPN(vpn) {
    const state = document.getElementById("vpnState");
    const details = document.getElementById("vpnDetails");
    const disconnectBtn = document.getElementById("disconnectBtn");
    const reconnectBtn = document.getElementById("reconnectBtn");
    const tokenPanel = document.getElementById("tokenPanel");
    const tokenPrompt = document.getElementById("tokenPrompt");

    state.textContent = phaseLabel(vpn.phase);
    state.className = vpn.phase === "connected" ? "ok" : (vpn.phase === "error" ? "error-text" : "muted");

    if (vpn.phase === "connected") {
        details.textContent = `${vpn.profile_name || vpn.profile_id} via ${vpn.tun_iface || "tun"} since ${vpn.since || "?"}`;
        disconnectBtn.disabled = false;
        tokenPanel.classList.add("hidden");
    } else if (vpn.phase === "connecting") {
        details.textContent = `Connecting to ${vpn.profile_name || vpn.profile_id || "VPN"}...`;
        disconnectBtn.disabled = false;
        tokenPanel.classList.add("hidden");
    } else if (vpn.phase === "need_input") {
        details.textContent = "Enter your FortiToken or OTP code.";
        tokenPrompt.textContent = vpn.input_prompt || "Fortinet is waiting for a one-time token.";
        tokenPanel.classList.remove("hidden");
        disconnectBtn.disabled = false;
        document.getElementById("vpnToken").focus();
    } else if (vpn.phase === "error") {
        details.textContent = vpn.last_error || "Connection failed";
        disconnectBtn.disabled = true;
        tokenPanel.classList.add("hidden");
    } else {
        details.textContent = vpn.last_error || "LAN uses direct WAN NAT";
        disconnectBtn.disabled = true;
        tokenPanel.classList.add("hidden");
    }

    reconnectBtn.disabled = vpn.phase === "connecting" || vpn.phase === "need_input";
}

function renderProfiles(profiles, vpn) {
    const list = document.getElementById("profilesList");
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
                    <div class="muted small">${escapeHtml(p.username)} @ ${escapeHtml(p.server_url)}</div>
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

async function refresh() {
    try {
        const data = await fetchStatus();
        renderVPN(data.vpn);
        renderProfiles(data.profiles || [], data.vpn);
        document.getElementById("logTail").textContent = data.log_tail || "";

        const active = data.vpn.phase === "connecting" || data.vpn.phase === "need_input";
        schedulePoll(active);

        if (data.vpn.phase === "connected") {
            showInfo("VPN connected", "ok");
        }
    } catch (err) {
        showInfo(err.message, "error");
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
    showInfo("Connecting... enter FortiToken if prompted", "info");
    refresh();
}

async function submitToken() {
    const token = document.getElementById("vpnToken").value.trim();
    if (!token) {
        showInfo("Enter the token code", "error");
        return;
    }
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
        document.getElementById("profileName").value = p.name;
        document.getElementById("profileUser").value = p.username;
        document.getElementById("profileURL").value = p.server_url;
        document.getElementById("profilePin").value = p.servercert_pin;
        document.getElementById("savePassword").checked = p.save_password;
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
        protocol: "fortinet",
        username: document.getElementById("profileUser").value,
        server_url: document.getElementById("profileURL").value,
        servercert_pin: document.getElementById("profilePin").value,
        save_password: document.getElementById("savePassword").checked,
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
