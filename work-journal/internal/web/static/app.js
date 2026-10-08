// Page behaviour: hour-view scrubber and transcript seeking, and live refresh.
(function () {
  "use strict";

  // initPage wires the interactive bits under root; run again after a swap.
  function initPage(root) {
    var sc = root.querySelector(".scrubber");
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
    root.querySelectorAll(".utt a[data-t]").forEach(function (a) {
      a.addEventListener("click", function (ev) {
        var t = parseInt(a.getAttribute("data-t"), 10), best = null;
        root.querySelectorAll('audio[data-track="' + a.getAttribute("data-track") + '"]').forEach(function (au) {
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
  }

  // ---- auto-refresh ----
  var POLL_MS = 30000;
  var busy = false;

  function revEl(doc) { return doc.getElementById("page-rev"); }

  // userIsBusy: a focused field/select (possibly mid-edit or with its list open)
  // or playing audio must not be torn down; the next tick retries.
  function userIsBusy() {
    var a = document.activeElement;
    if (a && /^(INPUT|SELECT|TEXTAREA)$/.test(a.tagName)) return true;
    var playing = false;
    document.querySelectorAll("main audio").forEach(function (au) { if (!au.paused && !au.ended) playing = true; });
    return playing;
  }

  function detailsKey(d, i) { return d.getAttribute("data-key") || "#" + i; }

  function saveOpen(main) {
    var m = {};
    main.querySelectorAll("details").forEach(function (d, i) { m[detailsKey(d, i)] = d.open; });
    return m;
  }

  function restoreOpen(main, m) {
    main.querySelectorAll("details").forEach(function (d, i) {
      var k = detailsKey(d, i);
      if (Object.prototype.hasOwnProperty.call(m, k)) d.open = m[k];
    });
  }

  function swapIn(html) {
    var doc = new DOMParser().parseFromString(html, "text/html");
    var next = doc.querySelector("main"), main = document.querySelector("main");
    if (!next || !main) return;
    var cur = revEl(document), fresh = revEl(doc);
    if (!fresh || (cur && cur.getAttribute("data-rev") === fresh.getAttribute("data-rev"))) return; // no loops
    var open = saveOpen(main), x = window.scrollX, y = window.scrollY;
    main.innerHTML = next.innerHTML;
    restoreOpen(main, open);
    if (doc.title) document.title = doc.title;
    initPage(main);
    if (window.WJChart) window.WJChart.init(main);
    window.scrollTo(x, y);
  }

  function check() {
    if (busy || document.visibilityState !== "visible") return;
    var cur = revEl(document);
    if (!cur) return;
    var url = cur.getAttribute("data-rev-url"), have = cur.getAttribute("data-rev");
    busy = true;
    fetch(url, { cache: "no-store" })
      .then(function (r) { if (!r.ok) throw new Error("rev " + r.status); return r.json(); })
      .then(function (v) {
        if (!v || !v.rev || v.rev === have) return null;
        if (userIsBusy()) return null; // retry next tick
        return fetch(location.pathname + location.search, { cache: "no-store", credentials: "same-origin" })
          .then(function (r) { if (!r.ok) throw new Error("page " + r.status); return r.text(); })
          .then(function (html) { if (!userIsBusy()) swapIn(html); });
      })
      .catch(function () { /* server restarting or offline: try again later */ })
      .then(function () { busy = false; });
  }

  document.addEventListener("DOMContentLoaded", function () {
    initPage(document);
    if (!revEl(document)) return;
    setInterval(check, POLL_MS);
    document.addEventListener("visibilitychange", function () {
      if (document.visibilityState === "visible") check();
    });
  });
})();
