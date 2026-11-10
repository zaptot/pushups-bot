import './style.css';
import { DiscordSDK } from '@discord/embedded-app-sdk';
import { inDiscord, request, setToken, type AppConfig, type Session } from './api';
import { el, type Cleanup, type Screen } from './dom';
import { mountBlackjack } from './blackjack';
import { mountPlinko } from './plinko';
import { mountRocket } from './rocket';
import { mountRoulette } from './roulette';

interface Game {
  title: string;
  icon: string;
  mount: Screen;
}

const games: Game[] = [
  { title: 'Рулетка', icon: '🎡', mount: mountRoulette },
  { title: 'Plinko', icon: '🔻', mount: mountPlinko },
  { title: 'Блэкджек', icon: '🃏', mount: mountBlackjack },
  { title: 'Рокетка', icon: '🚀', mount: mountRocket },
];

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
  header.append(back, el('span', 'title', '🎰 Казино отжиманий'), el('span', 'user', user.name));
  const body = el('main', 'body');
  root.replaceChildren(header, body);

  const showMenu = (): void => {
    cleanup?.();
    cleanup = null;
    back.classList.add('hidden');
    const menu = el('div', 'menu');
    for (const game of games) {
      const card = el('button', 'card');
      card.append(el('span', 'card-icon', game.icon), el('span', 'card-title', game.title));
      card.addEventListener('click', () => {
        back.classList.remove('hidden');
        body.replaceChildren();
        cleanup = game.mount(body);
      });
      menu.append(card);
    }
    body.replaceChildren(menu);
  };

  back.addEventListener('click', showMenu);
  showMenu();
}

function renderNotCitizen(root: HTMLElement, user: Session['user']): void {
  const header = el('header', 'top');
  header.append(el('span', 'title', '🎰 Казино отжиманий'), el('span', 'user', user.name));
  const body = el('main', 'body not-citizen');
  const img = el('img', 'not-citizen-img');
  img.src = 'not-citizen.jpg';
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
