// Matrix Digital Rain background animation
// https://github.com/Rezmason/matrix
(function () {
  var STORAGE_KEY = "matrix-background-enabled";
  var iframe = null;
  var iframeSrc = null;

  function isEnabled() {
    var stored = localStorage.getItem(STORAGE_KEY);
    // enabled by default if no preference saved
    return stored === null || stored === "true";
  }

  function buildIframeSrc() {
    var width = window.innerWidth;
    var numColumns = Math.max(15, Math.min(120, Math.round((width / 1600) * 60)));
    var params = new URLSearchParams({
      version: "classic",
      animationSpeed: "0.08",
      numColumns: numColumns.toString(),
      skipIntro: "true",
      backgroundColor: "0,0,0",
      // to help reduce performance cost
      fps: "15",
      // according to the github readme, this is more about bloom quality than size
      bloomSize: "0.1",
    });
    return "https://rezmason.github.io/matrix/?" + params.toString();
  }

  function createIframe() {
    if (iframe) return;
    if (!iframeSrc) iframeSrc = buildIframeSrc();
    iframe = document.createElement("iframe");
    iframe.id = "matrix-bg";
    iframe.src = iframeSrc;
    iframe.setAttribute("aria-hidden", "true");
    iframe.setAttribute("tabindex", "-1");
    iframe.setAttribute("sandbox", "allow-scripts");
    iframe.style.cssText = [
      "position:fixed",
      "top:0",
      "left:0",
      "width:100vw",
      "height:100vh",
      "border:none",
      "z-index:0",
      "pointer-events:none",
      "filter:blur(4px)brightness(0.5)",
    ].join(";");
    document.body.prepend(iframe);
  }

  function removeIframe() {
    if (!iframe) return;
    iframe.remove();
    iframe = null;
  }

  function setEnabled(enabled) {
    localStorage.setItem(STORAGE_KEY, enabled ? "true" : "false");
    if (enabled) {
      createIframe();
    } else {
      removeIframe();
    }
  }

  function replaceColorButton() {
    var colorDiv = document.getElementById("color");
    if (!colorDiv) return;

    var btn = colorDiv.querySelector("button");
    if (!btn) return;

    var toggleBtn = document.createElement("button");
    toggleBtn.type = "button";
    toggleBtn.title = "Toggle Matrix background";
    toggleBtn.setAttribute("aria-label", "Toggle Matrix background");
    // Copy the existing button's classes so it matches the footer style
    toggleBtn.className = btn.className;
    toggleBtn.innerHTML =
      '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" ' +
      'stroke="currentColor" stroke-width="2" stroke-linecap="round" ' +
      'stroke-linejoin="round" class="w-4 h-4">' +
      '<rect x="2" y="3" width="20" height="14" rx="2" ry="2"/>' +
      '<line x1="8" y1="21" x2="16" y2="21"/>' +
      '<line x1="12" y1="17" x2="12" y2="21"/>' +
      "</svg>";

    toggleBtn.addEventListener("click", function () {
      var nowEnabled = !isEnabled();
      setEnabled(nowEnabled);
      toggleBtn.style.opacity = nowEnabled ? "1" : "0.4";
    });

    toggleBtn.style.opacity = isEnabled() ? "1" : "0.4";

    // Replace the color button with our toggle
    btn.replaceWith(toggleBtn);
  }

  function init() {
    // Inject transparency styles
    var style = document.createElement("style");
    style.textContent = [
      "html, body, #__next, #page_wrapper, #inner_wrapper {",
      "  background: transparent !important;",
      "}",
      "#__next {",
      "  position: relative;",
      "  z-index: 1;",
      "}",
    ].join("\n");
    document.head.appendChild(style);

    // Create iframe if enabled
    if (isEnabled()) {
      createIframe();
    }

    // Replace the color button — Homepage renders async via React so poll briefly
    var attempts = 0;
    var interval = setInterval(function () {
      var colorDiv = document.getElementById("color");
      if (colorDiv && colorDiv.querySelector("button")) {
        clearInterval(interval);
        replaceColorButton();
      } else if (++attempts > 50) {
        clearInterval(interval);
      }
    }, 100);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
