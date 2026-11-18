// Progressive enhancement for the forum: vote without a reload, open reply
// boxes inline, and expand collapsed replies that a #c123 link points into.
// Everything still works without JS via plain forms and links.
(function () {
  document.querySelectorAll('.voter').forEach(function (box) {
    box.querySelectorAll('form').forEach(function (form) {
      form.addEventListener('submit', function (e) {
        e.preventDefault();
        var btn = form.querySelector('button');
        btn.disabled = true;
        fetch(form.action, {
          method: 'POST',
          headers: { 'Accept': 'application/json' },
          body: new URLSearchParams(new FormData(form)),
          credentials: 'same-origin'
        })
          .then(function (r) { return r.json(); })
          .then(function (res) {
            var flash = box.parentElement.querySelector('.vote-error');
            if (res.error) {
              if (!flash) {
                flash = document.createElement('span');
                flash.className = 'vote-error';
                box.after(flash);
              }
              flash.textContent = res.error;
              return;
            }
            if (flash) flash.remove();
            box.querySelector('.vote-score').textContent = res.score;
            // Update both buttons and the value each would send next.
            box.querySelectorAll('form').forEach(function (f) {
              var b = f.querySelector('button');
              var mine = Number(b.dataset.value);
              var active = res.value === mine;
              b.classList.toggle('voted', active);
              f.querySelector('input[name=value]').value = active ? 0 : mine;
            });
          })
          .catch(function () { form.submit(); })
          .finally(function () { btn.disabled = false; });
      });
    });
  });

  document.querySelectorAll('a[data-reply]').forEach(function (link) {
    link.addEventListener('click', function (e) {
      e.preventDefault();
      var actions = link.closest('.comment-actions');
      var existing = actions.nextElementSibling;
      if (existing && existing.classList.contains('comment-form')) {
        existing.querySelector('textarea').focus();
        return;
      }
      var thread = location.pathname.split('/').pop();
      var form = document.createElement('form');
      form.method = 'post';
      form.action = '/forum/t/' + thread + '/comments';
      form.className = 'comment-form';
      form.innerHTML = '<input type="hidden" name="parent_id"><textarea name="body" rows="3" required></textarea><button type="submit">Reply</button>';
      form.querySelector('input').value = link.dataset.reply;
      actions.after(form);
      form.querySelector('textarea').focus();
    });
  });

  // Replies start collapsed; open every ancestor of the comment in the URL.
  function reveal() {
    var target = location.hash && document.getElementById(location.hash.slice(1));
    if (!target) return;
    for (var el = target.parentElement; el; el = el.parentElement) {
      if (el.tagName === 'DETAILS') el.open = true;
    }
    target.scrollIntoView({ block: 'center' });
  }
  reveal();
  window.addEventListener('hashchange', reveal);
})();
