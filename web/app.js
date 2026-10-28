'use strict';
const $ = id => document.getElementById(id);
let busy = false;
function notify(text, error = false) { $('msg').textContent = text; $('msg').className = error ? 'err' : ''; $('msg').hidden = !text; }
function td(tr, text, cls) { const c = tr.insertCell(); c.textContent = text; if (cls) c.className = cls; return c; }
function btn(label, fn, cls = 'secondary') { const b = document.createElement('button'); b.type = 'button'; b.textContent = label; b.onclick = fn; b.className = cls; return b; }
function show(loggedIn) { $('login').hidden = loggedIn; $('app').hidden = !loggedIn; $('logout').hidden = !loggedIn; if (!loggedIn) { $('qr').close(); $('qrImg').removeAttribute('src'); } }
async function api(method, url, body) {
 const r = await fetch(url, {method, headers: {'Content-Type':'application/json'}, body:body === undefined ? undefined : JSON.stringify(body)});
 if (r.status === 401 && url !== '/api/login') { show(false); throw new Error('Your session has expired. Please log in again.'); }
 const j = await r.json().catch(() => ({}));
 if (!r.ok) throw new Error(j.error || r.statusText);
 return j;
}
async function act(fn, message = '', reload = true) {
 if (busy) return;
 busy = true; notify('');
 const controls = [...document.querySelectorAll('button')];
 const states = controls.map(b => b.disabled); controls.forEach(b => b.disabled = true);
 try { await fn(); if (reload) await load(); notify(message); }
 catch (e) { notify(e.message, true); }
 finally { controls.forEach((b, i) => b.disabled = states[i]); busy = false; $('addPeer').disabled = !$('userSvc').value; }
}
function empty(body, cols, message) { const tr = body.insertRow(); const c = td(tr,message,'empty'); c.colSpan = cols; }
function choice(container, name, checked, detail) {
 const label = document.createElement('label'); label.className = 'check';
 const input = document.createElement('input'); input.type = 'checkbox'; input.value = name; input.checked = checked;
 const text = document.createElement('span'); text.textContent = name;
 if (detail) { const small = document.createElement('small'); small.textContent = detail; text.append(small); }
 label.append(input,text); container.append(label);
}
async function load() {
 const [svcs, users, settings, interfaces] = await Promise.all([api('GET','/api/services'),api('GET','/api/users'),api('GET','/api/settings'),api('GET','/api/interfaces')]);
 show(true);
 $('serviceCount').textContent = svcs.length; $('peerCount').textContent = users.length;
 $('backend').textContent = settings.systemd ? 'Managed by systemd' : 'Direct interface management';
 $('lifecycleHint').textContent = settings.systemd ? 'Start enables an interface at boot. Stop disables it. Peer changes reload without dropping connections.' : 'Interfaces run in this container. Boot persistence requires a native systemd installation.';
 const sb = $('services'); sb.replaceChildren();
 const previous = $('userSvc').value; $('userSvc').replaceChildren();
 if (!svcs.length) { empty(sb,6,'No interfaces yet. Add one below to get started.'); $('userSvc').add(new Option('Add an interface first','')); }
 for (const s of svcs) {
  const tr = sb.insertRow(); td(tr,s.name); td(tr,s.address,'address');
  const endpoint = td(tr,String(s.listenPort)); const hint = document.createElement('small'); hint.textContent = s.endpoint || 'No public endpoint'; endpoint.append(hint);
  td(tr,s.users); const st = tr.insertCell(); const badge = document.createElement('span'); badge.textContent = s.active ? 'Running' : 'Stopped'; badge.className = 'badge ' + (s.active ? 'on' : 'off'); st.append(badge);
  if (settings.systemd) { const boot = document.createElement('span'); boot.className='boot'; boot.textContent = s.enabled ? 'Starts at boot' : 'Boot disabled'; st.append(boot); }
  const a = tr.insertCell(); a.className='actions';
  const action = name => act(() => api('POST',`/api/services/${encodeURIComponent(s.name)}/${name}`));
  a.append(btn(s.active ? 'Stop' : 'Start',() => action(s.active ? 'stop' : 'start')));
  if (s.active) a.append(btn('Restart',() => action('restart')));
  a.append(btn('Delete',() => confirm(`Delete ${s.name} and all its peers? This also disables it at boot.`) && act(() => api('DELETE',`/api/services/${encodeURIComponent(s.name)}`)),'danger'));
  $('userSvc').add(new Option(s.name,s.name));
 }
 if (svcs.some(s => s.name === previous)) $('userSvc').value=previous;
 $('addPeer').disabled = !svcs.length;
 const ub = $('users'); ub.replaceChildren();
 if (!users.length) empty(ub,4,'No peers yet. Add a device to generate its configuration.');
 for (const u of users) {
  const tr = ub.insertRow(); td(tr,u.name); td(tr,u.service); td(tr,u.address,'address');
  const a = tr.insertCell(); a.className='actions';
  a.append(btn('QR code',() => { $('qrImg').src=`/api/users/${encodeURIComponent(u.id)}/qr.png`; $('qr').showModal(); }));
  const dl = document.createElement('a'); dl.href=`/api/users/${encodeURIComponent(u.id)}/config`; dl.textContent='Download'; dl.className='button-link'; a.append(dl);
  a.append(btn('Delete',() => confirm(`Delete peer ${u.name}?`) && act(() => api('DELETE',`/api/users/${encodeURIComponent(u.id)}`)),'danger'));
 }
 const cfg=settings.mdns;
 $('mdnsEnabled').checked=cfg.enabled;
 $('mdnsUpstreams').replaceChildren(); $('mdnsServices').replaceChildren();
 const lan=interfaces.filter(i => !svcs.some(s => s.name===i.name));
 for (const i of lan) choice($('mdnsUpstreams'),i.name,cfg.upstreams.includes(i.name),`${i.up ? 'Up' : 'Down'} · ${i.addresses.join(', ')}`);
 for (const name of cfg.upstreams) if (!lan.some(i => i.name===name)) choice($('mdnsUpstreams'),name,true,'Unavailable — deselect to remove');
 if (!lan.length && !cfg.upstreams.length) $('mdnsUpstreams').textContent='No LAN interfaces available.';
 for (const s of svcs) choice($('mdnsServices'),s.name,cfg.services.includes(s.name),s.active ? 'Running' : 'Stopped');
 if (!svcs.length) $('mdnsServices').textContent='Add a VPN interface first.';
}
function tab(settings) {
 $('networkPanel').hidden=settings; $('settingsPanel').hidden=!settings;
 $('networkTab').setAttribute('aria-pressed',String(!settings)); $('settingsTab').setAttribute('aria-pressed',String(settings));
}
$('networkTab').onclick=() => tab(false); $('settingsTab').onclick=() => tab(true);
$('qrClose').onclick=() => $('qr').close();
$('qr').addEventListener('close',() => $('qrImg').removeAttribute('src'));
$('loginForm').onsubmit=e => { e.preventDefault(); act(async () => { await api('POST','/api/login',{password:$('pw').value}); $('pw').value=''; },'',true); };
$('logout').onclick=() => act(async () => { await api('POST','/api/logout'); show(false); },'',false);
$('svcForm').onsubmit=e => { e.preventDefault(); const o=Object.fromEntries(new FormData(e.target)); o.listenPort=+o.listenPort; act(async () => { await api('POST','/api/services',o); e.target.reset(); },'Interface added. Start it when you are ready.'); };
$('userForm').onsubmit=e => { e.preventDefault(); act(async () => { await api('POST','/api/users',Object.fromEntries(new FormData(e.target))); e.target.elements.namedItem('name').value=''; },'Peer added. Download its configuration or scan the QR code.'); };
$('mdnsForm').onsubmit=e => {
 e.preventDefault();
 const values=id => [...$(id).querySelectorAll('input:checked')].map(c => c.value);
 const cfg={enabled:$('mdnsEnabled').checked,upstreams:values('mdnsUpstreams'),services:values('mdnsServices')};
 act(() => api('PUT','/api/settings/mdns',cfg),'Discovery settings saved.');
};
$('passwordForm').onsubmit=e => {
 e.preventDefault(); const o=Object.fromEntries(new FormData(e.target));
 if (o.newPassword!==o.confirmation) { notify('The new passwords do not match.',true); return; }
 act(async () => { await api('POST','/api/password',{currentPassword:o.currentPassword,newPassword:o.newPassword}); e.target.reset(); show(false); $('pw').focus(); },'Password changed. Log in with your new password.',false);
};
load().catch(e => { show(false); if (!e.message.includes('session has expired')) notify(e.message,true); });
