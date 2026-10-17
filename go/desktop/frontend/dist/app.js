// EntropyChain Miner UI. Talks to the Go backend through the bindings Wails
// injects at window.go.main.App (each method returns a Promise).
'use strict';

const $ = (id) => document.getElementById(id);
const App = () => window.go.main.App;
const RT = () => window.runtime;

let state = { accounts: [], active: '' };
let currentTab = 'mine';
let mining = false;

const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const errText = (e) => String(e?.message ?? e).replace(/^Error:\s*/, '');

function store(key, val) {
  try { if (val === undefined) return localStorage.getItem(key); localStorage.setItem(key, val); } catch { return null; }
}

// ---------- Toast & messages ----------
let toastTimer;
function toast(text, isErr = false) {
  const t = $('toast');
  t.textContent = text;
  t.classList.toggle('err', isErr);
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, 3500);
}
function msg(id, text, kind = '') {
  const el = $(id);
  el.textContent = text;
  el.className = 'msg' + (kind ? ' ' + kind : '');
}

// ---------- Dialogs ----------
function openDialog(id) { $(id).hidden = false; const f = $(id).querySelector('input'); if (f) f.focus(); }
function closeDialog(id) { $(id).hidden = true; }
document.querySelectorAll('[data-close]').forEach((b) => b.addEventListener('click', () => closeDialog(b.closest('.overlay').id)));
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  ['addDialog', 'exportDialog', 'sendNftDialog', 'confirmDialog'].forEach((id) => { if (!$(id).hidden) closeDialog(id); });
  $('manageMenu').hidden = true;
});

function confirmBox(title, text, yesLabel = 'Confirm') {
  return new Promise((resolve) => {
    $('confirmTitle').textContent = title;
    $('confirmText').textContent = text;
    $('confirmYes').textContent = yesLabel;
    openDialog('confirmDialog');
    const done = (v) => { closeDialog('confirmDialog'); $('confirmYes').onclick = $('confirmNo').onclick = null; resolve(v); };
    $('confirmYes').onclick = () => done(true);
    $('confirmNo').onclick = () => done(false);
  });
}

function segmented(container, onChange) {
  container.querySelectorAll('.seg-btn').forEach((b) => b.addEventListener('click', () => {
    container.querySelectorAll('.seg-btn').forEach((x) => x.classList.toggle('active', x === b));
    onChange(b.dataset.seg);
  }));
}

// ---------- State / accounts ----------
async function loadState() {
  state = await App().GetState();
  $('network').textContent = state.network || 'TESTNET';
  $('version').textContent = state.version;
  $('setupPath').textContent = state.walletPath;
  const slider = $('threads');
  slider.max = state.maxThreads;
  const saved = parseInt(store('threads') || '', 10);
  slider.value = Math.min(state.maxThreads, saved > 0 ? saved : Math.max(1, Math.floor(state.maxThreads / 2)));
  $('threadsVal').textContent = slider.value;
  if (state.error) toast(state.error, true);
  renderAccounts();
  $('setup').hidden = state.hasWallet;
}

function renderAccounts() {
  const sel = $('accountSelect');
  sel.innerHTML = (state.accounts || []).map((a) =>
    `<option value="${esc(a.address)}">${esc(a.label || 'account')} · ${esc(a.address.slice(0, 6))}…${esc(a.address.slice(-4))}</option>`).join('');
  sel.value = state.active;
  $('activeAddr').textContent = state.active || '—';
}

$('accountSelect').addEventListener('change', async (e) => {
  try {
    await App().SetActive(e.target.value);
    state.active = e.target.value;
    renderAccounts();
    if (mining) toast('Mining continues to the previous account until you restart mining.');
    refreshAll();
  } catch (err) { toast(errText(err), true); }
});

$('copyBtn').addEventListener('click', async () => {
  if (!state.active) return;
  await RT().ClipboardSetText(state.active);
  toast('Address copied');
});

