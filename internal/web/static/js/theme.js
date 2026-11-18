// Loaded synchronously in <head> so the theme applies before first paint.
// Light by default; follows the OS dark setting until the reader picks one.
(function () {
  var root = document.documentElement;
  var saved = null;
  try { saved = localStorage.getItem('theme'); } catch (e) {}
  var dark = saved ? saved === 'dark'
    : !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
  if (dark) root.classList.add('dark');

  document.addEventListener('DOMContentLoaded', function () {
    var btn = document.getElementById('themeToggle');
    if (btn) btn.addEventListener('click', function () {
      var isDark = root.classList.toggle('dark');
      try { localStorage.setItem('theme', isDark ? 'dark' : 'light'); } catch (e) {}
    });
    // Close open dropdown menus when clicking elsewhere.
    document.addEventListener('click', function (e) {
      document.querySelectorAll('details.menu[open]').forEach(function (m) {
        if (!m.contains(e.target)) m.removeAttribute('open');
      });
    });
  });
})();
