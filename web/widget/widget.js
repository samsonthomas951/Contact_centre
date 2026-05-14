// Embeddable contact-centre widget. ~3 KB minified, no dependencies.
//
//   <script src="https://cdn.example.co.ke/widget.js"
//           data-embed-key="abc123"
//           data-endpoint="wss://app.example.co.ke/ws/widget"
//           data-consent-text="By chatting you agree to our privacy notice."
//           async></script>
//
// Behaviour:
//   1. On script load, mount a minimal button + chat panel into the page.
//   2. On open, generate or read a `cc_visitor` UUID cookie (12-month TTL).
//   3. Open a WebSocket to data-endpoint; send {"type":"hello", payload:{visitor_id, embed_key}}.
//   4. Render `hello_ack`, `agent_message`, and `error` frames; emit
//      `message` / `identify` / `typing` frames from the UI.
//   5. Show the consent banner before any identify form is submitted.

(function () {
  "use strict";

  const script = document.currentScript;
  if (!script) return;
  const ENDPOINT = script.dataset.endpoint || "";
  const EMBED_KEY = script.dataset.embedKey || "";
  const CONSENT_TEXT = script.dataset.consentText ||
    "By starting a chat you accept our privacy notice.";

  if (!ENDPOINT) {
    console.warn("[contact-centre widget] data-endpoint missing");
    return;
  }

  // ---- cookie helpers ------------------------------------------------------
  function getCookie(name) {
    const m = document.cookie.match(new RegExp("(?:^|; )" + name + "=([^;]+)"));
    return m ? decodeURIComponent(m[1]) : "";
  }
  function setCookie(name, value, days) {
    const exp = new Date(Date.now() + days * 86400 * 1000).toUTCString();
    document.cookie =
      name + "=" + encodeURIComponent(value) +
      "; expires=" + exp + "; path=/; SameSite=Lax";
  }
  function uuid() {
    // RFC 4122 v4 — uses crypto.getRandomValues when available.
    const buf = new Uint8Array(16);
    (window.crypto || window.msCrypto).getRandomValues(buf);
    buf[6] = (buf[6] & 0x0f) | 0x40;
    buf[8] = (buf[8] & 0x3f) | 0x80;
    const h = Array.from(buf, (b) => b.toString(16).padStart(2, "0")).join("");
    return h.slice(0, 8) + "-" + h.slice(8, 12) + "-" + h.slice(12, 16) +
           "-" + h.slice(16, 20) + "-" + h.slice(20);
  }
  function visitorID() {
    let v = getCookie("cc_visitor");
    if (!v) {
      v = uuid();
      setCookie("cc_visitor", v, 365);
    }
    return v;
  }

  // ---- DOM ----------------------------------------------------------------
  function el(tag, attrs, ...children) {
    const node = document.createElement(tag);
    for (const k in (attrs || {})) {
      if (k === "style") node.style.cssText = attrs[k];
      else if (k.startsWith("on")) node.addEventListener(k.slice(2), attrs[k]);
      else node.setAttribute(k, attrs[k]);
    }
    children.forEach((c) => node.appendChild(typeof c === "string"
      ? document.createTextNode(c) : c));
    return node;
  }

  const button = el("button", {
    id: "cc-widget-toggle",
    style: "position:fixed;right:24px;bottom:24px;width:56px;height:56px;border-radius:28px;border:0;background:#0b6df0;color:#fff;font:600 22px sans-serif;box-shadow:0 4px 16px rgba(0,0,0,.18);cursor:pointer;z-index:2147483646;",
  }, "💬");
  const panel = el("div", {
    id: "cc-widget-panel",
    style: "position:fixed;right:24px;bottom:96px;width:340px;max-height:520px;background:#fff;border-radius:12px;box-shadow:0 8px 30px rgba(0,0,0,.18);font:14px/1.4 system-ui,sans-serif;display:none;flex-direction:column;overflow:hidden;z-index:2147483647;",
  });
  const log = el("div", {
    id: "cc-widget-log",
    style: "flex:1;overflow:auto;padding:12px;background:#f7f8fa;",
  });
  const consent = el("div", {
    id: "cc-widget-consent",
    style: "padding:10px;background:#fff7e0;font-size:12px;color:#553;",
  }, CONSENT_TEXT);
  const inputRow = el("div", {
    style: "display:flex;border-top:1px solid #e3e6ea;",
  });
  const input = el("input", {
    type: "text", placeholder: "Type a message...",
    style: "flex:1;border:0;padding:12px;outline:0;font:inherit;",
  });
  const send = el("button", {
    type: "button",
    style: "border:0;padding:0 16px;background:#0b6df0;color:#fff;font:600 14px sans-serif;cursor:pointer;",
  }, "Send");
  inputRow.appendChild(input);
  inputRow.appendChild(send);

  panel.appendChild(consent);
  panel.appendChild(log);
  panel.appendChild(inputRow);

  document.body.appendChild(button);
  document.body.appendChild(panel);

  // ---- transport ---------------------------------------------------------
  let ws = null;
  let opened = false;
  function append(kind, text) {
    const row = el("div", {
      style: "margin:6px 0;padding:8px 10px;border-radius:10px;max-width:80%;" +
             (kind === "out"
               ? "background:#0b6df0;color:#fff;margin-left:auto;"
               : "background:#fff;border:1px solid #e3e6ea;"),
    }, text);
    log.appendChild(row);
    log.scrollTop = log.scrollHeight;
  }
  function open() {
    if (ws && ws.readyState <= 1) return;
    ws = new WebSocket(ENDPOINT);
    ws.onopen = () => {
      ws.send(JSON.stringify({
        type: "hello",
        payload: { visitor_id: visitorID(), embed_key: EMBED_KEY },
      }));
    };
    ws.onmessage = (ev) => {
      let frame;
      try { frame = JSON.parse(ev.data); } catch { return; }
      switch (frame.type) {
        case "hello_ack":
        case "system":
          append("sys", frame.body || "");
          break;
        case "agent_message":
          append("in", frame.body || "");
          break;
        case "error":
          append("sys", "⚠ " + (frame.body || "error"));
          break;
      }
    };
    ws.onclose = () => { append("sys", "Disconnected."); };
  }
  send.addEventListener("click", () => {
    const body = input.value.trim();
    if (!body || !ws || ws.readyState !== 1) return;
    ws.send(JSON.stringify({ type: "message", body }));
    append("out", body);
    input.value = "";
  });
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter") send.click();
  });

  button.addEventListener("click", () => {
    opened = !opened;
    panel.style.display = opened ? "flex" : "none";
    if (opened) open();
  });
})();
