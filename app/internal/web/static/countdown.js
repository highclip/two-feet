// "Starts in N min" counts down on its own. It reads the time from the element, so cards that
// arrive later are picked up too. Under 5 minutes the chip turns red; once started it reads
// "Happening now".
(function () {
  "use strict";

  var SOON_MINUTES = 5;

  function tick() {
    document.querySelectorAll("[data-starts-at]").forEach(function (el) {
      var starts = Date.parse(el.dataset.startsAt);
      if (isNaN(starts)) return;
      var minutes = Math.ceil((starts - Date.now()) / 60000);
      el.classList.toggle("starts-soon", minutes > 0 && minutes < SOON_MINUTES);
      el.classList.toggle("starts-now", minutes <= 0);
      el.textContent = minutes > 0 ? "starts in " + minutes + " min" : "Happening now";
    });
  }

  setInterval(tick, 15000);
})();
