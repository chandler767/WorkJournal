// Tiny dependency-free chart helpers: an SVG day timeline and horizontal bars.
(function () {
  "use strict";
  var NS = "http://www.w3.org/2000/svg";

  function el(name, attrs, parent) {
    var n = document.createElementNS(NS, name);
    Object.keys(attrs || {}).forEach(function (k) { n.setAttribute(k, attrs[k]); });
    if (parent) parent.appendChild(n);
    return n;
  }

  // edgeClass keeps labels near either end inside the axis: left-aligned at the start,
  // right-aligned at the end, centred elsewhere (frac is 0..1 along the axis).
  function edgeClass(frac) { return frac < 0.03 ? "first" : frac > 0.97 ? "last" : ""; }

  function pad(n) { return (n < 10 ? "0" : "") + n; }

  // timeline draws spec = {from, to, day, segs:[{s,e,c,l,h}]} (minutes since midnight).
  // The bars stretch with the SVG (preserveAspectRatio="none"); the hour
  // labels are HTML in the sibling .timeline-axis so text never distorts.
  function timeline(svg, spec) {
    var W = 1000, H = 44, top = 4, barH = 36;
    var from = spec.from * 60, span = (spec.to - spec.from) * 60;
    svg.setAttribute("viewBox", "0 0 " + W + " " + H);
    svg.setAttribute("preserveAspectRatio", "none");
    while (svg.firstChild) svg.removeChild(svg.firstChild);
    var axis = svg.nextElementSibling;
    if (!axis || !axis.classList.contains("timeline-axis")) {
      axis = document.createElement("div");
      axis.className = "timeline-axis";
      axis.setAttribute("aria-hidden", "true");
      svg.parentNode.insertBefore(axis, svg.nextSibling);
    }
    while (axis.firstChild) axis.removeChild(axis.firstChild);
    var x = function (m) { return ((m - from) / span) * W; };
    var narrow = (svg.getBoundingClientRect().width || W) < 480;
    var step = spec.to - spec.from <= 12 && !narrow ? 1 : (narrow && spec.to - spec.from > 12 ? 3 : 2);
    for (var h = spec.from; h <= spec.to; h++) {
      var gx = x(h * 60);
      el("line", { x1: gx, x2: gx, y1: top, y2: top + barH, stroke: "currentColor", "stroke-opacity": 0.2, "vector-effect": "non-scaling-stroke" }, svg);
      if ((h - spec.from) % step === 0) {
        var t = document.createElement("span");
        t.textContent = pad(h % 24);
        t.style.left = (gx / W * 100) + "%";
        t.className = edgeClass(gx / W);
        axis.appendChild(t);
      }
    }
    (spec.segs || []).forEach(function (s) {
      var a = el("a", { href: "/hour/" + spec.day + "/" + pad(s.h) }, svg);
      var r = el("rect", { "class": "seg", x: x(s.s), y: top, width: Math.max(x(s.e) - x(s.s), 1.5), height: barH, fill: s.c }, a);
      var tip = el("title", {}, r);
      tip.textContent = pad(Math.floor(s.s / 60)) + ":" + pad(s.s % 60) + " " + s.l;
    });
  }

  // bar fills a track element to pct percent with the given colour.
  function bar(node) {
    var fill = document.createElement("i");
    fill.style.width = Math.max(parseInt(node.getAttribute("data-pct"), 10) || 0, 1) + "%";
    fill.style.background = node.getAttribute("data-color") || "currentColor";
    node.appendChild(fill);
  }

  // init draws every timeline and bar under root (the whole document by default).
  function init(root) {
    root = root || document;
    root.querySelectorAll('[data-chart="timeline"]').forEach(function (n) {
      try { timeline(n, JSON.parse(n.getAttribute("data-spec"))); } catch (e) { console.error("timeline", e); }
    });
    root.querySelectorAll('[data-chart="bar"]').forEach(function (n) {
      if (!n.firstChild) bar(n);
    });
  }

  window.WJChart = { timeline: timeline, bar: bar, init: init };

  document.addEventListener("DOMContentLoaded", function () {
    init(document);
    var resizeTimer;
    window.addEventListener("resize", function () {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(function () { init(document); }, 150);
    });
  });
})();
