// Live unread badges (nav and account menu) over Server-Sent Events.
// EventSource reconnects by itself.
(function () {
  var badges = document.querySelectorAll('[data-unread]');
  if (!badges.length || !window.EventSource) return;
  var baseTitle = document.title.replace(/^\(\d+\) /, '');
  var es = new EventSource('/notifications/stream');
  es.addEventListener('unread', function (e) {
    var d = JSON.parse(e.data);
    badges.forEach(function (b) {
      b.textContent = d.unread;
      b.hidden = d.unread === 0;
      if (d.latest) b.parentElement.title = d.latest.text;
    });
    document.title = d.unread > 0 ? '(' + d.unread + ') ' + baseTitle : baseTitle;
  });
})();
