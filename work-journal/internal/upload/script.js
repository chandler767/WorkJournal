// Fills the time zone and prefills the start time from the chosen file:
// its last-modified time less its duration, where the browser gives them.
(function () {
  var form = document.getElementById("upload");
  if (!form) return;
  var start = form.elements.start, tz = form.elements.tz, file = form.elements.audio;
  try { tz.value = Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch (e) {}
  var touched = false;
  start.addEventListener("input", function () { touched = true; });
  function pad(n) { return (n < 10 ? "0" : "") + n; }
  function local(d) {
    return d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) +
      "T" + pad(d.getHours()) + ":" + pad(d.getMinutes());
  }
  if (!start.value) start.value = local(new Date());
  file.addEventListener("change", function () {
    var f = file.files && file.files[0];
    if (!f || touched) return;
    if (f.size > Number(form.dataset.max)) {
      document.getElementById("status").textContent = "That file is over the 1 GB limit.";
    }
    var modified = f.lastModified ? new Date(f.lastModified) : null;
    if (!modified) return;
    start.value = local(modified);
    var url = URL.createObjectURL(f), audio = document.createElement("audio");
    audio.preload = "metadata";
    audio.addEventListener("loadedmetadata", function () {
      if (!touched && isFinite(audio.duration) && audio.duration > 0) {
        start.value = local(new Date(modified.getTime() - audio.duration * 1000));
      }
      URL.revokeObjectURL(url);
    });
    audio.addEventListener("error", function () { URL.revokeObjectURL(url); });
    audio.src = url;
  });
  form.addEventListener("submit", function () {
    form.querySelector("button").disabled = true;
    document.getElementById("status").textContent = "Uploading… keep this page open.";
  });
})();
