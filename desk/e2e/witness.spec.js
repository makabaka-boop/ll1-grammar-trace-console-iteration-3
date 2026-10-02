// @ts-check
import { test, expect } from '@playwright/test'

// 冲突来源见证：每条竞争规则标明 FIRST / FOLLOW 来源与传播链，
// 链环与表格、产生式列表共用同一产生式编号，点击可定位规则；
// 表格、见证与编辑中的文法始终属于同一次分析请求。

// A -> B 同时经 FIRST（B -> t）与 FOLLOW（B 可空，S -> A t）命中 M[A, t]
const DUAL = 'S -> A t\nA -> B\nA -> t\nB -> t\nB ->'
// 悬空 else：Q 的 e 列冲突，Q -> ε 经 FOLLOW 传播命中
const ELSE = 'P -> i b P Q\nQ -> e P\nQ ->\nP -> s'

test('冲突见证：双重来源保留两份，点击链环定位规则', async ({ page }) => {
  await page.goto('/')
  await page.getByTestId('grammar-input').fill(DUAL)
  await page.getByTestId('start-input').fill('S')
  await page.getByTestId('tokens-input').fill('t')
  await page.getByTestId('analyze-btn').click()

  await expect(page.getByTestId('badge-conflict')).toBeVisible()
  await expect(page.getByTestId('conflict-panel')).toContainText('M[A, t]')

  // 规则 #2 (A -> B) 同时经 FIRST 与 FOLLOW 命中：两份来源都保留
  const first = page.getByTestId('witness-2-first')
  const follow = page.getByTestId('witness-2-follow')
  await expect(first).toBeVisible()
  await expect(follow).toBeVisible()
  await expect(first).toContainText('FIRST 命中')
  await expect(first).toContainText('B 的 FIRST 含 t')
  await expect(follow).toContainText('FOLLOW 传播命中')
  await expect(follow).toContainText('可空')
  await expect(follow).toContainText('S -> A t')

  // 规则 #3 (A -> t) 只有 FIRST 来源
  await expect(page.getByTestId('witness-3-first')).toBeVisible()
  await expect(page.getByTestId('witness-3-follow')).toHaveCount(0)

  // 表格与见证共用产生式编号：M[A, t] 格内规则 2 仍只计一条
  const conflictCell = page.getByTestId('ll1-table').locator('td.cell-conflict')
  await expect(conflictCell).toHaveCount(1)
  await expect(conflictCell).toHaveText('2, 3')

  // 点击见证链中的 #4（B -> t）：产生式列表中规则 4 高亮定位
  await first.getByRole('button', { name: '#4', exact: true }).click()
  await expect(page.getByTestId('rule-4')).toHaveClass(/rule-highlight/)
  // 再点 #1（S -> A t）：高亮跟随移动
  await follow.getByRole('button', { name: '#1', exact: true }).click()
  await expect(page.getByTestId('rule-1')).toHaveClass(/rule-highlight/)
  await expect(page.getByTestId('rule-4')).not.toHaveClass(/rule-highlight/)
})

test('表格、见证与文法同属一次请求：编辑标过期，重新提交后一致更新', async ({ page }) => {
  await page.goto('/')

  // 第一次提交：冲突文法，见证与表格来自同一响应
  await page.getByTestId('grammar-input').fill(ELSE)
  await page.getByTestId('start-input').fill('P')
  await page.getByTestId('tokens-input').fill('i b s e s')
  await page.getByTestId('analyze-btn').click()
  await expect(page.getByTestId('conflict-panel')).toContainText('M[Q, e]')
  await expect(page.getByTestId('conflict-witnesses')).toContainText('FOLLOW 传播命中')
  await expect(page.getByTestId('witness-3-follow')).toContainText('Q 位于尾部')

  // 编辑文法但不重新提交：旧结果（表格 + 见证）原样保留并标记过期
  await page.getByTestId('grammar-input').fill('E -> A b\nA ->')
  await expect(page.getByTestId('stale-warn')).toBeVisible()
  await expect(page.getByTestId('conflict-panel')).toContainText('M[Q, e]')
  await expect(page.getByTestId('conflict-witnesses')).toContainText('FOLLOW 传播命中')

  // 重新提交：表格、见证与新文法同属新一次请求
  await page.getByTestId('start-input').fill('E')
  await page.getByTestId('tokens-input').fill('b')
  await page.getByTestId('analyze-btn').click()
  await expect(page.getByTestId('badge-accepted')).toBeVisible()
  await expect(page.getByTestId('conflict-panel')).toHaveCount(0)
  await expect(page.getByTestId('stale-warn')).toHaveCount(0)
})

test('迟到的旧响应不覆盖当前表格与见证（请求版本隔离）', async ({ page }) => {
  /** @type {() => void} */
  let release = () => {}
  const gate = new Promise(resolve => { release = resolve })
  /** @type {() => void} */
  let markDelivered = () => {}
  const delivered = new Promise(resolve => { markDelivered = resolve })
  await page.route('**/api/analyze', async (route) => {
    const body = route.request().postDataJSON()
    if (body.grammar.includes('E -> A b')) {
      // 第一个请求的响应挂起，直到测试放行
      const response = await route.fetch()
      await gate
      await route.fulfill({ response })
      markDelivered()
    } else {
      await route.continue()
    }
  })

  await page.goto('/')
  // 请求 1：LL(1) 文法，其响应被挂起
  await page.getByTestId('grammar-input').fill('E -> A b\nA ->')
  await page.getByTestId('tokens-input').fill('b')
  await page.getByTestId('analyze-btn').click()
  // 请求 2：冲突文法，先返回并展示
  await page.getByTestId('grammar-input').fill(ELSE)
  await page.getByTestId('start-input').fill('P')
  await page.getByTestId('analyze-btn').click()
  await expect(page.getByTestId('badge-conflict')).toBeVisible()
  await expect(page.getByTestId('conflict-panel')).toContainText('M[Q, e]')

  // 放行请求 1 的迟到响应，等它真正送达页面：仍不得覆盖请求 2 的结果
  release()
  await delivered
  await page.waitForTimeout(300)
  await expect(page.getByTestId('badge-conflict')).toBeVisible()
  await expect(page.getByTestId('conflict-panel')).toContainText('M[Q, e]')
  await expect(page.getByTestId('conflict-witnesses')).toContainText('FOLLOW 传播命中')
  await expect(page.getByTestId('badge-accepted')).toHaveCount(0)
})
