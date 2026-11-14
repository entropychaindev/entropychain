// EntropyChain Wallet Connect: lets a website ask the EntropyChain web wallet
// for the player's address and for approval of ENTRO payments. The private key
// never leaves the wallet; the user approves every payment in the wallet's
// own window.
//
//   <script src="https://wallet.entropychain.io/connect.js"></script>
//   const wallet = new EntropyWallet();
//   button.onclick = async () => {
//     const { address } = await wallet.connect();            // asks the user once
//     const { tx_hash } = await wallet.sendTransaction({        // asks every time
//       to: 'E…', amount: 100000000, data: { game: 'my-game', action: 'play' },
//     });
//   };
//
// Call connect() and sendTransaction() from a click handler: the wallet opens
// in a popup, and browsers only allow popups in response to a click.
// amount is in base units (1 ENTRO = 100000000). data adds labelled fields to
// the transfer: strings, safe integers, booleans or lists of strings.
(function () {
  'use strict';

  class EntropyWallet {
    constructor(walletUrl = 'https://wallet.entropychain.io/') {
      this.url = new URL(walletUrl, location.href).href.split('#')[0];
      this.origin = new URL(this.url).origin;
      this.win = null;
      this.readyP = null;
      this.pending = new Map();
      this.seq = 0;
      window.addEventListener('message', (e) => this._onMessage(e));
      // If the user closes the wallet window, fail whatever is still waiting.
      setInterval(() => {
        if (this.win && this.win.closed) {
          this.win = null;
          for (const p of this.pending.values()) p.reject(Object.assign(new Error('The wallet window was closed'), { code: 4900 }));
          this.pending.clear();
        }
      }, 800);
    }

    // Opens the wallet popup if it is not already open. Must run inside a click.
    _open() {
      if (this.win && !this.win.closed) return this.readyP;
      this.win = window.open(this.url + '#connect', 'entropy-wallet', 'popup,width=460,height=760');
      if (!this.win) throw Object.assign(new Error('Your browser blocked the wallet popup. Allow popups for this site and try again.'), { code: 4901 });
      this.readyP = new Promise((resolve) => { this._ready = resolve; });
      return this.readyP;
    }

    _onMessage(e) {
      const m = e.data;
      if (e.origin !== this.origin || !this.win || e.source !== this.win || !m || m.ec !== 1) return;
      if (m.method === 'ready') { this._ready && this._ready(); return; }
      const p = this.pending.get(m.id);
      if (!p) return;
      this.pending.delete(m.id);
      if (m.error) p.reject(Object.assign(new Error(m.error), { code: m.code }));
      else p.resolve(m.result);
    }

    _call(method, params) {
      let ready;
      try { ready = this._open(); } catch (err) { return Promise.reject(err); }
      const id = ++this.seq;
      return new Promise((resolve, reject) => {
        this.pending.set(id, { resolve, reject });
        ready.then(() => {
          this.win.postMessage({ ec: 1, id, method, params }, this.origin);
          try { this.win.focus(); } catch { /* ignore */ }
        });
      });
    }

    /** Resolves {address, network}. */
    connect() { return this._call('connect'); }

    /** Resolves {tx_hash, from}. */
    sendTransaction({ to, amount, data } = {}) { return this._call('sendTransaction', { to, amount, data }); }

    /** True while the wallet window is open. */
    get isOpen() { return !!(this.win && !this.win.closed); }
  }

  window.EntropyWallet = EntropyWallet;
})();
