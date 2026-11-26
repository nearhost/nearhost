// Keeps Nearhost's page up to date: on each change the core's event stream
// tells of, and every 15 seconds while the page is in view, it fetches the
// page again and puts its main part in place of the one shown, and its
// title on the tab. One fetch runs at a time, with at most one more
// queued. Only the page's own part, marked so, replaces what is shown. A
// view showing an error is left as it is, so the error stays until the
// user moves on, and the timed refresh waits while a form's button has the
// focus.
"use strict";
if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(function(){});
(function () {
  var main = document.querySelector("main");
  if (!main) return;
  var busy = false, again = false;
  function refresh() {
    if (main.querySelector("[data-stay]")) return;
    if (busy) { again = true; return; }
    busy = true;
    fetch(location.pathname + location.search, {
      headers: { "X-Nearhost-Part": "main" }, credentials: "same-origin", cache: "no-store"
    }).then(function (r) {
      if (r.headers.get("X-Nearhost-Part") !== "main") return null;
      var title = r.headers.get("X-Nearhost-Title");
      return r.text().then(function (html) { return { html: html, title: title }; });
    }).then(function (part) {
      if (part === null) return;
      main.innerHTML = part.html;
      if (part.title !== null) {
        try { document.title = decodeURIComponent(part.title); } catch (e) {}
      }
    }).catch(function () {}).then(function () {
      busy = false;
      if (again) { again = false; refresh(); }
    });
  }
  var failed = false;
  function connect() {
    var source = new EventSource("/events");
    source.addEventListener("message", function (e) {
      if (e.data === "changed") refresh();
    });
    source.addEventListener("open", function () {
      // A reconnect may follow a core that restarted: what it shows may
      // have changed meanwhile.
      if (failed) { failed = false; refresh(); }
    });
    source.addEventListener("error", function () {
      failed = true;
      // A stream refused outright is not retried by the browser.
      if (source.readyState === EventSource.CLOSED) setTimeout(connect, 5000);
    });
  }
  if (window.EventSource) connect();
  setInterval(function () {
    var focus = document.activeElement;
    if (focus && main.contains(focus) && focus.closest("form")) return;
    if (document.visibilityState === "visible") refresh();
  }, 15000);
})();
