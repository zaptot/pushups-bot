import { request, type BlackjackAction, type BlackjackState, type BlackjackStatus } from './api';
import { el, errorText, stakeError, stakeField, type Cleanup } from './dom';

const hidden = '??';
const suits: Record<string, string> = { s: '♠', h: '♥', d: '♦', c: '♣' };
const outcomes: Record<Exclude<BlackjackStatus, 'playing'>, string> = {
  blackjack: '🃏 Блэкджек!',
  win: '🤑 Победа!',
  push: '🤝 Ничья',
  lose: '💀 Дилер выиграл',
  bust: '💥 Перебор',
};

function cardNode(code: string, fresh: boolean): HTMLDivElement {
  const node = el('div', fresh ? 'card-face deal' : 'card-face');
  if (code === hidden) {
    node.classList.add('back');
    return node;
  }
  const suit = code[1];
  if (suit === 'h' || suit === 'd') {
    node.classList.add('red');
  }
  node.append(el('span', 'rank', code[0] === 'T' ? '10' : code[0]), el('span', 'suit', suits[suit] ?? suit));
  return node;
}

export function mountBlackjack(root: HTMLElement): Cleanup {
  let disposed = false;
  let state: BlackjackState | null = null;
  let shown = { player: 0, dealer: 0 };

  const dealerCards = el('div', 'hand');
  const dealerTotal = el('span', 'total');
  const playerCards = el('div', 'hand');
  const playerTotal = el('span', 'total');
  const status = el('p', 'status', 'Загрузка…');

  const stake = stakeField();
  const deal = el('button', 'primary', 'Раздать');
  const hit = el('button', 'bet', 'Ещё');
  const stand = el('button', 'bet', 'Хватит');
  const double = el('button', 'bet', 'Удвоить');
  const betPanel = el('div', 'controls');
  betPanel.append(stake.row, deal);
  const actionPanel = el('div', 'bets');
  actionPanel.append(hit, stand, double);

  const renderHand = (box: HTMLElement, cards: string[], before: number): void => {
    box.replaceChildren(...cards.map((code, i) => cardNode(code, i >= before)));
  };

  const setButtons = (busy: boolean): void => {
    const playing = state?.status === 'playing';
    deal.disabled = busy;
    hit.disabled = busy || !playing;
    stand.disabled = busy || !playing;
    double.disabled = busy || !playing || !state?.canDouble;
    betPanel.classList.toggle('gone', playing);
    actionPanel.classList.toggle('gone', !playing);
  };

  const render = (next: BlackjackState | null): void => {
    const sameHand = state !== null && next !== null && state.id === next.id;
    const before = sameHand ? shown : { player: 0, dealer: 0 };
    const wasHidden = sameHand && state?.dealer[1] === hidden;
    state = next;
    if (!next) {
      dealerCards.replaceChildren();
      playerCards.replaceChildren();
      dealerTotal.textContent = '';
      playerTotal.textContent = '';
      shown = { player: 0, dealer: 0 };
      setButtons(false);
      return;
    }
    renderHand(playerCards, next.player, before.player);
    renderHand(dealerCards, next.dealer, wasHidden && next.dealer[1] !== hidden ? 1 : before.dealer);
    shown = { player: next.player.length, dealer: next.dealer.length };
    dealerTotal.textContent = String(next.dealerTotal);
    playerTotal.textContent = String(next.playerTotal);
    if (next.status === 'playing') {
      status.textContent = `Ставка ${next.stake}. Ещё или хватит?`;
    } else {
      status.textContent = `${outcomes[next.status]} — ставка ${next.stake}, засчитано ${next.payout}. Осталось: ${next.remaining}`;
      console.log({ blackjack: next });
    }
    setButtons(false);
  };

  const call = async (method: 'GET' | 'POST', path: string, body?: unknown): Promise<void> => {
    setButtons(true);
    try {
      const next = await request<BlackjackState | null>(method, path, body);
      if (!disposed) {
        render(next ?? state);
      }
    } catch (e) {
      if (!disposed) {
        status.textContent = errorText(e);
        setButtons(false);
      }
    }
  };

  const act = (action: BlackjackAction): void => {
    if (state) {
      void call('POST', '/blackjack/action', { id: state.id, seq: state.seq, action });
    }
  };

  deal.addEventListener('click', () => {
    const amount = stake.read();
    if (amount === null) {
      status.textContent = stakeError;
      return;
    }
    render(null);
    void call('POST', '/blackjack', { stake: amount });
  });
  hit.addEventListener('click', () => act('hit'));
  stand.addEventListener('click', () => act('stand'));
  double.addEventListener('click', () => act('double'));

  const dealerRow = el('div', 'seat');
  dealerRow.append(el('span', 'hint', 'Дилер'), dealerTotal);
  const playerRow = el('div', 'seat');
  playerRow.append(el('span', 'hint', 'Ты'), playerTotal);
  const table = el('div', 'felt');
  table.append(dealerRow, dealerCards, playerRow, playerCards);
  const controls = el('div', 'controls');
  controls.append(betPanel, actionPanel, status);
  const layout = el('section', 'game blackjack');
  layout.append(table, controls);
  root.append(layout);
  setButtons(true);

  request<BlackjackState | null>('GET', '/blackjack')
    .then((current) => {
      if (disposed) {
        return;
      }
      if (current) {
        render(current);
      } else {
        status.textContent = 'Делай ставку';
        setButtons(false);
      }
    })
    .catch((e: unknown) => {
      if (!disposed) {
        status.textContent = errorText(e);
        setButtons(false);
      }
    });

  return () => {
    disposed = true;
  };
}
