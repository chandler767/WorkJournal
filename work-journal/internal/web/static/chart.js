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

  function pad(n) { return (n < 10 ? "0" : "") + n; }

  // timeline draws spec = {from, to, day, segs:[{s,e,c,l,h}]} (minutes since midnight).
  function timeline(svg, spec) {
    var W = 1000, H = 64, top = 6, barH = 34;
    var from = spec.from * 60, span = (spec.to - spec.from) * 60;
    svg.setAttribute("viewBox", "0 0 " + W + " " + H);
    svg.setAttribute("preserveAspectRatio", "none");
    while (svg.firstChild) svg.removeChild(svg.firstChild);
    var x = function (m) { return ((m - from) / span) * W; };
    for (var h = spec.from; h <= spec.to; h++) {
      var gx = x(h * 60);
      el("line", { x1: gx, x2: gx, y1: top, y2: top + barH + 4, stroke: "currentColor", "stroke-opacity": 0.2 }, svg);
      if ((h - spec.from) % 2 === 0 || spec.to - spec.from <= 12) {
        var t = el("text", { x: Math.min(Math.max(gx, 12), W - 12), y: H - 8, "text-anchor": "middle" }, svg);
        t.textContent = pad(h);
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

  window.WJChart = { timeline: timeline, bar: bar };

  document.addEventListener("DOMContentLoaded", function () {
    document.querySelectorAll('[data-chart="timeline"]').forEach(function (n) {
      try { timeline(n, JSON.parse(n.getAttribute("data-spec"))); } catch (e) { console.error("timeline", e); }
    });
    document.querySelectorAll('[data-chart="bar"]').forEach(bar);
  });
})();
