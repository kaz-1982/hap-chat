/* ------------------------------------------------------------------
   HAP Chat — クライアント側の担当は 3 つだけ。
     htmx  : 通信と DOM の差し替え（SSE 含む）
     Alpine: 手元の UI 状態（設定・下書き・絞り込み）
     ここ  : 両者をつなぐ薄い糊
------------------------------------------------------------------- */

const STORAGE_KEY = 'hap.ui';
const DEFAULTS = {
  theme: 'auto',
  fontSize: 16,
  compact: false,
  showSystem: true,
  sound: false,
  toastOn: true,
  sendKey: 'enter',
};

function loadPrefs() {
  try {
    return { ...DEFAULTS, ...JSON.parse(localStorage.getItem(STORAGE_KEY) || '{}') };
  } catch {
    return { ...DEFAULTS };
  }
}

/* ---------- Pico のモーダル（公式の作法どおり html にクラスを付ける）---------- */

function openModal(id) {
  const dlg = document.getElementById(id);
  if (!dlg) return;
  const root = document.documentElement;
  root.classList.add('modal-is-open', 'modal-is-opening');
  setTimeout(() => root.classList.remove('modal-is-opening'), 400);
  dlg.setAttribute('open', '');
}

function closeModal(id) {
  const dlg = document.getElementById(id);
  if (!dlg) return;
  const root = document.documentElement;
  root.classList.add('modal-is-closing');
  setTimeout(() => {
    root.classList.remove('modal-is-closing', 'modal-is-open');
    dlg.removeAttribute('open');
  }, 400);
}

function closeTopModal() {
  const dlg = document.querySelector('dialog[open]');
  if (dlg) closeModal(dlg.id);
}

function focusComposer() {
  const input = document.querySelector('.composer-wrap input[name="text"]');
  if (input) input.focus();
}

/* ---------- 新着音（WebAudio で短いポン）---------- */

let audioCtx = null;
function blip() {
  try {
    audioCtx = audioCtx || new (window.AudioContext || window.webkitAudioContext)();
    const osc = audioCtx.createOscillator();
    const gain = audioCtx.createGain();
    osc.type = 'sine';
    osc.frequency.value = 660;
    gain.gain.setValueAtTime(0.0001, audioCtx.currentTime);
    gain.gain.exponentialRampToValueAtTime(0.12, audioCtx.currentTime + 0.01);
    gain.gain.exponentialRampToValueAtTime(0.0001, audioCtx.currentTime + 0.18);
    osc.connect(gain).connect(audioCtx.destination);
    osc.start();
    osc.stop(audioCtx.currentTime + 0.2);
  } catch { /* 音は出せなくても本質ではないので黙って無視 */ }
}

/* ---------- Alpine ---------- */