// First-run setup
segmented($('setup'), (seg) => { $('setupCreate').hidden = seg !== 'create'; $('setupImport').hidden = seg !== 'import'; });
$('setupCreate').addEventListener('submit', async (e) => {
  e.preventDefault();
  try {
    await App().CreateAccount($('setupLabel').value.trim());
    await loadState();
    toast('Wallet created. Back it up from Manage → Download wallet backup.');
    refreshAll();
  } catch (err) { msg('setupMsg', errText(err), 'err'); }
});
$('setupImport').addEventListener('submit', async (e) => {
  e.preventDefault();
  try {
    await App().ImportAccount($('setupKey').value, $('setupImportLabel').value.trim());
    $('setupKey').value = '';
    await loadState();
    toast('Wallet imported');
    refreshAll();
  } catch (err) { msg('setupImportMsg', errText(err), 'err'); }
});

// Add account
let addMode = 'gen';
segmented($('addDialog'), (seg) => { addMode = seg; $('addKeyRow').hidden = seg !== 'imp'; });
$('addAccountBtn').addEventListener('click', () => { msg('addMsg', ''); $('addLabel').value = ''; $('addKey').value = ''; openDialog('addDialog'); });
$('addForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  try {
    const label = $('addLabel').value.trim();
    if (addMode === 'imp') await App().ImportAccount($('addKey').value, label);
    else await App().CreateAccount(label);
    $('addKey').value = '';
    closeDialog('addDialog');
    await loadState();
    toast('Account added');
    refreshAll();
  } catch (err) { msg('addMsg', errText(err), 'err'); }
});

// Manage menu
$('manageBtn').addEventListener('click', (e) => {
  e.stopPropagation();
  const m = $('manageMenu');
  m.hidden = !m.hidden;
  $('manageBtn').setAttribute('aria-expanded', String(!m.hidden));
});
document.addEventListener('click', () => { $('manageMenu').hidden = true; $('manageBtn').setAttribute('aria-expanded', 'false'); });
$('manageMenu').addEventListener('click', async (e) => {
  const action = e.target.dataset.action;
  if (!action) return;
  try {
    if (action === 'export') openExport();
    if (action === 'backup') { const p = await App().BackupWallet(); if (p) toast('Backup saved to ' + p); }
    if (action === 'folder') await App().OpenDataFolder();
    if (action === 'explorer') await App().OpenExplorer(state.active);
  } catch (err) { toast(errText(err), true); }
});

function openExport() {
  if (!state.active) return;
  $('exportAddr').textContent = state.active;
  $('exportConfirm').value = '';
  $('exportKey').hidden = true;
  $('exportKey').textContent = '';
  $('exportCopy').hidden = true;
  msg('exportMsg', '');
  openDialog('exportDialog');
}
$('exportConfirm').addEventListener('input', async (e) => {
  if (e.target.value.trim() !== 'CONFIRM') return;
  try {
    $('exportKey').textContent = await App().ExportKey(state.active);
    $('exportKey').hidden = false;
    $('exportCopy').hidden = false;
  } catch (err) { msg('exportMsg', errText(err), 'err'); }
});
$('exportCopy').addEventListener('click', async () => {
  await RT().ClipboardSetText($('exportKey').textContent);
  msg('exportMsg', 'Copied. Clear your clipboard when done.', 'ok');
});
// Forget the revealed key when the dialog closes.
new MutationObserver(() => { if ($('exportDialog').hidden) $('exportKey').textContent = ''; })
  .observe($('exportDialog'), { attributes: true, attributeFilter: ['hidden'] });

// ---------- Tabs ----------
document.querySelectorAll('.tab').forEach((t) => t.addEventListener('click', () => {
  currentTab = t.dataset.tab;
  lastNftSig = lastLotSig = '';
  document.querySelectorAll('.tab').forEach((x) => x.classList.toggle('active', x === t));
  ['mine', 'wallet', 'tokens', 'nfts', 'lotteries'].forEach((p) => { $('pane-' + p).hidden = p !== currentTab; });
  refreshAll();
}));

// ---------- Mine ----------
function fmtRate(h) {
  if (!h) return '—';
  if (h >= 1000) return (h / 1000).toFixed(2) + ' kH/s';
  return h.toFixed(1) + ' H/s';
}
function fmtAge(s) {
  if (s < 60) return s + 's ago';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
}

