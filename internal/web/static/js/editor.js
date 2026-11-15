// Editor helpers. Kept in a file (not inline) so the CSP can forbid inline scripts.
(function () {
  // Chips: data-fill replaces an input's value, data-append adds a word to it.
  document.querySelectorAll('.chip[data-value]').forEach(function (chip) {
    chip.addEventListener('click', function () {
      var value = chip.dataset.value;
      if (chip.dataset.fill) {
        document.getElementById(chip.dataset.fill).value = value;
        return;
      }
      var input = document.getElementById(chip.dataset.append);
      var words = input.value.split(/\s+/).filter(Boolean);
      if (words.indexOf(value) === -1) words.push(value);
      input.value = words.join(' ');
    });
  });

  // Destructive forms ask first.
  document.querySelectorAll('form[data-confirm]').forEach(function (form) {
    form.addEventListener('submit', function (e) {
      if (!window.confirm(form.dataset.confirm)) e.preventDefault();
    });
  });

  // Preview renders on the server with the same sanitising renderer as
  // published posts, so what you see is exactly what readers get.
  var toggle = document.getElementById('previewToggle');
  var body = document.getElementById('body');
  var preview = document.getElementById('bodyPreview');
  if (!toggle || !body || !preview) return;
  toggle.addEventListener('click', function () {
    if (!preview.hidden) {
      preview.hidden = true;
      body.hidden = false;
      toggle.textContent = 'Preview';
      return;
    }
    toggle.disabled = true;
    fetch('/panorama/editor/preview', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ body: body.value }),
      credentials: 'same-origin'
    })
      .then(function (r) { if (!r.ok) throw new Error(r.status); return r.text(); })
      .then(function (html) {
        preview.innerHTML = html;
        preview.hidden = false;
        body.hidden = true;
        toggle.textContent = 'Edit';
      })
      .catch(function () { toggle.textContent = 'Preview failed — retry'; })
      .finally(function () { toggle.disabled = false; });
  });
})();
