(function () {
  "use strict";

  // ---- API helpers ----------------------------------------------------
  function api(method, path, body) {
    var opts = { method: method, headers: {} };
    if (body !== undefined) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    return fetch(path, opts).then(function (r) {
      if (!r.ok) {
        return r.text().then(function (t) {
          var msg = t;
          try { var j = JSON.parse(t); msg = j.error && j.error.message ? j.error.message : (j.detail || t); } catch (_) {}
          throw new Error(msg || r.statusText);
        });
      }
      var ct = r.headers.get("content-type") || "";
      return ct.indexOf("application/json") === 0 ? r.json() : r.text();
    });
  }

  function esc(s) {
    var d = document.createElement("div");
    d.textContent = s == null ? "" : String(s);
    return d.innerHTML;
  }

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined && text !== null) e.textContent = String(text);
    return e;
  }

  // ---- Panel composition ----------------------------------------------
  // On load, fetch /v1/dashboard/panels and build tabs + panel containers
  // from the response. Each panel is a list of sections rendered by kind.

  function loadPanels() {
    return api("GET", "/v1/dashboard/panels").then(function (data) {
      var panels = (data && data.panels) || [];
      var tabs = document.getElementById("panel-tabs");
      var host = document.getElementById("panel-host");
      var empty = document.getElementById("empty-state");
      tabs.innerHTML = "";
      host.innerHTML = "";

      if (panels.length === 0) {
        empty.classList.remove("hidden");
        return;
      }
      empty.classList.add("hidden");

      panels.forEach(function (entry, idx) {
        var d = entry.descriptor || entry;
        var panelID = "panel-" + slug(entry.service_id + "-" + d.title);

        var tab = el("button", "tab" + (idx === 0 ? " active" : ""));
        tab.textContent = d.title;
        tab.dataset.panel = panelID;
        tab.addEventListener("click", function () {
          document.querySelectorAll("#panel-tabs .tab").forEach(function (b) { b.classList.remove("active"); });
          document.querySelectorAll("#panel-host .panel").forEach(function (p) { p.classList.remove("active"); });
          tab.classList.add("active");
          document.getElementById(panelID).classList.add("active");
        });
        tabs.appendChild(tab);

        var panel = el("section", "panel" + (idx === 0 ? " active" : ""));
        panel.id = panelID;
        renderPanel(panel, d);
        host.appendChild(panel);
      });
    }).catch(function (err) {
      var host = document.getElementById("panel-host");
      host.innerHTML = '<div class="err">Failed to load dashboard panels: ' + esc(err.message) + '</div>';
    });
  }

  function slug(s) {
    return String(s).toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/(^-|-$)/g, "");
  }

  function renderPanel(container, descriptor) {
    var header = el("h2", null, descriptor.title);
    container.appendChild(header);
    (descriptor.sections || []).forEach(function (section) {
      var node = renderSection(section);
      if (node) container.appendChild(node);
    });
  }

  // ---- Section renderers ----------------------------------------------
  function renderSection(section) {
    switch (section.kind) {
      case "form":   return renderFormSection(section);
      case "iframe": return renderIframeSection(section);
      case "status": return renderStatusSection(section);
      case "logs":   return renderLogsSection(section);
      case "action": return renderActionSection(section);
      default:
        var d = el("div", "err", "Unknown section kind: " + section.kind);
        return d;
    }
  }

  // ---- Action section -------------------------------------------------
  function renderActionSection(section) {
    var wrap = el("div", "section section-action");
    wrap.appendChild(el("h3", null, section.title));
    (section.actions || []).forEach(function (action) {
      wrap.appendChild(renderOneAction(action));
    });
    return wrap;
  }

  function renderOneAction(action) {
    var row = el("div", "action-row");
    var paramInputs = {};
    (action.params || []).forEach(function (param) {
      var label = el("label", "action-param");
      label.appendChild(el("span", "field-label", param.label || param.name));
      var input;
      switch (param.type) {
        case "textarea":
          input = el("textarea");
          input.rows = 3;
          break;
        case "boolean":
          input = el("input");
          input.type = "checkbox";
          if (param.default === true) input.checked = true;
          break;
        case "enum":
          input = el("select");
          (param.values || []).forEach(function (v) {
            var opt = el("option", null, v);
            opt.value = v;
            input.appendChild(opt);
          });
          break;
        case "number":
        case "integer":
          input = el("input");
          input.type = "number";
          break;
        default:
          input = el("input");
          input.type = "text";
      }
      if (param.default !== undefined && param.default !== null && param.type !== "boolean") {
        input.value = param.default;
      }
      paramInputs[param.name] = { input: input, param: param };
      label.appendChild(input);
      row.appendChild(label);
    });

    var btn = el("button", "action-btn", action.label);
    btn.type = "button";
    var status = el("span", "action-status");
    row.appendChild(btn);
    row.appendChild(status);

    function readParamValue(entry) {
      var p = entry.param, input = entry.input;
      switch (p.type) {
        case "boolean": return input.checked;
        case "number":  return input.value === "" ? null : parseFloat(input.value);
        case "integer": return input.value === "" ? null : parseInt(input.value, 10);
        default:        return input.value;
      }
    }

    function collectRequiredMissing() {
      return (action.params || []).filter(function (p) {
        if (!p.required) return false;
        var v = readParamValue(paramInputs[p.name]);
        return v === null || v === undefined || v === "";
      }).map(function (p) { return p.name; });
    }

    btn.addEventListener("click", function () {
      status.textContent = "";
      var missing = collectRequiredMissing();
      if (missing.length) {
        status.textContent = "Missing required: " + missing.join(", ");
        status.className = "action-status err";
        return;
      }
      if (action.confirm && !window.confirm(action.confirm)) return;

      // Build URL with {name} substitution from params.
      var url = action.url;
      var body = Object.assign({}, action.body || {});
      Object.keys(paramInputs).forEach(function (name) {
        var v = readParamValue(paramInputs[name]);
        var placeholder = "{" + name + "}";
        if (url.indexOf(placeholder) >= 0) {
          url = url.replace(placeholder, encodeURIComponent(String(v)));
        } else {
          body[name] = v;
        }
      });

      var method = (action.method || "POST").toUpperCase();
      var requestBody = method === "DELETE" ? undefined : body;
      api(method, url, requestBody).then(function (data) {
        var msg = "OK";
        if (action.status_key && data && data[action.status_key] !== undefined) {
          msg = action.status_key + ": " + data[action.status_key];
        }
        status.textContent = msg;
        status.className = "action-status ok";
        setTimeout(function () { status.textContent = ""; }, 4000);
      }).catch(function (err) {
        status.textContent = "Error: " + err.message;
        status.className = "action-status err";
      });
    });

    return row;
  }

  // ---- Logs section ---------------------------------------------------
  // Live-tail via SSE, with filter controls (service multiselect, level,
  // correlation_id, free-text). Entries appended in-order; capped at 500
  // in the DOM (FIFO eviction) to keep long sessions responsive.
  var LOGS_DOM_CAP = 500;

  function renderLogsSection(section) {
    var wrap = el("div", "section section-logs");
    wrap.appendChild(el("h3", null, section.title));

    var controls = el("div", "logs-controls");

    var serviceRow = el("div", "logs-filter-row");
    serviceRow.appendChild(el("span", "logs-filter-label", "Services:"));
    var serviceList = el("div", "logs-service-list");
    serviceRow.appendChild(serviceList);
    controls.appendChild(serviceRow);

    var filterRow = el("div", "logs-filter-row");
    var levelLabel = el("label");
    levelLabel.appendChild(el("span", "logs-filter-label", "Level:"));
    var levelSel = el("select");
    ["debug", "info", "warn", "error"].forEach(function (lvl) {
      var opt = el("option", null, lvl.toUpperCase());
      opt.value = lvl;
      if (lvl === (section.default_level || "info")) opt.selected = true;
      levelSel.appendChild(opt);
    });
    levelLabel.appendChild(levelSel);
    filterRow.appendChild(levelLabel);

    var corrLabel = el("label");
    corrLabel.appendChild(el("span", "logs-filter-label", "Correlation ID:"));
    var corrInput = el("input");
    corrInput.type = "text";
    corrInput.placeholder = "exact or prefix";
    corrLabel.appendChild(corrInput);
    filterRow.appendChild(corrLabel);

    var queryLabel = el("label");
    queryLabel.appendChild(el("span", "logs-filter-label", "Search:"));
    var queryInput = el("input");
    queryInput.type = "text";
    queryInput.placeholder = "free text";
    queryLabel.appendChild(queryInput);
    filterRow.appendChild(queryLabel);

    var autoScrollLabel = el("label", "logs-autoscroll");
    var autoScroll = el("input");
    autoScroll.type = "checkbox";
    autoScroll.checked = true;
    autoScrollLabel.appendChild(autoScroll);
    autoScrollLabel.appendChild(el("span", "logs-filter-label", "Auto-scroll"));
    filterRow.appendChild(autoScrollLabel);

    var status = el("span", "logs-status", "disconnected");
    filterRow.appendChild(status);
    controls.appendChild(filterRow);

    wrap.appendChild(controls);

    var list = el("div", "logs-list");
    wrap.appendChild(list);

    // State for the live feed and filter.
    var state = {
      services: [],          // all discovered
      selected: {},          // service → bool
      source: null,          // EventSource
    };

    function reconnect() {
      if (state.source) {
        state.source.close();
        state.source = null;
      }
      var params = new URLSearchParams();
      var svcs = Object.keys(state.selected).filter(function (s) { return state.selected[s]; });
      if (svcs.length) params.set("services", svcs.join(","));
      if (levelSel.value) params.set("level", levelSel.value);
      if (corrInput.value) params.set("correlation_id", corrInput.value);
      if (queryInput.value) params.set("query", queryInput.value);
      status.textContent = "connecting…";
      var src = new EventSource(section.tail_url + "?" + params.toString());
      src.onopen = function () { status.textContent = "connected"; };
      src.onerror = function () { status.textContent = "reconnecting…"; };
      src.onmessage = function (ev) {
        try {
          var entry = JSON.parse(ev.data);
          appendEntry(entry);
        } catch (_) {}
      };
      state.source = src;
    }

    function appendEntry(e) {
      var row = el("div", "logs-row logs-level-" + (e.level || "info").toLowerCase());
      var t = el("span", "logs-time", (e.time || "").slice(11, 23));
      var lvl = el("span", "logs-level", e.level || "");
      var svc = el("span", "logs-service", e.service || "");
      var cid = el("span", "logs-cid", (e.correlation_id || "").slice(0, 8));
      var msg = el("span", "logs-msg", e.msg || "");
      row.appendChild(t);
      row.appendChild(lvl);
      row.appendChild(svc);
      row.appendChild(cid);
      row.appendChild(msg);

      // Expand on click to show raw JSON.
      var details = el("pre", "logs-raw");
      try {
        details.textContent = JSON.stringify(JSON.parse(e.raw), null, 2);
      } catch (_) {
        details.textContent = typeof e.raw === "string" ? e.raw : JSON.stringify(e.raw);
      }
      details.style.display = "none";
      row.addEventListener("click", function () {
        details.style.display = details.style.display === "none" ? "block" : "none";
      });
      row.appendChild(details);

      list.appendChild(row);
      while (list.childNodes.length > LOGS_DOM_CAP) {
        list.removeChild(list.firstChild);
      }
      if (autoScroll.checked) list.scrollTop = list.scrollHeight;
    }

    // Kick off: fetch discoverable services, render toggles, then connect.
    api("GET", section.services_url).then(function (data) {
      state.services = (data && data.services) || [];
      (section.default_services || state.services).forEach(function (s) { state.selected[s] = true; });
      // ensure every discovered service has an entry
      state.services.forEach(function (s) {
        if (!(s in state.selected)) state.selected[s] = true;
      });
      state.services.forEach(function (s) {
        var chk = el("label", "logs-service-toggle");
        var box = el("input");
        box.type = "checkbox";
        box.checked = !!state.selected[s];
        box.addEventListener("change", function () {
          state.selected[s] = box.checked;
          reconnect();
        });
        chk.appendChild(box);
        chk.appendChild(el("span", null, s));
        serviceList.appendChild(chk);
      });
      reconnect();
    }).catch(function (err) {
      status.textContent = "services fetch failed: " + err.message;
    });

    // Wire filter controls to reconnect.
    levelSel.addEventListener("change", reconnect);
    corrInput.addEventListener("change", reconnect);
    queryInput.addEventListener("change", reconnect);

    return wrap;
  }

  function renderFormSection(section) {
    var wrap = el("div", "section section-form");
    wrap.appendChild(el("h3", null, section.title));

    var form = el("form");
    var grid = el("div", "form-grid");
    var fields = section.fields || {};
    var fieldNames = Object.keys(fields);

    // Load current values from /v1/config, populate inputs, wire submit.
    api("GET", "/v1/config").then(function (cfg) {
      var values = (cfg && cfg[section.config_key]) || {};
      fieldNames.forEach(function (name) {
        var schema = fields[name];
        var group = renderField(name, schema, values[name]);
        grid.appendChild(group);
      });
    }).catch(function (err) {
      grid.appendChild(el("div", "err", "Load failed: " + esc(err.message)));
    });

    form.appendChild(grid);

    var saveBtn = el("button", "save-btn", "Save");
    saveBtn.type = "submit";
    var status = el("span", "save-status");
    form.appendChild(saveBtn);
    form.appendChild(status);

    form.addEventListener("submit", function (e) {
      e.preventDefault();
      var out = {};
      fieldNames.forEach(function (name) {
        var schema = fields[name];
        if (schema.readonly) return; // skip readonly fields
        var input = form.querySelector('[data-field="' + name + '"]');
        if (!input) return;
        out[name] = readField(schema, input);
      });
      api("PUT", "/v1/config", { section: section.config_key, values: out })
        .then(function () {
          status.textContent = "Saved";
          status.className = "save-status ok";
          setTimeout(function () { status.textContent = ""; }, 3000);
        })
        .catch(function (err) {
          status.textContent = "Error: " + err.message;
          status.className = "save-status err";
        });
    });

    wrap.appendChild(form);
    return wrap;
  }

  function renderField(name, schema, currentValue) {
    var group = el("label", "form-field");
    var labelText = schema.label || name;
    group.appendChild(el("span", "field-label", labelText));
    if (schema.description) group.appendChild(el("span", "field-desc", schema.description));

    var input;
    switch (schema.type) {
      case "textarea":
        input = el("textarea");
        input.rows = 8;
        break;
      case "boolean":
        input = el("input");
        input.type = "checkbox";
        break;
      case "enum":
        input = el("select");
        (schema.values || []).forEach(function (v) {
          var opt = el("option", null, v);
          opt.value = v;
          input.appendChild(opt);
        });
        break;
      case "number":
      case "integer":
        input = el("input");
        input.type = "number";
        if (schema.min !== undefined) input.min = schema.min;
        if (schema.max !== undefined) input.max = schema.max;
        if (schema.step !== undefined) input.step = schema.step;
        else if (schema.type === "integer") input.step = "1";
        break;
      default:
        input = el("input");
        input.type = "text";
    }
    input.dataset.field = name;
    if (schema.readonly) input.readOnly = true;

    var initial = currentValue !== undefined ? currentValue : (schema.default !== undefined ? schema.default : null);
    if (initial !== null && initial !== undefined) {
      if (schema.type === "boolean") input.checked = Boolean(initial);
      else input.value = initial;
    }
    group.appendChild(input);
    return group;
  }

  function readField(schema, input) {
    switch (schema.type) {
      case "boolean": return input.checked;
      case "number":  return input.value === "" ? null : parseFloat(input.value);
      case "integer": return input.value === "" ? null : parseInt(input.value, 10);
      default:        return input.value;
    }
  }

  function renderIframeSection(section) {
    var wrap = el("div", "section section-iframe");
    wrap.appendChild(el("h3", null, section.title));
    var frame = el("iframe");
    var iframeURL = new URL(section.url);
    if (iframeURL.hostname === "localhost" || iframeURL.hostname === "127.0.0.1") {
      iframeURL.hostname = window.location.hostname;
    }
    frame.src = iframeURL.toString();
    frame.setAttribute("sandbox", "allow-scripts allow-same-origin allow-forms");
    frame.style.width = "100%";
    frame.style.height = section.height || "600px";
    frame.style.border = "1px solid #ccc";
    wrap.appendChild(frame);
    return wrap;
  }

  function renderStatusSection(section) {
    var wrap = el("div", "section section-status");
    wrap.appendChild(el("h3", null, section.title));
    var body = el("div", "status-body");
    body.textContent = "Loading…";
    wrap.appendChild(body);

    api("GET", section.url).then(function (data) {
      body.innerHTML = "";
      if (section.layout === "table") {
        body.appendChild(renderAsTable(data));
      } else {
        body.appendChild(renderAsKeyValue(data));
      }
    }).catch(function (err) {
      body.textContent = "Error: " + err.message;
      body.className = "status-body err";
    });
    return wrap;
  }

  function renderAsKeyValue(obj) {
    // Accept either a top-level object or a { services: [...], system: {...}, ... } shape.
    var dl = el("dl", "kv");
    flatten(obj, "", function (k, v) {
      dl.appendChild(el("dt", null, k));
      dl.appendChild(el("dd", null, typeof v === "object" ? JSON.stringify(v) : String(v)));
    });
    return dl;
  }

  function flatten(obj, prefix, cb) {
    if (obj == null) return;
    if (Array.isArray(obj)) {
      cb(prefix || "items", obj);
      return;
    }
    if (typeof obj !== "object") {
      cb(prefix, obj);
      return;
    }
    Object.keys(obj).forEach(function (k) {
      var path = prefix ? prefix + "." + k : k;
      var v = obj[k];
      if (v && typeof v === "object" && !Array.isArray(v)) {
        flatten(v, path, cb);
      } else {
        cb(path, v);
      }
    });
  }

  function renderAsTable(data) {
    // Accept an array directly, or an object whose first array value is the rows.
    var rows = Array.isArray(data) ? data : firstArrayValue(data);
    if (!rows || rows.length === 0) {
      return el("p", "muted", "No rows.");
    }
    var columns = columnsFor(rows);
    var table = el("table");
    var thead = el("thead");
    var trh = el("tr");
    columns.forEach(function (c) { trh.appendChild(el("th", null, c)); });
    thead.appendChild(trh);
    table.appendChild(thead);
    var tbody = el("tbody");
    rows.forEach(function (row) {
      var tr = el("tr");
      columns.forEach(function (c) {
        var v = row && row[c];
        tr.appendChild(el("td", null, v == null ? "" : (typeof v === "object" ? JSON.stringify(v) : String(v))));
      });
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    return table;
  }

  function firstArrayValue(obj) {
    if (!obj || typeof obj !== "object") return null;
    var keys = Object.keys(obj);
    for (var i = 0; i < keys.length; i++) {
      if (Array.isArray(obj[keys[i]])) return obj[keys[i]];
    }
    return null;
  }

  function columnsFor(rows) {
    var set = {};
    rows.forEach(function (r) {
      if (r && typeof r === "object") Object.keys(r).forEach(function (k) { set[k] = true; });
    });
    return Object.keys(set);
  }

  // ---- Bootstrap -------------------------------------------------------
  document.addEventListener("DOMContentLoaded", loadPanels);
})();