async function refreshStatus() {
  let s;
  try { s = await App().GetStatus(); } catch { return; }
  if (!s.ready) { $('sStatus').textContent = 'Starting…'; return; }
  mining = s.mining;
  const st = $('sStatus');
  if (s.mining && s.waiting) { st.textContent = 'Syncing…'; st.className = 'v warn'; }
  else if (s.mining) { st.textContent = 'Mining'; st.className = 'v ok'; }
  else if (s.peers === 0) { st.textContent = 'Connecting'; st.className = 'v warn'; }
  else { st.textContent = 'Synced · idle'; st.className = 'v'; }
  $('sHeight').textContent = s.height.toLocaleString();
  $('sPeers').textContent = s.peers;
  $('sRate').textContent = s.mining && !s.waiting ? fmtRate(s.hashRate) : '—';
  $('sMined').textContent = s.mined;
  $('sBalance').textContent = s.balance ? s.balance + ' ENTRO' : '—';
  $('sSub').textContent = `tip ${s.tipHash.slice(0, 16)}… · ${fmtAge(Math.max(0, s.tipAgeSec))} · difficulty ${s.difficulty.toLocaleString()} · mempool ${s.mempool}` +
    (s.mining ? ` · ${s.threads} thread${s.threads > 1 ? 's' : ''}` : '');
  const btn = $('mineBtn');
  btn.textContent = s.mining ? '■ Stop mining' : '▶ Start mining';
  btn.classList.toggle('stop', s.mining);
  $('threads').disabled = s.mining;
}

$('threads').addEventListener('input', (e) => { $('threadsVal').textContent = e.target.value; store('threads', e.target.value); });
$('mineBtn').addEventListener('click', async () => {
  try {
    if (mining) await App().StopMining();
    else await App().StartMining(parseInt($('threads').value, 10));
  } catch (err) { toast(errText(err), true); }
  refreshStatus();
});

const logEl = $('log');
function appendLog(line) {
  const atBottom = logEl.scrollTop + logEl.clientHeight >= logEl.scrollHeight - 20;
  logEl.textContent += line + '\n';
  if (logEl.textContent.length > 120000) logEl.textContent = logEl.textContent.slice(-90000);
  if (atBottom) logEl.scrollTop = logEl.scrollHeight;
}

// ---------- Wallet ----------
async function refreshWallet() {
  const [bals, hist] = await Promise.all([App().Balances(), App().History()]);
  const sel = $('sendToken');
  const prev = sel.value;
  sel.innerHTML = (bals || []).map((b) => `<option>${esc(b.token)}</option>`).join('');
  if ([...sel.options].some((o) => o.value === prev)) sel.value = prev;

  $('balances').innerHTML = (bals && bals.length) ? bals.map((b) =>
    `<div class="row-item"><span class="sym">${esc(b.token)}</span><span class="grow"></span><span class="num">${esc(b.display)}</span></div>`).join('')
    : '<div class="empty">No balances yet</div>';

  $('history').innerHTML = (hist && hist.length) ? hist.map((h) => {
    const dir = h.outgoing ? 'out' : 'in';
    return `<div class="row-item"><span class="dir ${dir}">${h.outgoing ? '↑' : '↓'}</span>
      <span class="grow"><div>${esc(h.text)}</div><div class="sub">Block #${h.height.toLocaleString()} · ${esc(h.hash.slice(0, 12))}…</div></span>
      <span class="num amt ${h.amount.startsWith('-') ? 'out' : 'in'}">${esc(h.amount)}</span></div>`;
  }).join('') : '<div class="empty">No activity yet — receive or mine to see history</div>';
}
$('refreshBtn').addEventListener('click', () => refreshWallet().catch((e) => toast(errText(e), true)));

$('sendForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const to = $('sendTo').value.trim(), amount = $('sendAmount').value.trim(), token = $('sendToken').value || 'ENTRO';
  const fee = parseInt($('sendFee').value || '0', 10);
  const ok = await confirmBox('Send ' + token, `Send ${amount} ${token} to ${to}?\nFee: ${fee} units. This cannot be undone.`, 'Send');
  if (!ok) return;
  msg('sendMsg', 'Submitting…');
  try {
    const hash = await App().Send(to, amount, token, fee);
    msg('sendMsg', 'Submitted ' + hash.slice(0, 16) + '… — it will appear after the next block.', 'ok');
    $('sendAmount').value = '';
  } catch (err) { msg('sendMsg', errText(err), 'err'); }
});

