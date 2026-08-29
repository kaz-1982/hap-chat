import { test, expect, type Page } from '@playwright/test';

// 実際の開発で踏んだバグは、ほとんどがこの層（ブラウザ上の JS）で起きた。
// 単体テストにも結合テストにも出てこないものを中心に確かめる。

const MESSAGES = '#messages';
const INPUT = 'メッセージを入力…';

let counter = 0;
/** テスト間で本文がぶつからないようにする */
function unique(label: string): string {
  counter += 1;
  return `${label}-${counter}-${test.info().parallelIndex}`;
}

async function joinAs(page: Page, name: string): Promise<void> {
  await page.goto('/');
  await page.getByLabel('表示名').fill(name);
  await page.getByRole('button', { name: '参加する' }).click();
  await page.waitForURL(/\/r\/general$/);
  await expect(page.locator(MESSAGES)).toBeVisible();
}

async function send(page: Page, text: string): Promise<void> {
  const input = page.getByPlaceholder(INPUT);
  await input.fill(text);
  await input.press('Enter');
  await expect(input).toHaveValue('');
}

test.describe('入室', () => {
  test('名前を入れて参加すると general に入る', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'HAP Chat' })).toBeVisible();

    // 名前が空のうちは参加できない
    await expect(page.getByRole('button', { name: '参加する' })).toBeDisabled();

    await page.getByLabel('表示名').fill('あかり');
    await expect(page.getByRole('button', { name: '参加する' })).toBeEnabled();
    await page.getByRole('button', { name: '参加する' }).click();

    await page.waitForURL(/\/r\/general$/);
    await expect(page.locator(MESSAGES)).toContainText('あかり さんが参加しました');
    await expect(page.locator('#presence')).toContainText('あかり');
  });

  test('色を選ぶとプレビューに反映される', async ({ page }) => {
    await page.goto('/');
    await page.getByLabel('表示名').fill('みどり');
    await page.locator('.swatch:has(input[value="jade"])').click();
    await expect(page.locator('.preview .avatar')).toHaveClass(/pico-background-jade-550/);
    await expect(page.locator('.preview .avatar')).toHaveText('み');
  });
});

test.describe('発言', () => {
  // Alpine の $el が input を指していて requestSubmit() が呼べていなかったバグの回帰テスト
  test('Enter キーで送信できる', async ({ page }) => {
    await joinAs(page, 'あかり');
    const text = unique('Enterで送信');
    await send(page, text);
    await expect(page.locator(MESSAGES)).toContainText(text);
  });

  test('送信ボタンでも送信できる', async ({ page }) => {
    await joinAs(page, 'あかり');
    const text = unique('ボタンで送信');
    await page.getByPlaceholder(INPUT).fill(text);
    await page.getByRole('button', { name: '送信' }).click();
    await expect(page.locator(MESSAGES)).toContainText(text);
  });

  // 入力欄の「入力中」通知（hx-post）が親フォームの htmx:afterRequest にも届き、
  // 下書きが消えてしまっていたバグの回帰テスト
  test('入力中の通知が下書きを消さない', async ({ page }) => {
    await joinAs(page, 'あかり');
    const input = page.getByPlaceholder(INPUT);
    await input.pressSequentially('まだ送らない', { delay: 50 });
    // 入力中通知の throttle（1.5 秒）を跨いでも残っていること
    await page.waitForTimeout(2500);
    await expect(input).toHaveValue('まだ送らない');
  });

  test('空のままでは送信できない', async ({ page }) => {
    await joinAs(page, 'あかり');
    await expect(page.getByRole('button', { name: '送信' })).toBeDisabled();
    await page.getByPlaceholder(INPUT).fill('   ');
    await expect(page.getByRole('button', { name: '送信' })).toBeDisabled();
  });

  test('本文の code とメンションが装飾される', async ({ page }) => {
    await joinAs(page, 'あかり');
    await send(page, '`go test ./...` を @あかり さんへ');
    const last = page.locator(`${MESSAGES} article`).last();
    await expect(last.locator('code')).toHaveText('go test ./...');
    await expect(last.locator('mark')).toHaveText('@あかり');
  });
});

