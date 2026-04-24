(function () {
  "use strict";

  // --- Panel switching ---
  document.querySelectorAll(".tab").forEach(function (btn) {
    btn.addEventListener("click", function () {
      document.querySelectorAll(".tab").forEach(function (b) { b.classList.remove("active"); });
      document.querySelectorAll(".panel").forEach(function (p) { p.classList.remove("active"); });
      btn.classList.add("active");
      document.getElementById("panel-" + btn.dataset.panel).classList.add("active");
      if (btn.dataset.panel === "sessions") loadSessions();
      if (btn.dataset.panel === "system") loadStatus();
      if (btn.dataset.panel === "agents") { loadStatus(); loadConfig(); loadMCPServers(); }
    });
  });

  // --- API helpers ---
  function api(method, path, body) {
    var opts = { method: method, headers: {} };
    if (body !== undefined) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    return fetch(path, opts).then(function (r) {
      if (!r.ok) return r.json().then(function (e) { throw new Error(e.error ? e.error.message : r.statusText); });
      return r.json();
    });
  }

  function showStatus(id, ok, msg) {
    var el = document.getElementById(id);
    el.textContent = msg;
    el.className = "save-status " + (ok ? "ok" : "err");
    setTimeout(function () { el.textContent = ""; }, 3000);
  }

  // --- Config load/save ---
  function loadConfig() {
    api("GET", "/v1/config").then(function (cfg) {
      // Chat
      setVal("chat-system_prompt", cfg.chat.system_prompt);
      setVal("chat-model", cfg.chat.model);
      setVal("chat-request_timeout_s", cfg.chat.request_timeout_s);
      // Memory
      setVal("memory-recall_limit", cfg.memory.recall_limit);
      setVal("memory-recall_threshold", cfg.memory.recall_threshold);
      setVal("memory-max_hops", cfg.memory.max_hops);
      setVal("memory-prewarm_limit", cfg.memory.prewarm_limit);
      setVal("memory-vault", cfg.memory.vault);
      setVal("memory-store_confidence", cfg.memory.store_confidence);
      // Broker
      setVal("broker-slot_count", cfg.broker.slot_count);
      setVal("broker-preempt_timeout_ms", cfg.broker.preempt_timeout_ms);
      // Retro
      setVal("retro-inactivity_timeout_s", cfg.retro.inactivity_timeout_s);
      setVal("retro-skill_dup_threshold", cfg.retro.skill_dup_threshold);
      setVal("retro-min_history_turns", cfg.retro.min_history_turns);
      setVal("retro-curation_categories", (cfg.retro.curation_categories || []).join(", "));
    });
  }

  function setVal(id, val) {
    var el = document.getElementById(id);
    if (el) el.value = val != null ? val : "";
  }
  function getVal(id) { return document.getElementById(id).value; }
  function getNum(id) { return Number(document.getElementById(id).value); }

  function saveSection(section, values, statusId) {
    api("PUT", "/v1/config", { section: section, values: values })
      .then(function () { showStatus(statusId, true, "Saved"); })
      .catch(function (e) { showStatus(statusId, false, e.message); });
  }

  document.getElementById("chat-form").addEventListener("submit", function (e) {
    e.preventDefault();
    saveSection("chat", {
      system_prompt: getVal("chat-system_prompt"),
      model: getVal("chat-model"),
      request_timeout_s: getNum("chat-request_timeout_s")
    }, "chat-status");
  });

  document.getElementById("memory-form").addEventListener("submit", function (e) {
    e.preventDefault();
    saveSection("memory", {
      recall_limit: getNum("memory-recall_limit"),
      recall_threshold: getNum("memory-recall_threshold"),
      max_hops: getNum("memory-max_hops"),
      prewarm_limit: getNum("memory-prewarm_limit"),
      vault: getVal("memory-vault"),
      store_confidence: getNum("memory-store_confidence")
    }, "memory-status");
  });

  document.getElementById("broker-form").addEventListener("submit", function (e) {
    e.preventDefault();
    saveSection("broker", {
      slot_count: getNum("broker-slot_count"),
      preempt_timeout_ms: getNum("broker-preempt_timeout_ms")
    }, "broker-status");
  });

  document.getElementById("retro-form").addEventListener("submit", function (e) {
    e.preventDefault();
    var cats = getVal("retro-curation_categories").split(",").map(function (s) { return s.trim(); }).filter(Boolean);
    saveSection("retro", {
      inactivity_timeout_s: getNum("retro-inactivity_timeout_s"),
      skill_dup_threshold: getNum("retro-skill_dup_threshold"),
      min_history_turns: getNum("retro-min_history_turns"),
      curation_categories: cats
    }, "retro-status");
  });

  // --- Sessions ---
  function loadSessions() {
    api("GET", "/v1/sessions").then(function (sessions) {
      var tbody = document.querySelector("#sessions-table tbody");
      tbody.innerHTML = "";
      sessions.forEach(function (s) {
        var tr = document.createElement("tr");
        tr.innerHTML =
          "<td>" + esc(s.session_id) + "</td>" +
          "<td>" + s.turn_count + "</td>" +
          "<td></td>";
        var actions = tr.querySelector("td:last-child");

        var viewBtn = document.createElement("button");
        viewBtn.className = "action-btn";
        viewBtn.textContent = "View";
        viewBtn.addEventListener("click", function () { viewSession(s.session_id); });
        actions.appendChild(viewBtn);

        var delBtn = document.createElement("button");
        delBtn.className = "action-btn danger";
        delBtn.textContent = "Delete";
        delBtn.addEventListener("click", function () { deleteSession(s.session_id); });
        actions.appendChild(delBtn);

        var sel = document.createElement("select");
        sel.className = "retro-select";
        ["memory_extraction", "skill_creation", "curation"].forEach(function (jt) {
          var opt = document.createElement("option");
          opt.value = jt;
          opt.textContent = jt;
          sel.appendChild(opt);
        });
        actions.appendChild(sel);

        var trigBtn = document.createElement("button");
        trigBtn.className = "action-btn";
        trigBtn.textContent = "Trigger";
        trigBtn.addEventListener("click", function () { triggerRetro(s.session_id, sel.value); });
        actions.appendChild(trigBtn);

        tbody.appendChild(tr);
      });
    });
  }

  function viewSession(id) {
    api("GET", "/v1/sessions/" + encodeURIComponent(id)).then(function (data) {
      var container = document.getElementById("session-messages");
      container.innerHTML = "";
      (data.messages || []).forEach(function (m) {
        container.appendChild(renderMessage(m));
      });
      document.getElementById("session-detail").classList.remove("hidden");
    });
  }

  function renderMessage(m) {
    // Assistant message with tool_calls: render each as a collapsed status block.
    if (m.role === "assistant" && Array.isArray(m.tool_calls) && m.tool_calls.length > 0) {
      var wrap = document.createElement("div");
      wrap.className = "msg";
      m.tool_calls.forEach(function (tc) {
        wrap.appendChild(renderToolCallBlock(tc));
      });
      if (m.content) {
        var textDiv = document.createElement("div");
        textDiv.className = "msg-content";
        textDiv.textContent = m.content;
        wrap.appendChild(textDiv);
      }
      return wrap;
    }
    // Tool result message: collapsed block with the output.
    if (m.role === "tool") {
      return renderToolResultBlock(m);
    }
    var div = document.createElement("div");
    div.className = "msg";
    div.innerHTML = '<div class="msg-role ' + esc(m.role) + '">' + esc(m.role) + '</div>' +
                    '<div class="msg-content">' + esc(m.content) + '</div>';
    return div;
  }

  function renderToolCallBlock(tc) {
    var det = document.createElement("details");
    det.className = "tool-call";
    var sum = document.createElement("summary");
    sum.textContent = "🔧 " + (tc.function && tc.function.name ? tc.function.name : "tool_call");
    det.appendChild(sum);
    var body = document.createElement("pre");
    body.className = "tool-call-body";
    body.textContent = prettyJSON(tc.function && tc.function.arguments);
    det.appendChild(body);
    return det;
  }

  function renderToolResultBlock(m) {
    var det = document.createElement("details");
    det.className = "tool-call tool-result";
    var sum = document.createElement("summary");
    sum.textContent = "↳ tool result" + (m.tool_call_id ? " (" + m.tool_call_id + ")" : "");
    det.appendChild(sum);
    var body = document.createElement("pre");
    body.className = "tool-call-body";
    body.textContent = m.content || "";
    det.appendChild(body);
    return det;
  }

  function prettyJSON(s) {
    if (s == null || s === "") return "";
    try {
      return JSON.stringify(JSON.parse(s), null, 2);
    } catch (_e) {
      return String(s);
    }
  }

  document.getElementById("close-detail").addEventListener("click", function () {
    document.getElementById("session-detail").classList.add("hidden");
  });

  function deleteSession(id) {
    if (!confirm("Delete session " + id + "?")) return;
    api("DELETE", "/v1/sessions/" + encodeURIComponent(id))
      .then(function () { loadSessions(); })
      .catch(function (e) { alert(e.message); });
  }

  function triggerRetro(sessionId, jobType) {
    api("POST", "/v1/retro/" + encodeURIComponent(sessionId) + "/trigger", { job_type: jobType })
      .then(function () { alert("Retro job triggered: " + jobType); })
      .catch(function (e) { alert(e.message); });
  }

  // --- System Status ---
  function loadStatus() {
    api("GET", "/v1/status").then(function (data) {
      // Health indicators
      var container = document.getElementById("health-indicators");
      container.innerHTML = "";
      (data.services || []).forEach(function (svc) {
        var card = document.createElement("div");
        card.className = "health-card";
        card.innerHTML = '<span class="health-dot ' + svc.status + '"></span>' +
                         '<span>' + esc(svc.name) + '</span>' +
                         (svc.message ? '<span class="health-msg">' + esc(svc.message) + '</span>' : '');
        container.appendChild(card);
      });

      // System info
      var dl = document.getElementById("system-info");
      dl.innerHTML = "";
      if (data.system) {
        addDL(dl, "Gateway Port", data.system.gateway_port);
        addDL(dl, "llama.cpp Address", data.system.llama_addr);
        addDL(dl, "Memory Service Address", data.system.memory_addr);
      }

      // Agents table (on agents panel)
      var tbody = document.querySelector("#agents-table tbody");
      tbody.innerHTML = "";
      (data.agents || []).forEach(function (a) {
        var tr = document.createElement("tr");
        tr.innerHTML =
          "<td>" + esc(a.agent_id) + "</td>" +
          "<td>" + a.priority + "</td>" +
          "<td>" + (a.preemptible ? "Yes" : "No") + "</td>" +
          "<td>" + esc((a.capabilities || []).join(", ")) + "</td>" +
          "<td>" + esc(a.trigger) + "</td>";
        tbody.appendChild(tr);
      });
    });
  }

  function addDL(dl, term, def) {
    var dt = document.createElement("dt");
    dt.textContent = term;
    dl.appendChild(dt);
    var dd = document.createElement("dd");
    dd.textContent = def || "—";
    dl.appendChild(dd);
  }

  function esc(s) {
    if (s == null) return "";
    var d = document.createElement("div");
    d.textContent = String(s);
    return d.innerHTML;
  }

  // --- MCP Servers ---
  var mcpEditingName = null;

  function loadMCPServers() {
    Promise.all([
      api("GET", "/v1/mcp/servers"),
      api("GET", "/v1/status")
    ]).then(function (results) {
      var stored = (results[0] && results[0].servers) || [];
      var live = (results[1] && results[1].mcp_servers) || [];
      var liveByName = {};
      live.forEach(function (e) { liveByName[e.name] = e; });
      var tbody = document.querySelector("#mcp-table tbody");
      tbody.innerHTML = "";
      stored.forEach(function (s) {
        var l = liveByName[s.name] || {};
        var tr = document.createElement("tr");
        tr.innerHTML =
          "<td>" + esc(s.name) + "</td>" +
          "<td>" + (s.enabled ? "Yes" : "No") + "</td>" +
          "<td><code>" + esc(s.command) + (s.args && s.args.length ? " " + esc(s.args.join(" ")) : "") + "</code></td>" +
          "<td>" + (l.connected ? "✓" : "—") + "</td>" +
          "<td>" + (l.tool_count || 0) + "</td>" +
          "<td>" + esc(l.last_error || "") + "</td>" +
          "<td></td>";
        var actions = tr.querySelector("td:last-child");
        var edit = document.createElement("button");
        edit.className = "action-btn";
        edit.textContent = "Edit";
        edit.addEventListener("click", function () { mcpOpenForm(s); });
        actions.appendChild(edit);
        var del = document.createElement("button");
        del.className = "action-btn danger";
        del.textContent = "Delete";
        del.addEventListener("click", function () { mcpDelete(s.name); });
        actions.appendChild(del);
        tbody.appendChild(tr);
      });

      // Drift detection: show banner when stored config diverges from live state.
      var banner = document.getElementById("mcp-restart-banner");
      var drift = computeMCPDrift(stored, liveByName);
      if (drift) banner.classList.remove("hidden");
      else banner.classList.add("hidden");
    }).catch(function (e) {
      console.error("loadMCPServers", e);
    });
  }

  function computeMCPDrift(stored, liveByName) {
    // Banner shows when stored names/commands/args/env/enabled differ from
    // what main-agent has actually loaded (reported via mcp_servers health).
    // Simple heuristic: any stored entry without a matching live entry, or
    // any live entry missing from stored, or any enabled-disagreement.
    var storedNames = stored.map(function (s) { return s.name; });
    var liveNames = Object.keys(liveByName);
    if (storedNames.length !== liveNames.length) return true;
    for (var i = 0; i < stored.length; i++) {
      var s = stored[i];
      var l = liveByName[s.name];
      if (!l) return true;
      if (!!s.enabled !== !!l.enabled) return true;
    }
    return false;
  }

  function mcpOpenForm(existing) {
    var form = document.getElementById("mcp-form");
    form.classList.remove("hidden");
    document.getElementById("mcp-name").value = existing ? existing.name : "";
    document.getElementById("mcp-name").disabled = !!existing;
    document.getElementById("mcp-command").value = existing ? existing.command : "";
    document.getElementById("mcp-args").value = existing && existing.args ? existing.args.join(" ") : "";
    document.getElementById("mcp-enabled").checked = existing ? !!existing.enabled : true;
    document.getElementById("mcp-env").value = existing && existing.env
      ? Object.keys(existing.env).map(function (k) { return k + "=" + existing.env[k]; }).join("\n")
      : "";
    mcpEditingName = existing ? existing.name : null;
  }

  document.getElementById("mcp-add-btn").addEventListener("click", function () { mcpOpenForm(null); });
  document.getElementById("mcp-cancel").addEventListener("click", function () {
    document.getElementById("mcp-form").classList.add("hidden");
    mcpEditingName = null;
  });

  document.getElementById("mcp-form").addEventListener("submit", function (e) {
    e.preventDefault();
    var entry = {
      name: getVal("mcp-name"),
      command: getVal("mcp-command"),
      args: getVal("mcp-args").split(/\s+/).filter(Boolean),
      enabled: document.getElementById("mcp-enabled").checked,
      env: parseEnvText(getVal("mcp-env"))
    };
    var done = function () {
      document.getElementById("mcp-form").classList.add("hidden");
      mcpEditingName = null;
      loadMCPServers();
      showStatus("mcp-status", true, "Saved — restart main-agent to apply");
    };
    var fail = function (err) { showStatus("mcp-status", false, err.message); };
    if (mcpEditingName) {
      // PUT the full list with this entry replaced.
      api("GET", "/v1/mcp/servers").then(function (cur) {
        var list = (cur.servers || []).map(function (s) {
          return s.name === mcpEditingName ? entry : s;
        });
        return api("PUT", "/v1/mcp/servers", { servers: list });
      }).then(done).catch(fail);
    } else {
      api("POST", "/v1/mcp/servers", entry).then(done).catch(fail);
    }
  });

  function mcpDelete(name) {
    if (!confirm("Delete MCP server " + name + "?")) return;
    api("DELETE", "/v1/mcp/servers/" + encodeURIComponent(name))
      .then(function () { loadMCPServers(); })
      .catch(function (e) { alert(e.message); });
  }

  function parseEnvText(s) {
    var out = {};
    s.split(/\n/).forEach(function (line) {
      line = line.trim();
      if (!line) return;
      var idx = line.indexOf("=");
      if (idx < 0) return;
      out[line.slice(0, idx).trim()] = line.slice(idx + 1);
    });
    return out;
  }

  // Patched api() to tolerate 204 No Content
  var _api = api;
  api = function (method, path, body) {
    var opts = { method: method, headers: {} };
    if (body !== undefined) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    return fetch(path, opts).then(function (r) {
      if (!r.ok) return r.json().then(function (e) { throw new Error(e.error ? e.error.message : r.statusText); });
      if (r.status === 204) return null;
      return r.json();
    });
  };

  // Initial load
  loadConfig();
})();