// ---------- Tokens ----------
async function refreshTokens() {
  const toks = await App().MyTokens();
  $('myTokens').innerHTML = (toks && toks.length) ? toks.map((t) =>
    `<div class="row-item"><span class="sym">${esc(t.symbol)}</span>
      <span class="grow"><div>${esc(t.name)}</div><div class="sub">${t.decimals} decimals · supply ${esc(t.supply)}</div></span>
      <span class="num">${esc(t.held)}</span></div>`).join('')
    : '<div class="empty">You haven\'t created any tokens yet</div>';
  const sel = $('mtSymbol');
  const prev = sel.value;
  sel.innerHTML = (toks || []).map((t) => `<option>${esc(t.symbol)}</option>`).join('');
  if ([...sel.options].some((o) => o.value === prev)) sel.value = prev;
}

$('createForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const sym = $('ctSymbol').value.trim().toUpperCase(), name = $('ctName').value.trim();
  const supply = $('ctSupply').value.trim(), dec = parseInt($('ctDecimals').value || '8', 10);
  const ok = await confirmBox('Create ' + sym, `Create token ${sym} ("${name}") with a supply of ${supply}? Fee: 100 units.`, 'Create');
  if (!ok) return;
  try {
    const hash = await App().CreateToken(sym, name, supply, dec);
    msg('createMsg', 'Submitted ' + hash.slice(0, 16) + '…', 'ok');
    e.target.reset();
    $('ctDecimals').value = 8;
  } catch (err) { msg('createMsg', errText(err), 'err'); }
});

$('mintForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const sym = $('mtSymbol').value, amount = $('mtAmount').value.trim(), to = $('mtTo').value.trim();
  if (!sym) { msg('mintMsg', 'Create a token first', 'err'); return; }
  const ok = await confirmBox('Mint ' + sym, `Mint ${amount} ${sym} to ${to || 'your account'}? Fee: 10 units.`, 'Mint');
  if (!ok) return;
  try {
    const hash = await App().MintToken(sym, amount, to);
    msg('mintMsg', 'Submitted ' + hash.slice(0, 16) + '…', 'ok');
    $('mtAmount').value = '';
  } catch (err) { msg('mintMsg', errText(err), 'err'); }
});

// ---------- NFTs ----------
// Seen per account, so arrivals and wins can be announced once.
let seenNfts = null, seenWins = null, seenFor = '';
let lastNftSig = '', lastLotSig = '';

function nftGradient(nft) {
  const c = (nft.colorSeed || '').match(/^[0-9a-f]{6}$/i) ? '#' + nft.colorSeed : null;
  if (c) return `linear-gradient(135deg, ${c}, #0c1118 85%)`;
  // No colour trait: derive two hues from the ids so cards still differ.
  let h = 0;
  for (const ch of nft.collection + '#' + nft.tokenId) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return `linear-gradient(135deg, hsl(${h % 360} 60% 40%), hsl(${(h >> 9) % 360} 50% 18%))`;
}

