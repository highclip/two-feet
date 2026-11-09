// Keeps the Clerk session and the server's view of it in step. Clerk's session token lasts about a
// minute and clerk-js refreshes it while a page is open, so a server-rendered page can arrive with
// an expired one. These handlers recover from that without asking anyone to sign in again.
(function () {
  "use strict";

  var RECENT_MS = 10000;

  // Reports whether this guard fired recently, and records that it's firing now. Stops a reload or
  // redirect loop if the server keeps rejecting a session Clerk thinks is fine.
  function firedRecently(key) {
    var last = Number(sessionStorage.getItem(key) || 0);
    var recent = Date.now() - last < RECENT_MS;
    if (!recent) sessionStorage.setItem(key, String(Date.now()));
    return recent;
  }

  function signInPage(clerk, el) {
    var returnTo = el.dataset.return || "/";
    if (!clerk.user) {
      clerk.mountSignIn(el, {
        withSignUp: true,
        forceRedirectUrl: returnTo,
        signUpForceRedirectUrl: returnTo,
      });
      return;
    }
    clerk.session.getToken().then(function () {
      if (firedRecently("twofeet-signin-return")) {
        el.textContent = "We couldn't confirm your sign-in. Reload this page to try again.";
        return;
      }
      location.replace(returnTo);
    });
  }

  window.addEventListener("load", function () {
    if (!window.Clerk) return;
    window.Clerk.load().then(function () {
      var el = document.getElementById("clerk-sign-in");
      if (el) {
        signInPage(window.Clerk, el);
        return;
      }
      if (window.Clerk.user && document.body.dataset.signedIn === "false" && !firedRecently("twofeet-refresh")) {
        window.Clerk.session.getToken().then(function () { location.reload(); });
      }
    });
  });

  document.addEventListener("htmx:responseError", function (e) {
    if (e.detail.xhr.status === 401) {
      location.href = "/sign-in?return=" + encodeURIComponent(location.pathname + location.search);
    }
  });
})();
