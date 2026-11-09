// Rotates the post box's placeholder through real posts from the room, while the box is empty.
(function () {
  "use strict";

  var ROTATE_MS = 4000;
  var box = document.querySelector("textarea[data-placeholders]");
  if (!box) return;

  var placeholders;
  try {
    placeholders = JSON.parse(box.dataset.placeholders);
  } catch (e) {
    return;
  }
  if (!Array.isArray(placeholders) || placeholders.length < 2) return;

  var next = 1;
  setInterval(function () {
    if (box.value !== "" || document.hidden) return;
    box.placeholder = placeholders[next];
    next = (next + 1) % placeholders.length;
  }, ROTATE_MS);
})();