async function refreshNfts(render) {
  const nfts = (await App().MyNFTs()) || [];
  const badge = $('nftCount');
  badge.textContent = nfts.length;
  badge.hidden = nfts.length === 0;

  const keys = new Set(nfts.map((n) => n.collection + '#' + n.tokenId));
  if (seenFor === state.active && seenNfts) {
    nfts.filter((n) => !seenNfts.has(n.collection + '#' + n.tokenId))
      .forEach((n) => toast(`New NFT in this account: ${n.collection} #${n.tokenId}`));
  }
  seenNfts = keys;
  // Re-render only when something changed, so images do not flicker.
  const sig = state.active + JSON.stringify(nfts);
  if (!render || sig === lastNftSig) return;
  lastNftSig = sig;

  $('nftGrid').innerHTML = nfts.length ? nfts.map((n, i) => `
    <div class="nft-card">
      <div class="nft-art" style="background:${nftGradient(n)}" data-uri="${esc(n.metadataUri)}" data-i="${i}">
        <span class="glyph">⟁</span>${n.legendary ? '<span class="legend">★ Legendary</span>' : ''}
      </div>
      <div class="nft-body">
        <div class="nft-title">${esc(n.collection)} <span class="muted">#${esc(n.tokenId)}</span></div>
        <div class="nft-name" data-name="${i}"></div>
        <div class="nft-meta">Minted at block #${n.mintedAt.toLocaleString()}${n.random ? (n.pending
          ? ` · <span class="warn">traits revealed at block #${n.revealHeight.toLocaleString()}</span>`
          : ` · traits from block #${n.revealHeight.toLocaleString()}`) : ''}</div>
        <div class="traits">${(n.traits || []).map((t) => `<span class="trait">${esc(t.key)}: <b>${esc(t.value)}</b></span>`).join('')}</div>
        <button class="ghost small" data-send="${i}">Send…</button>
      </div>
    </div>`).join('')
    : '<div class="empty">No NFTs in this account yet. When someone sends you one, or you win one, it appears here.</div>';

  $('nftGrid').querySelectorAll('[data-send]').forEach((b) => b.addEventListener('click', () => openSendNft(nfts[+b.dataset.send])));
  // Load images from https metadata, in the background.
  $('nftGrid').querySelectorAll('.nft-art[data-uri]').forEach(async (el) => {
    const uri = el.dataset.uri;
    if (!uri.startsWith('https://')) return;
    try {
      const meta = await App().NFTMetadata(uri);
      const nameEl = $('nftGrid').querySelector(`[data-name="${el.dataset.i}"]`);
      if (meta.name && nameEl) nameEl.textContent = meta.name;
      if (meta.image) {
        const img = new Image();
        img.alt = meta.name || '';
        img.onload = () => { el.innerHTML = ''; el.appendChild(img); };
        img.src = meta.image;
      }
    } catch { /* keep the generated artwork */ }
  });
}

let sendingNft = null;
function openSendNft(nft) {
  sendingNft = nft;
  $('sendNftName').textContent = `${nft.collection} #${nft.tokenId}`;
  $('sendNftTo').value = '';
  msg('sendNftMsg', '');
  openDialog('sendNftDialog');
}
$('sendNftForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  if (!sendingNft) return;
  const to = $('sendNftTo').value.trim();
  try {
    const hash = await App().SendNFT(sendingNft.collection, sendingNft.tokenId, to);
    closeDialog('sendNftDialog');
    toast(`Sent ${sendingNft.collection} #${sendingNft.tokenId} · ${hash.slice(0, 12)}… — it leaves this account in the next block`);
  } catch (err) { msg('sendNftMsg', errText(err), 'err'); }
});

// ---------- Lotteries ----------
const lotteryStatus = {
  open: (l) => `Open · draws in ${l.blocksLeft.toLocaleString()} block${l.blocksLeft === 1 ? '' : 's'}`,
  closed: (l) => `Entries closed · result in ${l.blocksLeft.toLocaleString()} block${l.blocksLeft === 1 ? '' : 's'}`,
  ready: () => 'Ready to draw',
  drawn: (l) => `Drawn at block #${l.drawnAt.toLocaleString()}`,
};

