// Keeps a live page honest on venue wifi (spec.live.flaky_wifi). The server only pushes changes, so
// after any gap the page has to ask for what it missed.
(function () {
  "use strict";

  var NOTE_AFTER_MS = 5000; // how long the connection can be down before saying so
  var SILENCE_MS = 20000;   // one 15-second heartbeat plus the same 5 seconds of grace
  var CLERK_WAIT_MS = 800;  // a stale session shouldn't hold up the refetch
  var REFETCH_GAP_MS = 1000;
  var POP_MS = 250;

  var note = document.querySelector("[data-live-note]");
  var source = null;
  var beating = null;
  var downSince = null;
  var everOpen = false;
  var lastHeard = Date.now();
  var lastRefetch = 0;

  function setNote(show) {
    if (note) note.hidden = !show;
  }

  // A long-idle tab has a stale Clerk session cookie; refresh it first or the refetch comes back as a
  // visitor's view.
  function freshSession() {
    var clerk = window.Clerk;
    if (!clerk || !clerk.session) return Promise.resolve();
    return Promise.race([
      clerk.session.getToken().catch(function () {}),
      new Promise(function (resolve) { setTimeout(resolve, CLERK_WAIT_MS); }),
    ]);
  }

  // Every fragment that updates over SSE has hx-get and hx-trigger="sse:<event>". Firing that same
  // event makes each one fetch itself once.
  function refetch() {
    var now = Date.now();
    if (now - lastRefetch < REFETCH_GAP_MS) return;
    lastRefetch = now;
    freshSession().then(function () {
      document.querySelectorAll('[hx-get][hx-trigger*="sse:"]').forEach(function (el) {
        var name = el.getAttribute("hx-trigger").match(/sse:[^\s,]+/);
        if (name) htmx.trigger(el, name[0]);
      });
    });
  }

  function heard() {
    lastHeard = Date.now();
  }

  // A dropped wifi connection often looks open to EventSource forever. Closing it and calling the
  // extension's error handler makes it open a fresh one.
  function reconnect() {
    if (!source) return;
    source.close();
    if (downSince === null) downSince = Date.now() - NOTE_AFTER_MS;
    heard();
    if (source.onerror) source.onerror(new Event("error"));
  }

  document.addEventListener("htmx:sseOpen", function (e) {
    source = e.detail.source;
    if (beating !== source) {
      beating = source;
      source.addEventListener("heartbeat", heard);
    }
    var cameBack = everOpen && downSince !== null;
    downSince = null;
    everOpen = true;
    heard();
    setNote(false);
    if (cameBack) refetch();
  });

  document.addEventListener("htmx:sseError", function () {
    if (downSince === null) downSince = Date.now();
  });

  setInterval(function () {
    if (downSince !== null && Date.now() - downSince >= NOTE_AFTER_MS) setNote(true);
    if (document.visibilityState === "visible" && source && source.readyState === EventSource.OPEN &&
        Date.now() - lastHeard >= SILENCE_MS) {
      reconnect();
    }
  }, 1000);

  function backInFront() {
    refetch();
    if (source && Date.now() - lastHeard >= SILENCE_MS) reconnect();
  }

  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState === "visible") backInFront();
  });
  window.addEventListener("pageshow", function (e) {
    if (e.persisted) backInFront();
  });

  // Toasts that arrive after page load also go away on their own.
  function expireToasts(root) {
    if (!root || !root.querySelectorAll) return;
    var found = Array.prototype.slice.call(root.querySelectorAll("[data-expire-ms]"));
    if (root.matches && root.matches("[data-expire-ms]")) found.push(root);
    found.forEach(function (el) {
      setTimeout(function () { el.remove(); }, Number(el.dataset.expireMs));
    });
  }
  document.addEventListener("htmx:load", function (e) { expireToasts(e.detail.elt); });

  // The dot that just filled drops in. An update can arrive twice in a row (the joiner's own answer
  // and the SSE refetch), and the second swap shouldn't cut the animation short.
  var before = new WeakMap();
  var pop = { target: null, from: 0, at: 0 };

  function filled(target) {
    return target.querySelectorAll(".seats .seat-filled");
  }

  document.addEventListener("htmx:beforeSwap", function (e) {
    if (e.detail.target) before.set(e.detail.target, filled(e.detail.target).length);
  });

  document.addEventListener("htmx:afterSwap", function (e) {
    var target = e.detail.target;
    if (!target || !before.has(target)) return;
    var from = before.get(target);
    var now = Date.now();
    var dots = filled(target);
    if (dots.length > from) {
      pop = { target: target, from: from, at: now };
    } else if (pop.target === target && now - pop.at < POP_MS) {
      from = pop.from;
    } else {
      return;
    }
    for (var i = from; i < dots.length; i++) dots[i].classList.add("seat-pop");
  });
})();
