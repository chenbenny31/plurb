// Site-wide behaviour. Loaded with defer.
(function () {
  // Close open dropdown menus when clicking elsewhere.
  document.addEventListener('click', function (e) {
    document.querySelectorAll('details.menu[open]').forEach(function (m) {
      if (!m.contains(e.target)) m.removeAttribute('open');
    });
  });
})();
