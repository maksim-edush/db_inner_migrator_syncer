(() => {
  const root = document.documentElement;
  const toggle = document.getElementById("theme-toggle");
  if (!toggle) {
    return;
  }
  toggle.addEventListener("click", () => {
    const current = root.getAttribute("data-theme") || "light";
    const next = current === "dark" ? "light" : "dark";
    root.setAttribute("data-theme", next);
    try {
      localStorage.setItem("theme", next);
    } catch (e) {}
  });
})();
