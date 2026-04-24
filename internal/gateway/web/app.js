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
      default:
        var d = el("div", "err", "Unknown section kind: " + section.kind);
        return d;
    }
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
    frame.src = section.url;
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
