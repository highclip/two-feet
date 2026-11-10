// Keeps the Clerk session and the server's view of it in step. Clerk's session token lasts about a
// minute and clerk-js refreshes it while a page is open, so a server-rendered page can arrive with
// an expired one. These handlers recover from that without asking anyone to sign in again.
(function () {
  "use strict";

  var RECENT_MS = 10000;
  var SIGN_IN_DEADLINE_MS = 10000;

  // Reports whether this guard fired recently, and records that it's firing now. Stops a reload or
  // redirect loop if the server keeps rejecting a session Clerk thinks is fine.
  function firedRecently(key) {
    var last = Number(sessionStorage.getItem(key) || 0);
    var recent = Date.now() - last < RECENT_MS;
    if (!recent) sessionStorage.setItem(key, String(Date.now()));
    return recent;
  }

  function token(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  // Clerk draws in its own theme unless told otherwise. Values come from tokens.css at runtime so
  // there is one place to change them.
  function appearance() {
    var card = {
      boxShadow: "none",
      border: "1px solid " + token("--line"),
      borderRadius: token("--radius-card"),
      backgroundColor: token("--card"),
    };
    var title = { fontFamily: token("--font-title"), fontWeight: 800 };
    return {
      variables: {
        colorPrimary: token("--ink"),
        colorText: token("--ink"),
        colorTextSecondary: token("--ink-soft"),
        colorBackground: token("--card"),
        colorInputBackground: token("--card"),
        colorInputText: token("--ink"),
        fontFamily: token("--font-body"),
        fontFamilyButtons: token("--font-title"),
        borderRadius: token("--radius-button"),
      },
      elements: {
        rootBox: { width: "100%" },
        cardBox: Object.assign({ width: "100%" }, card),
        card: { boxShadow: "none", border: "0", backgroundColor: token("--card") },
        footer: { background: token("--card") },
        headerTitle: title,
        formButtonPrimary: {
          boxShadow: "none",
          backgroundImage: "none",
          minHeight: token("--tap"),
          "&::before, &::after": { display: "none" },
        },
        formFieldInput: { boxShadow: "none", borderColor: token("--line"), minHeight: token("--tap") },
      },
    };
  }

  // Swaps the loading outline for one sentence and a way to try again. The outline's element stays
  // in place, so if Clerk arrives late after all, showMounted still clears it.
  function showSignInFailed(slot) {
    var text = document.createElement("p");
    text.textContent = "Sign-in didn't load. Reload to try again.";
    var button = document.createElement("button");
    button.type = "button";
    button.className = "button";
    button.textContent = "Reload";
    button.addEventListener("click", function () { location.reload(); });
    slot.setAttribute("role", "alert");
    slot.replaceChildren(text, button);
  }

  // Removes the loading outline as soon as Clerk has put something of its own in the mount point.
  function showMounted(el, slot) {
    new MutationObserver(function (records, observer) {
      if (Array.prototype.some.call(el.children, function (child) { return child !== slot; })) {
        slot.remove();
        observer.disconnect();
      }
    }).observe(el, { childList: true });
  }

  function signInPage(clerk, el) {
    var returnTo = el.dataset.return || "/";
    if (!clerk.user) {
      clerk.mountSignIn(el, {
        withSignUp: true,
        forceRedirectUrl: returnTo,
        signUpForceRedirectUrl: returnTo,
        appearance: appearance(),
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

  var signInEl = document.getElementById("clerk-sign-in");
  var signInSlot = signInEl && signInEl.querySelector(".clerk-placeholder");

  function signInFailed() {
    if (signInSlot && signInSlot.isConnected) showSignInFailed(signInSlot);
  }

  if (signInSlot) {
    showMounted(signInEl, signInSlot);
    // A stalled connection never fires an error, and holds up the window's load event too.
    setTimeout(signInFailed, SIGN_IN_DEADLINE_MS);
  }

  window.addEventListener("load", function () {
    if (!window.Clerk) {
      signInFailed();
      return;
    }
    window.Clerk.load().then(function () {
      if (signInEl) {
        signInPage(window.Clerk, signInEl);
        return;
      }
      if (window.Clerk.user && document.body.dataset.signedIn === "false" && !firedRecently("twofeet-refresh")) {
        window.Clerk.session.getToken().then(function () { location.reload(); });
      }
    }).catch(signInFailed);
  });

  function showFormDetails(form, show) {
    var summary = form.querySelector("[data-quick-summary]");
    if (summary) summary.hidden = show;
    form.querySelector("[data-form-details]").hidden = !show;
  }

  // A topic fills in the title and, when a spot is free, the spot and time, leaving one tap to confirm.
  function fillFromTopic(button) {
    var form = button.form;
    form.elements.title.value = button.dataset.fillTitle;
    form.querySelectorAll("[data-fill-title]").forEach(function (b) {
      b.setAttribute("aria-pressed", String(b === button));
    });
    if (!button.dataset.fillSpot) return;
    form.elements.spot.value = button.dataset.fillSpot;
    form.elements.starts.value = button.dataset.fillStarts;
    showFormDetails(form, false);
  }

  // Toasts: an Undo goes away by itself, and a notice can be closed.
  document.querySelectorAll("[data-expire-ms]").forEach(function (el) {
    setTimeout(function () { el.remove(); }, Number(el.dataset.expireMs));
  });
  document.addEventListener("click", function (e) {
    var close = e.target.closest("[data-dismiss]");
    if (close) close.closest(".toast").remove();

    // A topic from online fills in the title, which stays an ordinary field to edit.
    var suggestion = e.target.closest("[data-fill-title]");
    if (suggestion) fillFromTopic(suggestion);

    var change = e.target.closest("[data-quick-change]");
    if (change) showFormDetails(change.form, true);

    // Clerk owns the session, so signing out happens here and not on the server.
    if (e.target.closest("[data-sign-out]") && window.Clerk && window.Clerk.loaded) {
      window.Clerk.signOut().then(function () { location.href = "/"; });
    }
  });

  document.addEventListener("htmx:responseError", function (e) {
    if (e.detail.xhr.status === 401) {
      location.href = "/sign-in?return=" + encodeURIComponent(location.pathname + location.search);
    }
  });
})();
