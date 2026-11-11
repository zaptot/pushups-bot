// Ленты казино в меню: последние игры сервера и топ выигрышей за неделю.
import { request, type Feed, type FeedGame } from './api';
import { el } from './dom';

const refreshMs = 15_000;
const format = new Intl.NumberFormat('ru-RU');
const relative = new Intl.RelativeTimeFormat('ru', { numeric: 'auto' });
const medals = ['🥇', '🥈', '🥉'];

const games: Record<string, { icon: string; title: string }> = {
  roulette: { icon: '🎡', title: 'Рулетка' },
  plinko: { icon: '🔻', title: 'Plinko' },
  blackjack: { icon: '🃏', title: 'Блэкджек' },
  rocket: { icon: '🚀', title: 'Рокетка' },
};

function ago(at: string): string {
  const seconds = (new Date(at).getTime() - Date.now()) / 1000;
  const steps: [Intl.RelativeTimeFormatUnit, number][] = [
    ['day', 86_400],
    ['hour', 3_600],
    ['minute', 60],
  ];
  for (const [unit, size] of steps) {
    if (Math.abs(seconds) >= size) {
      return relative.format(Math.round(seconds / size), unit);
    }
  }
  return 'только что';
}

const signed = (n: number): string => (n > 0 ? `+${format.format(n)}` : n < 0 ? `−${format.format(-n)}` : '0');

function details(g: FeedGame): string {
  const parts = [`ставка ${format.format(g.stake)}`];
  if (g.multiplier > 0) {
    parts.push(`x${+g.multiplier.toFixed(2)}`);
  }
  parts.push(ago(g.at));
  return parts.join(' · ');
}

function row(g: FeedGame, lead: string): HTMLLIElement {
  const game = games[g.game] ?? { icon: '🎰', title: g.game };
  const item = el('li', 'feed-row');
  const icon = el('span', 'feed-lead', lead || game.icon);
  icon.title = game.title;
  const main = el('div', 'feed-main');
  const who = el('span', 'feed-name', g.name);
  const sub = el('span', 'feed-sub', `${game.title} · ${details(g)}`);
  main.append(who, sub);
  const right = el('div', 'feed-right');
  right.append(el('span', `feed-profit ${g.profit > 0 ? 'up' : g.profit < 0 ? 'down' : ''}`, signed(g.profit)));
  if (g.jackpot > 0) {
    right.append(el('span', 'feed-jackpot', `💎 ${format.format(g.jackpot)}`));
  }
  item.append(icon, main, right);
  return item;
}

function panel(title: string): { node: HTMLElement; list: HTMLOListElement; empty: HTMLParagraphElement } {
  const node = el('section', 'feed-panel');
  const list = el('ol', 'feed-list');
  const empty = el('p', 'feed-empty gone');
  node.append(el('h3', 'feed-title', title), list, empty);
  return { node, list, empty };
}

/** Блоки «Последние игры» и «Топ выигрышей»; обновляются сами, пока видны. */
export function casinoFeed(): HTMLElement {
  const box = el('div', 'feed');
  const recent = panel('🕹 Последние игры');
  const top = panel('🏆 Топ выигрышей за неделю');
  box.append(recent.node, top.node);

  const fill = (p: ReturnType<typeof panel>, items: FeedGame[], ranked: boolean, emptyText: string): void => {
    p.list.replaceChildren(...items.map((g, i) => row(g, ranked ? (medals[i] ?? String(i + 1)) : '')));
    p.empty.textContent = emptyText;
    p.empty.classList.toggle('gone', items.length > 0);
  };

  let timer = 0;
  const load = async (): Promise<void> => {
    if (!box.isConnected && timer) {
      clearInterval(timer);
      return;
    }
    try {
      const feed = await request<Feed>('GET', '/feed');
      fill(recent, feed.recent, false, 'Пока никто не играл — начни первым');
      fill(top, feed.top, true, `За ${feed.topDays} дней в плюс никто не вышел`);
    } catch (e) {
      console.log({ feedError: e instanceof Error ? e.message : e });
    }
  };
  void load();
  timer = window.setInterval(() => {
    if (document.visibilityState === 'visible') {
      void load();
    }
  }, refreshMs);
  return box;
}
