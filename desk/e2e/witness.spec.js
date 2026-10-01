// @ts-check
import { test, expect } from '@playwright/test'

// 冲突来源见证的浏览器验收：
//   表格、见证与编辑中的文法必须始终属于同一次分析请求（请求版本隔离已有 stale 提示）；
//   见证区分 FIRST 命中与“可空后 FOLLOW 传播”命中，传播链编号全部指向本次分析的产生式；
//   点击见证编号可定位（滚动 + 高亮）到产生式面板中的对应规则。

// 悬空 else：M[Q,e]，Q->e P 经 FIRST 命中，Q->ε 经 FOLLOW 传播命中
const CONFLICT_GRAMMAR = 'P -> i b P Q\nQ -> e P\nQ ->\nP -> s'

// 双重来源：A->B 既经右部 FIRST 含 t 命中，又因右部可空（B->ε）经 FOLLOW(A) 含 t 命中；
// 格内只计两条不同规则（A->B、A->t），但来源见证保留三份。
const DUAL_GRAMMAR = 'S -> A t\nS -> u\nA -> B\nA -> t\nB -> t\nB ->'

test.beforeEach(async ({ page }) => {
  await page.goto('/')
})

test('冲突示例：FIRST 与 FOLLOW 两类来源见证齐全，链可追溯到本次分析的产生式', async ({ page }) => {
  await page.getByTestId('grammar-input').fill(CONFLICT_GRAMMAR)
  await page.getByTestId('start-input').fill('P')
  await page.getByTestId('tokens-input').fill('i')
  await page.getByTestId('analyze-btn').click()

  await expect(page.getByTestId('badge-conflict')).toBeVisible()
  const panel = page.getByTestId('conflict-panel')
  await expect(panel).toContainText('M[Q, e]')

  // 两个产生式分组（与表格格内编号 [2, 3] 一致）
  const g2 = page.getByTestId('witness-group-2')
  const g3 = page.getByTestId('witness-group-3')
  await expect(g2).toBeVisible()
  await expect(g3).toBeVisible()
  await expect(g2.getByTestId('witness-rule-text-2')).toContainText('Q -> e P')
  await expect(g3.getByTestId('witness-rule-text-3')).toContainText('Q -> ε')

  // 规则 2：右部 FIRST 命中，单环 first-term 指向自身
  const firstRoute = page.getByTestId('witness-route-2-first')
  await expect(firstRoute).toContainText('右部 FIRST 命中')
  await expect(firstRoute).toContainText('右部第 1 位终结符 e')
  await expect(firstRoute.getByTestId('chain-pid-2-0')).toContainText('2')

  // 规则 3：可空后经 FOLLOW 命中，链完整（entry -> propagate -> follow-first -> first-term）
  const followRoute = page.getByTestId('witness-route-3-follow')
  await expect(followRoute).toContainText('可空后经 FOLLOW 命中')
  await expect(followRoute).toContainText('空右部天然可空')
  await expect(followRoute).toContainText('FOLLOW(P) 回填 FOLLOW(Q)')
  await expect(followRoute).toContainText('e 加入 FOLLOW(P)')
  // 链上产生式编号都是本次分析的产生式
  for (const testid of ['chain-pid-3-0', 'chain-pid-3-1', 'chain-pid-3-2', 'chain-pid-3-3']) {
    await expect(followRoute.getByTestId(testid)).toBeVisible()
  }

  // 点击链中产生式 1 的编号：产生式面板中的 P -> i b P Q 被高亮
  await followRoute.getByTestId('chain-pid-3-2').click()
  const rule1 = page.locator('[data-rule-id="1"]')
  await expect(rule1).toHaveClass(/rule-flash/)
  await expect(rule1).toContainText('P -> i b P Q')
})

test('双重来源：同一规则两份见证都保留，但格中只计一条规则', async ({ page }) => {
  await page.getByTestId('grammar-input').fill(DUAL_GRAMMAR)
  await page.getByTestId('start-input').fill('S')
  await page.getByTestId('tokens-input').fill('t')
  await page.getByTestId('analyze-btn').click()

  await expect(page.getByTestId('badge-conflict')).toBeVisible()
  await expect(page.getByTestId('conflict-panel')).toContainText('M[A, t]')

  // 格内两条不同规则；规则 3（A->B）有 first + follow 两份见证
  const g3 = page.getByTestId('witness-group-3')
  await expect(g3).toBeVisible()
  await expect(g3.getByTestId('dual-source-badge')).toBeVisible()
  await expect(g3.getByTestId('witness-route-3-first')).toContainText('右部 FIRST 命中')
  await expect(g3.getByTestId('witness-route-3-first')).toContainText('非终结符 B 的 FIRST 含 t')
  await expect(g3.getByTestId('witness-route-3-follow')).toContainText('可空后经 FOLLOW 命中')
  // FOLLOW 见证首环带可空见证 B->ε（产生式 6），且可点
  const proofChip = g3.getByTestId('proof-pid-3-0-0')
  await expect(proofChip).toContainText('6')
  await proofChip.click()
  await expect(page.locator('[data-rule-id="6"]')).toHaveClass(/rule-flash/)
  await expect(page.locator('[data-rule-id="6"]')).toContainText('B -> ε')

  // 规则 4（A->t）只有 FIRST 一份见证；总见证三份
  await expect(page.getByTestId('witness-group-4')).toBeVisible()
  await expect(page.getByTestId('witness-route-4-first')).toBeVisible()
  await expect(page.locator('[data-testid^="witness-route-"]')).toHaveCount(3)
  // 分组（=表中不同规则）只有两个
  await expect(page.locator('[data-testid^="witness-group-"]')).toHaveCount(2)
})

test('表格、见证与编辑中的文法同属一次分析请求：过期有提示，重新分析后整体刷新', async ({ page }) => {
  await page.getByTestId('grammar-input').fill(CONFLICT_GRAMMAR)
  await page.getByTestId('start-input').fill('P')
  await page.getByTestId('tokens-input').fill('i')
  await page.getByTestId('analyze-btn').click()
  await expect(page.getByTestId('badge-conflict')).toBeVisible()

  // 见证中的规则文本与当前编辑框文法一致（同一次请求）
  await expect(page.getByTestId('witness-rule-text-3')).toContainText('Q -> ε')

  // 未重新提交就编辑文法：旧推导标记过期，表格与见证仍属于上一请求
  await page.getByTestId('grammar-input').fill('S -> a\nS ->')
  await expect(page.getByTestId('stale-warn')).toBeVisible()
  // 旧见证仍指向旧文法的产生式 3（Q->ε），编辑框已是新文法——提示已明确两者不同步
  await expect(page.getByTestId('witness-list')).toBeVisible()
  await expect(page.getByTestId('witness-rule-text-3')).toContainText('Q -> ε')

  // 重新分析：新文法是 LL(1)，冲突、见证、表格一并切换为本次请求的结果
  await page.getByTestId('start-input').fill('S')
  await page.getByTestId('tokens-input').fill('a')
  await page.getByTestId('analyze-btn').click()
  await expect(page.getByTestId('badge-accepted')).toBeVisible()
  await expect(page.getByTestId('witness-list')).toHaveCount(0)
  await expect(page.getByTestId('trace-panel')).toBeVisible()
  await expect(page.getByTestId('stale-warn')).toHaveCount(0)
  // 新一次分析的产生式面板只有两条规则，旧的 Q->ε 编号 3 已不存在
  await expect(page.locator('[data-rule-id="3"]')).toHaveCount(0)
  await expect(page.locator('[data-rule-id="1"]')).toContainText('S -> a')
})