async function refreshLotteries(render) {
  const lots = (await App().Lotteries()) || [];
  const joinable = lots.filter((l) => l.status === 'open' && !l.entered).length;
  const badge = $('lotCount');
  badge.textContent = joinable;
  badge.hidden = joinable === 0;

  const wins = new Set(lots.filter((l) => l.youWon).map((l) => l.id));
  if (seenFor === state.active && seenWins) {
    lots.filter((l) => l.youWon && !seenWins.has(l.id)).forEach((l) => toast(`You won lottery "${l.id}": ${l.prize} + ${l.pool}!`));
  }
  seenWins = wins;
  const sig = state.active + JSON.stringify(lots);
  if (!render || sig === lastLotSig) return;
  lastLotSig = sig;

  $('lotList').innerHTML = lots.length ? lots.map((l, i) => {
    let action = '';
    if (l.status === 'open' && !l.entered) action = `<button class="primary small" data-enter="${i}">Enter · ${esc(l.ticket)}</button>`;
    else if (l.status === 'ready') action = `<button class="ghost small" data-draw="${i}">Draw now</button>`;
    let badges = '';
    if (l.youWon) badges += '<span class="pill win">You won!</span>';
    else if (l.entered) badges += '<span class="pill">You\'re in</span>';
    if (l.isOrganizer) badges += '<span class="pill">Your lottery</span>';
    const winner = l.status === 'drawn' && !l.youWon && l.winner ? ` · winner ${esc(l.winner.slice(0, 8))}…${esc(l.winner.slice(-4))}` : '';
    return `<div class="row-item lot ${l.youWon ? 'won' : ''}">
      <span class="grow"><div><b>${esc(l.id)}</b> ${badges}</div>
        <div class="sub">${esc(lotteryStatus[l.status](l))} · prize ${esc(l.prize)} + pool ${esc(l.pool)} · ${l.entrants} entrant${l.entrants === 1 ? '' : 's'}${winner}${l.drawBlocks > 1 ? ` · ${l.drawBlocks}-block draw` : ''}</div></span>
      ${action}</div>`;
  }).join('') : '<div class="empty">No lotteries yet. Anyone can create one from the web wallet.</div>';

  $('lotList').querySelectorAll('[data-enter]').forEach((b) => b.addEventListener('click', async () => {
    const l = lots[+b.dataset.enter];
    const ok = await confirmBox('Enter ' + l.id, `Buy one ticket for ${l.ticket} (plus a 1-unit fee)? The prize is ${l.prize}, plus every ticket sold. The winner is drawn from future block beacons.`, 'Enter');
    if (!ok) return;
    try { await App().EnterLottery(l.id); toast(`Ticket bought for "${l.id}" — it counts once the next block is mined`); }
    catch (err) { toast(errText(err), true); }
  }));
  $('lotList').querySelectorAll('[data-draw]').forEach((b) => b.addEventListener('click', async () => {
    const l = lots[+b.dataset.draw];
    try { await App().DrawLottery(l.id); toast(`Draw submitted for "${l.id}" — the winner is announced in the next block`); }
    catch (err) { toast(errText(err), true); }
  }));
}

// ---------- Refresh loop ----------
let refreshing = false;
async function refreshAll() {
  if (refreshing) return;
  refreshing = true;
  try {
    await refreshStatus();
    if (state.active && currentTab === 'wallet') await refreshWallet();
    if (state.active && currentTab === 'tokens') await refreshTokens();
    // NFTs and lotteries refresh even in the background, so new arrivals
    // and wins show as badges and notifications on any tab.
    if (state.active) {
      if (seenFor !== state.active) { seenNfts = null; seenWins = null; }
      await refreshNfts(currentTab === 'nfts');
      await refreshLotteries(currentTab === 'lotteries');
      seenFor = state.active;
    }
  } catch (err) { console.error(err); } finally { refreshing = false; }
}

$('siteLink').addEventListener('click', (e) => { e.preventDefault(); App().OpenWebsite(); });

// The very first binding call can be lost if it is queued before the IPC
// channel is up (seen in `wails dev` browser mode), so retry it.
async function firstCall() {
  for (let i = 0; i < 20; i++) {
    const timeout = new Promise((_, rej) => setTimeout(() => rej(new Error('timeout')), 2000));
    try { return await Promise.race([App().GetState(), timeout]); } catch (e) { if (errText(e) !== 'timeout') throw e; }
  }
  throw new Error('Backend did not respond');
}

async function init() {
  await firstCall();
  await loadState();
  (await App().Logs()).forEach(appendLog);
  RT().EventsOn('log', appendLog);
  RT().EventsOn('tip', () => refreshAll());
  refreshAll();
  setInterval(refreshAll, 2000);
}

// Bindings are injected asynchronously; wait for them.
(function waitForBindings() {
  if (window.go && window.go.main && window.runtime) init().catch((e) => toast(errText(e), true));
  else setTimeout(waitForBindings, 50);
})();
