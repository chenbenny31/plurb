// Loaded synchronously in <head> so the saved theme applies before first paint.
(function () {
  var root = document.documentElement;
  try {
    if (localStorage.getItem('theme') === 'light') root.classList.add('light');
  } catch (e) {}
  document.addEventListener('DOMContentLoaded', function () {
    var btn = document.getElementById('themeToggle');
    if (!btn) return;
    btn.addEventListener('click', function () {
      var light = root.classList.toggle('light');
      try { localStorage.setItem('theme', light ? 'light' : 'dark'); } catch (e) {}
    });
  });
})();
