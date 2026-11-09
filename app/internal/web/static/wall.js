// The Wall refetches its whole board, so this picks out what actually changed to animate it: a new
// space drops on and a seat that filled pops in. Everything else swaps in without moving.
(function () {
  "use strict";

  var board = document.getElementById("wall-board");
  if (!board) return;
  var before = null;

  function going() {
    var seen = {};
    board.querySelectorAll(".wall-note").forEach(function (note) {
      seen[note.dataset.space] = Number(note.dataset.going);
    });
    return seen;
  }

  board.addEventListener("htmx:beforeSwap", function () {
    before = going();
  });

  board.addEventListener("htmx:afterSwap", function () {
    if (!before) return;
    board.querySelectorAll(".wall-note").forEach(function (note) {
      var was = before[note.dataset.space];
      if (was === undefined) {
        note.classList.add("wall-drop");
        return;
      }
      var dots = note.querySelectorAll(".wall-dot-filled");
      for (var i = was; i < dots.length; i++) dots[i].classList.add("wall-seat-pop");
    });
  });
})();