test.describe('リアルタイム配信', () => {
  test('相手の発言が SSE で届き、自分の発言だけ is-mine が付く', async ({ browser }) => {
    const ctxA = await browser.newContext();
    const ctxB = await browser.newContext();
    const a = await ctxA.newPage();
    const b = await ctxB.newPage();

    await joinAs(a, 'あかり');
    await joinAs(b, 'ボブ');

    // 互いの在室が見えている
    await expect(a.locator('#presence')).toContainText('ボブ');
    await expect(b.locator('#presence')).toContainText('あかり');

    const text = unique('SSEで届く');
    await send(a, text);

    await expect(b.locator(MESSAGES)).toContainText(text);
    await expect(a.locator(`${MESSAGES} article`).last()).toHaveClass(/is-mine/);
    await expect(b.locator(`${MESSAGES} article`).last()).not.toHaveClass(/is-mine/);

    await ctxA.close();
    await ctxB.close();
  });

  test('相手が入力するとインジケータが出る', async ({ browser }) => {
    const ctxA = await browser.newContext();
    const ctxB = await browser.newContext();
    const a = await ctxA.newPage();
    const b = await ctxB.newPage();
    await joinAs(a, 'あかり');
    await joinAs(b, 'ボブ');
    await expect(a.locator('#presence')).toContainText('ボブ');

    await b.getByPlaceholder(INPUT).pressSequentially('入力中です', { delay: 60 });

    await expect(a.locator('#typing')).toContainText('ボブ');
    // 自分の入力中は自分には出ない
    await expect(b.locator('#typing')).not.toContainText('ボブ');

    await ctxA.close();
    await ctxB.close();
  });

  test('新着があるとトーストが出る', async ({ browser }) => {
    const ctxA = await browser.newContext();
    const ctxB = await browser.newContext();
    const a = await ctxA.newPage();
    const b = await ctxB.newPage();
    await joinAs(a, 'あかり');
    await joinAs(b, 'ボブ');
    await expect(a.locator('#presence')).toContainText('ボブ');

    const text = unique('トースト');
    await send(b, text);

    await expect(a.locator('.toasts .toast')).toContainText(text);
    await ctxA.close();
    await ctxB.close();
  });
});

test.describe('ルーム', () => {
  test('切り替えると URL と内容が変わり、発言が混ざらない', async ({ page }) => {
    await joinAs(page, 'あかり');

    await page.getByRole('link', { name: /#dev/ }).click();
    await page.waitForURL(/\/r\/dev$/);
    await expect(page.locator('.chat-head')).toContainText('#dev');

    const text = unique('devだけの発言');
    await send(page, text);
    await expect(page.locator(MESSAGES)).toContainText(text);

    await page.getByRole('link', { name: /#general/ }).click();
    await page.waitForURL(/\/r\/general$/);
    await expect(page.locator(MESSAGES)).not.toContainText(text);
  });

  test('切り替え後も SSE が繋がっている', async ({ page }) => {
    await joinAs(page, 'あかり');
    await page.getByRole('link', { name: /#random/ }).click();
    await page.waitForURL(/\/r\/random$/);

    // 接続インジケータが「接続中」を指していること
    await expect(page.locator('header .conn-dot')).toHaveClass(/is-on/);

    const text = unique('randomで発言');
    await send(page, text);
    await expect(page.locator(MESSAGES)).toContainText(text);
  });
});

test.describe('設定', () => {
  test('テーマの切り替えがリロード後も残る', async ({ page }) => {
    await joinAs(page, 'あかり');
    const before = await page.evaluate(() => document.documentElement.dataset.theme ?? '');

    await page.locator('.theme-switch input').click();
    const after = await page.evaluate(() => document.documentElement.dataset.theme ?? '');
    expect(after).not.toBe(before);
    expect(['light', 'dark']).toContain(after);

    await page.reload();
    await expect
      .poll(() => page.evaluate(() => document.documentElement.dataset.theme ?? ''))
      .toBe(after);
  });

  test('設定モーダルが開き、コンパクト表示が効く', async ({ page }) => {
    await joinAs(page, 'あかり');
    await page.locator('header button.icon-btn').click();

    const dialog = page.locator('#settings');
    await expect(dialog).toBeVisible();

    await dialog.getByText('コンパクト表示').click();
    await expect
      .poll(() => page.evaluate(() => document.documentElement.classList.contains('is-compact')))
      .toBe(true);

    await dialog.locator('footer').getByRole('button', { name: '閉じる' }).click();
    await expect(dialog).toBeHidden();
  });
});

test.describe('表示', () => {
  // CSS の scroll-behavior: smooth のせいで初期表示が最上部で止まっていたバグの回帰テスト
  test('再読み込みしても最新の発言が見えている', async ({ page }) => {
    await joinAs(page, 'あかり');
    for (let i = 0; i < 25; i += 1) {
      await send(page, `${unique('連投')}`);
    }
    await page.reload();
    await expect(page.locator(MESSAGES)).toBeVisible();

    await expect
      .poll(async () =>
        page.evaluate(() => {
          const box = document.getElementById('messages');
          if (!box) return false;
          return box.scrollHeight - box.scrollTop - box.clientHeight < 80;
        }),
      )
      .toBe(true);
  });

  test('絞り込みで一致しない発言が隠れる', async ({ page }) => {
    await joinAs(page, 'あかり');
    const hit = unique('みつかる');
    const miss = unique('かくれる');
    await send(page, hit);
    await send(page, miss);

    await page.getByPlaceholder('この部屋を絞り込み').fill(hit);
    await expect(page.locator(MESSAGES)).toContainText(hit);
    await expect(page.locator(`${MESSAGES} article:not(.is-hidden)`)).toHaveCount(1);

    await page.getByRole('button', { name: 'クリア' }).click();
    await expect(page.locator(MESSAGES)).toContainText(miss);
  });

  test('退室すると入室画面に戻る', async ({ page }) => {
    await joinAs(page, 'あかり');
    await page.locator('header details.dropdown').last().click();
    await page.getByRole('link', { name: '退室する' }).click();
    await page.waitForURL(/\/$/);
    await expect(page.getByRole('button', { name: '参加する' })).toBeVisible();
  });
});
