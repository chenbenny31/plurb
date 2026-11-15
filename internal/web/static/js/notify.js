// Live inbox badge over Server-Sent Events. EventSource reconnects by itself.
(function () {
  var badge = document.getElementById('inboxCount');
  if (!badge || !window.EventSource) return;
  var baseTitle = document.title;
  var es = new EventSource('/notifications/stream');
  es.addEventListener('unread', function (e) {
    var d = JSON.parse(e.data);
    badge.textContent = d.unread;
    badge.hidden = d.unread === 0;
    document.title = d.unread > 0 ? '(' + d.unread + ') ' + baseTitle : baseTitle;
    if (d.latest) badge.parentElement.title = d.latest.text;
  });
})();
