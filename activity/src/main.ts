import './style.css';
import { DiscordSDK } from '@discord/embedded-app-sdk';
import { inDiscord, onNotEnough, request, setToken, type AppConfig, type Session } from './api';
import { el, type Cleanup, type Screen } from './dom';
import { mountBlackjack } from './blackjack';
import { mountPlinko } from './plinko';
import { mountRocket } from './rocket';
import { mountRoulette } from './roulette';
import { showTooPoor } from './poor';
import { mountShop } from './shop';
import { jackpotBanner, startJackpotPolling } from './jackpot';
import { refreshBalance, showBalanceIn } from './wallet';

interface Game {
  id: string;
  title: string;
  blurb: string;
  icon: string;
  mount: Screen;
}

const games: Game[] = [
  { id: 'roulette', title: 'Рулетка', blurb: 'Цвет, чёт или число — до x36', icon: '🎡', mount: mountRoulette },
  { id: 'plinko', title: 'Plinko', blurb: 'Шарик сам решит, сколько дать', icon: '🔻', mount: mountPlinko },
  { id: 'blackjack', title: 'Блэкджек', blurb: 'Набери 21 и обыграй дилера', icon: '🃏', mount: mountBlackjack },
  { id: 'rocket', title: 'Рокетка', blurb: 'Забери до взрыва — до x100', icon: '🚀', mount: mountRocket },
  { id: 'shop', title: 'Магазин', blurb: 'Потрать поинты на призы', icon: '🛒', mount: mountShop },
];

function titleNode(): HTMLSpanElement {
  const title = el('span', 'title');
  title.append(el('span', 'title-icon', '🎰 '), el('span', 'title-text', 'Казино отжиманий'));
  return title;
}

const app = document.querySelector<HTMLDivElement>('#app');

async function login(): Promise<Session> {
  const config = await request<AppConfig>('GET', '/config');
  if (inDiscord) {
    if (!config.clientId) {
      throw new Error('Бот ещё не подключился к Discord, попробуй через минуту');
    }
    const sdk = new DiscordSDK(config.clientId);
    await sdk.ready();
    const { code } = await sdk.commands.authorize({
      client_id: config.clientId,
      response_type: 'code',
      state: '',
      prompt: 'none',
      scope: ['identify'],
    });
    return request<Session>('POST', '/session', { code, guildId: sdk.guildId });
  }
  if (!config.dev) {
    throw new Error('Режим разработки выключен: задай ACTIVITY_DEV_USER_ID');
  }
  return request<Session>('POST', '/session');
}

function render(root: HTMLElement, user: Session['user']): void {
  let cleanup: Cleanup | null = null;
  const header = el('header', 'top');
  const back = el('button', 'back hidden', '← Меню');
  const balance = el('span', 'balance');
  balance.title = 'Баланс, поинтов';
  const who = el('div', 'who');
  who.append(el('span', 'user', user.name), balance);
  header.append(back, titleNode(), who);
  showBalanceIn(balance);
  void refreshBalance();
  startJackpotPolling();
  const body = el('main', 'body');
  root.replaceChildren(header, body);

  const showMenu = (): void => {
    cleanup?.();
    cleanup = null;
    back.classList.add('hidden');
    header.classList.remove('in-game');
    const menu = el('div', 'menu');
    for (const game of games) {
      const card = el('button', `card ${game.id}`);
      const text = el('span', 'card-text');
      text.append(el('span', 'card-title', game.title), el('span', 'card-blurb', game.blurb));
      card.append(el('span', 'card-icon', game.icon), text);
      card.addEventListener('click', () => {
        back.classList.remove('hidden');
        header.classList.add('in-game');
        body.replaceChildren();
        cleanup = game.mount(body);
      });
      menu.append(card);
    }
    const lobby = el('div', 'lobby');
    lobby.append(jackpotBanner(), menu);
    body.replaceChildren(lobby);
  };

  back.addEventListener('click', showMenu);
  showMenu();
}

function renderNotCitizen(root: HTMLElement, user: Session['user']): void {
  const header = el('header', 'top');
  const who = el('div', 'who');
  who.append(el('span', 'user', user.name));
  header.append(el('span'), titleNode(), who);
  const body = el('main', 'body not-citizen');
  const img = el('img', 'not-citizen-img');
  img.src = 'not-citizen.png';
  img.alt = 'Сори, пупсик, но ты даже не гражданин';
  body.append(img, el('p', 'status', 'Играть могут только граждане'));
  root.replaceChildren(header, body);
}

async function start(root: HTMLElement): Promise<void> {
  try {
    const session = await login();
    if (!session.citizen || !session.token) {
      renderNotCitizen(root, session.user);
      return;
    }
    setToken(session.token);
    onNotEnough(() => {
      showTooPoor();
      void refreshBalance();
    });
    render(root, session.user);
  } catch (e) {
    console.log({ loginError: e instanceof Error ? e.message : e });
    const box = el('div', 'loading');
    box.append(el('p', 'status error', e instanceof Error ? e.message : 'Не удалось войти'));
    root.replaceChildren(box);
  }
}

if (app) {
  void start(app);
}
