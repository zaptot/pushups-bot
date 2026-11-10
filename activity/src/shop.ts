import { request } from './api';
import { el, errorText, type Cleanup } from './dom';
import { points, setBalance } from './wallet';

interface ShopItem {
  key: string;
  title: string;
  price: number;
}

export function mountShop(root: HTMLElement): Cleanup {
  let disposed = false;
  const list = el('div', 'shop');
  const status = el('p', 'status', 'Товар выдаёт админ после покупки');

  const buy = async (item: ShopItem, button: HTMLButtonElement): Promise<void> => {
    if (button.dataset.armed !== '1') {
      button.dataset.armed = '1';
      button.textContent = `Точно купить за ${points(item.price)}?`;
      return;
    }
    button.dataset.armed = '';
    button.textContent = points(item.price);
    button.disabled = true;
    try {
      const res = await request<{ balance: number; order: number }>('POST', '/shop/buy', { item: item.key });
      setBalance(res.balance);
      if (!disposed) {
        status.textContent = `🛒 Заказ №${res.order}: «${item.title}». Админ выдаст, объявление в канале.`;
      }
    } catch (e) {
      if (!disposed) {
        status.textContent = errorText(e);
      }
    } finally {
      button.disabled = false;
    }
  };

  const layout = el('section', 'game shop-page');
  layout.append(list, status);
  root.append(layout);

  request<ShopItem[]>('GET', '/shop')
    .then((items) => {
      if (disposed) {
        return;
      }
      for (const item of items) {
        const row = el('div', 'shop-item');
        const button = el('button', 'primary', points(item.price));
        button.addEventListener('click', () => {
          void buy(item, button);
        });
        row.append(el('span', 'shop-title', item.title), button);
        list.append(row);
      }
    })
    .catch((e: unknown) => {
      status.textContent = errorText(e);
    });

  return () => {
    disposed = true;
  };
}
