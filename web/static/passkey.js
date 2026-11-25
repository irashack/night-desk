// Passkey sign-in and registration (server.auth.mode local with passkeys).
// The server sends WebAuthn options as JSON with base64url binary fields; this
// converts them for navigator.credentials and posts the result back.
(function () {
  'use strict';
  if (!window.PublicKeyCredential || !navigator.credentials) return;
  function bytes(v) {
    var s = atob(v.replace(/-/g, '+').replace(/_/g, '/') + '==='.slice((v.length + 3) % 4));
    var out = new Uint8Array(s.length);
    for (var i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
    return out.buffer;
  }
  function text(buf) {
    var b = new Uint8Array(buf), s = '';
    for (var i = 0; i < b.length; i++) s += String.fromCharCode(b[i]);
    return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  }
  function post(url, body) {
    return fetch(url, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      .then(function (r) { return r.json().catch(function () { return {}; }).then(function (j) { if (!r.ok) throw new Error(j.error || 'The passkey did not work.'); return j; }); });
  }
  function encode(cred) {
    var r = cred.response, out = { id: cred.id, rawId: text(cred.rawId), type: cred.type, response: { clientDataJSON: text(r.clientDataJSON) } };
    if (r.attestationObject) { out.response.attestationObject = text(r.attestationObject); if (r.getTransports) out.response.transports = r.getTransports(); }
    if (r.authenticatorData) { out.response.authenticatorData = text(r.authenticatorData); out.response.signature = text(r.signature); if (r.userHandle) out.response.userHandle = text(r.userHandle); }
    out.clientExtensionResults = cred.getClientExtensionResults ? cred.getClientExtensionResults() : {};
    return out;
  }
  function fail(el, err) {
    var p = document.querySelector('.error') || document.createElement('p');
    p.className = 'error'; p.setAttribute('role', 'alert');
    p.textContent = err && err.name === 'NotAllowedError' ? 'The passkey request was cancelled.' : (err && err.message) || 'The passkey did not work.';
    el.parentNode.insertBefore(p, el);
  }
  var login = document.querySelector('[data-passkey-login]');
  if (login) {
    login.hidden = false;
    var or = document.querySelector('.or'); if (or) or.hidden = false;
    login.addEventListener('click', function () {
      var form = login.getAttribute('data-passkey-login');
      post('/login/passkey/begin', { form: form }).then(function (o) {
        var pk = o.publicKey;
        pk.challenge = bytes(pk.challenge);
        (pk.allowCredentials || []).forEach(function (c) { c.id = bytes(c.id); });
        return navigator.credentials.get({ publicKey: pk });
      }).then(function (cred) {
        return post('/login/passkey/finish', { form: form, next: login.getAttribute('data-next') || '', credential: encode(cred) });
      }).then(function (j) { location.assign(j.next || '/'); }).catch(function (e) { fail(login, e); });
    });
  }
  var reg = document.querySelector('[data-passkey-register]');
  if (reg) {
    reg.hidden = false;
    reg.addEventListener('submit', function (ev) {
      ev.preventDefault();
      var csrf = reg.getAttribute('data-passkey-register'), name = reg.elements.name.value, password = reg.elements.password.value;
      post('/account/passkeys/begin', { csrf: csrf, password: password }).then(function (o) {
        var pk = o.publicKey;
        pk.challenge = bytes(pk.challenge);
        pk.user.id = bytes(pk.user.id);
        (pk.excludeCredentials || []).forEach(function (c) { c.id = bytes(c.id); });
        return navigator.credentials.create({ publicKey: pk });
      }).then(function (cred) {
        return post('/account/passkeys/finish', { csrf: csrf, name: name, credential: encode(cred) });
      }).then(function () { location.reload(); }).catch(function (e) { fail(reg, e); });
    });
  }
})();
