// Hour view behaviour: screenshot scrubber and transcript-to-audio seeking.
(function () {
  "use strict";
  document.addEventListener("DOMContentLoaded", function () {
    var sc = document.querySelector(".scrubber");
    if (sc) {
      var cfg = JSON.parse(sc.getAttribute("data-screens"));
      var img = sc.querySelector("img"), range = sc.querySelector("input"), out = sc.querySelector("output");
      range.max = Math.max(cfg.files.length - 1, 0);
      range.addEventListener("input", function () {
        var f = cfg.files[range.value];
        img.src = "/media/" + cfg.day + "/screens/" + encodeURIComponent(f);
        out.textContent = f.replace(/\.jpg$/, "").replace(/(\d\d)(\d\d)(\d\d)/, "$1:$2:$3");
      });
    }
    document.querySelectorAll(".utt a[data-t]").forEach(function (a) {
      a.addEventListener("click", function (ev) {
        var t = parseInt(a.getAttribute("data-t"), 10), best = null;
        document.querySelectorAll('audio[data-track="' + a.getAttribute("data-track") + '"]').forEach(function (au) {
          var s = parseInt(au.getAttribute("data-start"), 10);
          if (s <= t && (!best || s > parseInt(best.getAttribute("data-start"), 10))) best = au;
        });
        if (best) {
          ev.preventDefault();
          best.currentTime = t - parseInt(best.getAttribute("data-start"), 10);
          best.play().catch(function () {});
        }
      });
    });
  });
})();
