// Nearhost's pair page: opened from the Home Screen, the page is already
// installed, so it asks for the code rather than saying to add it.
if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(function(){});
document.addEventListener("DOMContentLoaded", function () {
  var standalone = window.matchMedia("(display-mode: standalone)").matches || window.navigator.standalone === true;
  if (!standalone) return;
  var inTab = document.getElementById("in-tab");
  var app = document.getElementById("standalone");
  if (app) {
    if (inTab) inTab.hidden = true;
    app.hidden = false;
  }
});