document.addEventListener('alpine:init', () => {
  Alpine.store('ui', {
    ...loadPrefs(),
    room: { id: '', name: '', icon: '' },
    me: '',
    connected: false,
    unread: 0,
    tick: 0,
    toasts: [],
    _seq: 0,

    init() {
      this.applyAll();
      // 相対時刻を 30 秒ごとに更新する（tick を触ると x-effect が走る）
      setInterval(() => this.tick++, 30000);
      // 設定は変更のたび保存
      Alpine.effect(() => {
        const snap = {};
        for (const k of Object.keys(DEFAULTS)) snap[k] = this[k];
        localStorage.setItem(STORAGE_KEY, JSON.stringify(snap));
      });
    },

    applyAll() { this.applyTheme(); this.applyFont(); this.applyCompact(); this.applySystem(); },

    isDark() {
      if (this.theme === 'auto') return matchMedia('(prefers-color-scheme: dark)').matches;
      return this.theme === 'dark';
    },
    setTheme(t) { this.theme = t; this.applyTheme(); },
    applyTheme() {
      const root = document.documentElement;
      if (this.theme === 'auto') delete root.dataset.theme;
      else root.dataset.theme = this.theme;
    },
    applyFont() { this.keepBottom(() => document.documentElement.style.setProperty('--app-font', this.fontSize + 'px')); },
    applyCompact() { this.keepBottom(() => document.documentElement.classList.toggle('is-compact', this.compact)); },
    applySystem() { this.keepBottom(() => document.documentElement.classList.toggle('is-nosys', !this.showSystem)); },

    // 表示設定を変えると高さが変わるので、最下部にいたなら追従させる
    keepBottom(change) {
      const box = document.getElementById('messages');
      const near = box && box.scrollHeight - box.scrollTop - box.clientHeight < 80;
      change();
      if (near) requestAnimationFrame(() => { box.scrollTop = box.scrollHeight; });
    },
    reset() { Object.assign(this, DEFAULTS); this.applyAll(); },

    relTime(ts) {
      this.tick; // 依存登録
      const d = new Date(ts);
      const s = (Date.now() - d.getTime()) / 1000;
      if (s < 60) return 'たった今';
      if (s < 3600) return Math.floor(s / 60) + ' 分前';
      if (s < 86400) return Math.floor(s / 3600) + ' 時間前';
      return d.toLocaleDateString('ja-JP');
    },

    toast(t) {
      if (!this.toastOn) return;
      const id = ++this._seq;
      this.toasts.push({ id, ...t });
      setTimeout(() => { this.toasts = this.toasts.filter((x) => x.id !== id); }, 4000);
    },

    bumpUnread() {
      this.unread++;
      document.title = `(${this.unread}) #${this.room.name} · HAP Chat`;
    },
    clearUnread() {
      this.unread = 0;
      document.title = `#${this.room.name} · HAP Chat`;
    },
  });

  /* 入室フォーム */
  Alpine.data('joinForm', () => ({
    name: '',
    color: 'azure',
    busy: false,
    touched: false,
    get valid() { return this.name.trim().length > 0; },
  }));

  /* ルーム 1 つ分のビュー。差し替えのたびに作り直される。 */
  Alpine.data('chatView', (room) => ({
    filter: '',
    init() {
      const ui = Alpine.store('ui');
      ui.room = { id: room.id, name: room.name, icon: room.icon };
      ui.me = room.me;
      ui.clearUnread();
      // レイアウト確定後に最下部へ。フォント読み込みで高さが変わるので数回試す。
      const snap = () => this.scrollBottom(false);
      this.$nextTick(() => { snap(); requestAnimationFrame(snap); setTimeout(snap, 200); });
    },
    scrollBottom(smooth = true) {
      const box = this.$refs.messages;
      if (!box) return;
      if (smooth) box.scrollTo({ top: box.scrollHeight, behavior: 'smooth' });
      else box.scrollTop = box.scrollHeight; // CSS の scroll-behavior に引っ張られない即時移動
    },
    applyFilter() {
      const q = this.filter.trim().toLowerCase();
      const box = this.$refs.messages;
      box.querySelectorAll('.msg').forEach((el) => {
        const hit = !q || (el.dataset.text || '').toLowerCase().includes(q);
        el.classList.toggle('is-hidden', !hit);
      });
      // 絞り込み中はシステムメッセージを隠す
      box.querySelectorAll('.msg-system').forEach((el) => el.classList.toggle('is-hidden', !!q));
      if (!q) this.scrollBottom(false);
    },
  }));

  /* 入力欄 */
  Alpine.data('composer', () => ({
    draft: '',
    pending: '',   // 送信中の本文。失敗したら書き戻す
    sending: false,
    emojis: ['👍', '🎉', '😄', '🙏', '🔥', '✅', '🚀', '💡', '❤️', '😂', '🤔', '👀'],
    insert(e) { this.draft += e; this.$refs.input.focus(); },

    onKey(ev) {
      if (ev.key !== 'Enter') return;
      const mod = ev.metaKey || ev.ctrlKey;
      const want = Alpine.store('ui').sendKey === 'enter' ? (!mod && !ev.shiftKey) : mod;
      ev.preventDefault();
      if (want) this.submit();
    },

    submit() {
      if (!this.draft.trim()) return;
      // $el はハンドラを書いた要素（= input）を指すので、フォームは $root で取る。
      // htmx が処理済みならここで POST が飛び、まだならフォームの action へ
      // ネイティブ送信される（どちらも同じ URL に POST される）。
      this.$root.requestSubmit();
      // 送信後に消す。順番が逆だと空の本文を送ってしまう。
      this.draft = '';
    },

    sent(ev) {
      this.sending = false;
      if (ev.detail.successful) {
        // 送信中に書き足していたら、それは消さない
        if (this.draft === this.pending) this.draft = '';
      } else {
        this.draft = this.pending; // 書いたものを失わせない
        Alpine.store('ui').toast({ name: '送信できませんでした', text: 'もう一度お試しください', color: 'pumpkin', initial: '!' });
      }
      this.pending = '';
      this.$refs.input.focus();
    },
  }));
});

/* ---------- htmx とのつなぎ ---------- */

let wasAtBottom = true;

document.addEventListener('DOMContentLoaded', () => {
  const body = document.body;

  // SSE の接続状態をヘッダーの点に反映する。
  // ルームを切り替えると新しい接続が開いたあとに古い接続の close が届くので、
  // 「いま生きている接続」だけを見る。そうしないと繋がっているのに切断表示になる。
  let activeSse = null;
  body.addEventListener('htmx:sseOpen', (e) => {
    activeSse = e.target;
    Alpine.store('ui').connected = true;
  });
  const dropped = (e) => {
    if (e.target === activeSse) Alpine.store('ui').connected = false;
  };
  body.addEventListener('htmx:sseError', dropped);
  body.addEventListener('htmx:sseClose', dropped);

  // 差し替え前に「一番下にいたか」を覚えておく
  body.addEventListener('htmx:beforeSwap', (e) => {
    const box = e.detail?.target || e.target;
    if (box.id !== 'messages') return;
    wasAtBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 80;
  });

  body.addEventListener('htmx:afterSwap', (e) => {
    const box = e.detail?.target || e.target;
    if (box.id !== 'messages') return;
    const ui = Alpine.store('ui');
    const last = box.lastElementChild;
    if (wasAtBottom) box.scrollTo({ top: box.scrollHeight, behavior: 'smooth' });

    if (!last || !last.classList.contains('msg') || last.classList.contains('is-mine')) return;
    const name = last.querySelector('strong')?.textContent ?? '';
    const text = last.dataset.text ?? '';
    const avatar = last.querySelector('.avatar');
    const color = (avatar?.className.match(/pico-background-([a-z]+)-/) || [, 'azure'])[1];
    ui.toast({ name, text, color, initial: name.charAt(0) });
    if (ui.sound) blip();
    if (document.hidden) ui.bumpUnread();
  });

  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) Alpine.store('ui').clearUnread();
  });
});
