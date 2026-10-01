package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// witnessPids 把一条见证链拍平为所引用的产生式编号序列（含可空见证）。
func witnessPids(w Witness) []int {
	var out []int
	for _, l := range w.Chain {
		if l.ProductionID != 0 {
			out = append(out, l.ProductionID)
		}
		out = append(out, l.NullableProof...)
	}
	return out
}

// validateWitnesses 通用校验：
//   - 见证与冲突格竞争规则身份一致：每条见证的规则都在 ProductionIDs 中，
//     同一规则两途皆中时保留两份，但竞争规则去重计数仍等于格内规则数；
//   - route 只取 first / follow，链非空且以终结符来源（first-term / follow-anchor 等）收尾；
//   - FIRST 展开与 FOLLOW 回填均不重复进入同一非终结符（见证不循环）；
//   - 链上产生式编号全部合法，可空见证编号也合法。
func validateWitnesses(t *testing.T, c *ConflictInfo) {
	t.Helper()
	if len(c.Witnesses) == 0 {
		t.Fatal("冲突格应至少有一条来源见证")
	}
	widSet := map[int]bool{}
	for _, id := range c.ProductionIDs {
		widSet[id] = true
	}
	seenRoutes := map[int]map[string]bool{}
	distinctRules := map[int]bool{}
	for _, w := range c.Witnesses {
		if !widSet[w.ProductionID] {
			t.Errorf("见证规则 %d 不在竞争规则集合 %v 中", w.ProductionID, c.ProductionIDs)
		}
		distinctRules[w.ProductionID] = true
		if w.Route != routeFirst && w.Route != routeFollow {
			t.Errorf("见证 route = %q，只允许 first / follow", w.Route)
		}
		if seenRoutes[w.ProductionID] == nil {
			seenRoutes[w.ProductionID] = map[string]bool{}
		}
		if seenRoutes[w.ProductionID][w.Route] {
			t.Errorf("规则 %d 的 %s 见证重复", w.ProductionID, w.Route)
		}
		seenRoutes[w.ProductionID][w.Route] = true
		if len(w.Chain) == 0 {
			t.Errorf("规则 %d 的 %s 见证链为空", w.ProductionID, w.Route)
			continue
		}
		last := w.Chain[len(w.Chain)-1]
		if w.Route == routeFirst && last.Kind != kindFirstTerm {
			t.Errorf("规则 %d 的 FIRST 见证应以 first-term 收尾，实际以 %s 收尾", w.ProductionID, last.Kind)
		}
		if w.Route == routeFollow &&
			last.Kind != kindFollowAnchor && last.Kind != kindFirstTerm && last.Kind != kindFollowFirst {
			t.Errorf("规则 %d 的 FOLLOW 见证应以 follow-anchor / follow-first / first-term 收尾，实际以 %s 收尾",
				w.ProductionID, last.Kind)
		}
		if w.Chain[0].ProductionID != w.ProductionID {
			t.Errorf("见证链首环必须是竞争规则 %d 自身，实际引用产生式 %d",
				w.ProductionID, w.Chain[0].ProductionID)
		}
		if w.Route == routeFirst && w.Chain[0].Kind != kindFirstTerm && w.Chain[0].Kind != kindFirstDerive {
			t.Errorf("FIRST 见证首环类别 = %s", w.Chain[0].Kind)
		}
		if w.Route == routeFollow && w.Chain[0].Kind != kindFollowEntry {
			t.Errorf("FOLLOW 见证首环类别 = %s，期望 follow-entry", w.Chain[0].Kind)
		}
		// 无环检查：FIRST 展开与 FOLLOW 回填阶段各自记录受益非终结符，禁止重复进入。
		seenFirst := map[string]bool{}
		seenFollow := map[string]bool{}
		for _, l := range w.Chain {
			if l.Kind == kindFollowAnchor {
				if l.ProductionID != 0 {
					t.Errorf("follow-anchor 环不应引用产生式，实际编号 %d", l.ProductionID)
				}
			} else if l.ProductionID < 1 || l.ProductionID > 30 {
				t.Errorf("链环引用非法产生式编号 %d", l.ProductionID)
			}
			for _, nid := range l.NullableProof {
				if nid < 1 || nid > 30 {
					t.Errorf("可空见证引用非法产生式编号 %d", nid)
				}
			}
			switch l.Kind {
			case kindFirstTerm:
				// 终结符是 FIRST 阶段的终点
			case kindFirstDerive:
				if seenFirst[l.Symbol] {
					t.Errorf("FIRST 见证在非终结符 %s 处成环：%v", l.Symbol, witnessPids(w))
				}
				seenFirst[l.Symbol] = true
			case kindFollowEntry:
				seenFollow[l.Symbol] = true
			case kindFollowFirst:
				// 进入 FIRST 子链前 FOLLOW 阶段结束；清空 FIRST 访问集
				seenFirst = map[string]bool{}
			case kindFollowPropagate:
				// 受益者 Symbol 已在环上；回填使 FOLLOW 链上溯到来源非终结符 FromSymbol
				if seenFollow[l.FromSymbol] {
					t.Errorf("FOLLOW 见证在非终结符 %s 处成环：%v", l.FromSymbol, witnessPids(w))
				}
				seenFollow[l.FromSymbol] = true
			case kindFollowAnchor:
				// 开始符 $ 锚点：传播链合法终点
			}
		}
	}
	if len(distinctRules) != len(c.ProductionIDs) {
		t.Errorf("见证涉及的不同规则数 %d 与格内规则数 %d 不一致（同一规则两途只计一条）",
			len(distinctRules), len(c.ProductionIDs))
	}
}

// findWitness 取出指定规则、指定途径的见证。
func findWitness(t *testing.T, c *ConflictInfo, pid int, route string) Witness {
	t.Helper()
	for _, w := range c.Witnesses {
		if w.ProductionID == pid && w.Route == route {
			return w
		}
	}
	t.Fatalf("未找到规则 %d 的 %s 见证；实际见证 = %+v", pid, route, c.Witnesses)
	return Witness{}
}

// W1) 间接可空：FOLLOW 传播链必须把逐层 ε 见证与可空前缀完整带出。
//
//	A -> B C a，B -> D | a，D -> ε，C -> ε：M[B,a] 中 B->a 经 FIRST 命中，
//	B->D 整条可空后，经 FOLLOW(B) 中的 a 命中，而 a 来自后缀 "C a" 中越过可空的 C。
func TestWitnessIndirectNullable(t *testing.T) {
	text := strings.Join([]string{
		"A -> B C a",
		"B -> D",
		"D ->",
		"C ->",
		"B -> a",
	}, "\n")
	res, err := analyze(text, "A", nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if res.Status != "conflict" {
		t.Fatalf("期望冲突，status=%q", res.Status)
	}
	c := res.Conflict
	if c.Nonterminal != "B" || c.Terminal != "a" {
		t.Fatalf("冲突格 = (%s,%s)，期望 (B,a)", c.Nonterminal, c.Terminal)
	}
	if !reflect.DeepEqual(c.ProductionIDs, []int{2, 5}) {
		t.Fatalf("竞争规则 = %v，期望 [2 5]", c.ProductionIDs)
	}
	validateWitnesses(t, c)

	first := findWitness(t, c, 5, routeFirst)
	if !reflect.DeepEqual(witnessPids(first), []int{5}) ||
		first.Chain[0].Kind != kindFirstTerm || first.Chain[0].Position != 1 {
		t.Errorf("B->a 的 FIRST 见证异常：%+v", first.Chain)
	}

	follow := findWitness(t, c, 2, routeFollow)
	pids := witnessPids(follow)
	// 首环 B->D 整条可空：可空见证 D->ε（3）；后缀 C a 的 FIRST 越过可空 C（4）后直达 a，
	// follow-first 环自身即终结符来源，不再接续 first-term。
	want := []int{2, 3, 1, 4}
	if !reflect.DeepEqual(pids, want) {
		t.Errorf("B->D 的 FOLLOW 见证引用 = %v，期望 %v（间接可空传播链不完整）", pids, want)
	}
	kinds := []string{kindFollowEntry, kindFollowFirst}
	for i, k := range kinds {
		if follow.Chain[i].Kind != k {
			t.Errorf("FOLLOW 见证第 %d 环类别 = %s，期望 %s", i+1, follow.Chain[i].Kind, k)
		}
	}
	if follow.Chain[0].NullableProof == nil ||
		!reflect.DeepEqual(follow.Chain[0].NullableProof, []int{3}) {
		t.Errorf("follow-entry 环应带可空见证 [3]（D->ε），实际 %v", follow.Chain[0].NullableProof)
	}
	if !reflect.DeepEqual(follow.Chain[1].NullableProof, []int{4}) {
		t.Errorf("follow-first 环应带可空前缀 [4]（C->ε），实际 %v", follow.Chain[1].NullableProof)
	}
}

// W2) FIRST / FOLLOW 双重来源：同一条规则同时经右部 FIRST 与“可空后 FOLLOW”命中，
//
//	保留两份来源见证，但表格 ProductionIDs 中只计一条规则。
//	S->A t 使 FOLLOW(A) 含 t；A->B 可空（B->ε）且 FIRST(B) 含 t（B->t），
//	故 A->B 在 M[A,t] 两途皆中；A->t 为另一竞争规则。
func TestWitnessFirstFollowDoubleSource(t *testing.T) {
	text := strings.Join([]string{
		"S -> A t",
		"S -> u",
		"A -> B",
		"A -> t",
		"B -> t",
		"B ->",
	}, "\n")
	res, err := analyze(text, "S", nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if res.Status != "conflict" {
		t.Fatalf("期望冲突，status=%q", res.Status)
	}
	c := res.Conflict
	if c.Nonterminal != "A" || c.Terminal != "t" {
		t.Fatalf("冲突格 = (%s,%s)，期望 (A,t)", c.Nonterminal, c.Terminal)
	}
	if !reflect.DeepEqual(c.ProductionIDs, []int{3, 4}) {
		t.Fatalf("格内规则去重后应为 [3 4]，实际 %v（A->B 两途命中只计一条）", c.ProductionIDs)
	}
	// 三份来源：规则 3 有 first、follow 两份，规则 4 有 first 一份。
	if len(c.Witnesses) != 3 {
		t.Fatalf("见证数 = %d，期望 3（同一规则双来源保留两份）", len(c.Witnesses))
	}
	if c.Witnesses[0].ProductionID != 3 || c.Witnesses[0].Route != routeFirst ||
		c.Witnesses[1].ProductionID != 3 || c.Witnesses[1].Route != routeFollow ||
		c.Witnesses[2].ProductionID != 4 || c.Witnesses[2].Route != routeFirst {
		t.Fatalf("见证顺序/身份异常：%+v", c.Witnesses)
	}
	validateWitnesses(t, c)

	wFirst := findWitness(t, c, 3, routeFirst)
	if !reflect.DeepEqual(witnessPids(wFirst), []int{3, 5}) {
		t.Errorf("A->B 的 FIRST 见证引用 = %v，期望 [3 5]", witnessPids(wFirst))
	}
	if wFirst.Chain[0].Kind != kindFirstDerive || wFirst.Chain[0].Symbol != "B" {
		t.Errorf("FIRST 见证首环异常：%+v", wFirst.Chain[0])
	}
	wFollow := findWitness(t, c, 3, routeFollow)
	wantFollow := []int{3, 6, 1}
	if !reflect.DeepEqual(witnessPids(wFollow), wantFollow) {
		t.Errorf("A->B 的 FOLLOW 见证引用 = %v，期望 %v（含 B->ε 可空见证 6）",
			witnessPids(wFollow), wantFollow)
	}
	if wFollow.Chain[0].Kind != kindFollowEntry ||
		!reflect.DeepEqual(wFollow.Chain[0].NullableProof, []int{6}) {
		t.Errorf("FOLLOW 见证首环应是带可空见证 [6] 的 follow-entry：%+v", wFollow.Chain[0])
	}
	if wFollow.Chain[1].Kind != kindFollowFirst {
		t.Errorf("FOLLOW 第二环应为 follow-first：%+v", wFollow.Chain[1])
	}

	// 冲突响应不带任何回放；普通模式语义不变。
	if res.Trace != nil || res.Accepted || res.Error != nil {
		t.Fatalf("冲突时不应产生回放：trace=%v accepted=%v error=%+v",
			res.Trace, res.Accepted, res.Error)
	}
}

// W3) 循环传播：nullable 与 FOLLOW 都存在循环时，见证必须有尽头、不得成环。
//
//	S->A x；A->B；B->A；B->ε。FOLLOW(A) 与 FOLLOW(B) 经互指传播，
//	M[B,x] 中 B->A 与 B->ε 都只能经 FOLLOW 命中；可空见证为 B->A, A->B, B->ε 的无环链。
func TestWitnessCyclicPropagation(t *testing.T) {
	text := strings.Join([]string{
		"S -> A x",
		"A -> B",
		"B -> A",
		"B ->",
	}, "\n")
	res, err := analyze(text, "S", nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if res.Status != "conflict" {
		t.Fatalf("期望冲突，status=%q", res.Status)
	}
	c := res.Conflict
	if c.Nonterminal != "B" || c.Terminal != "x" {
		t.Fatalf("冲突格 = (%s,%s)，期望 (B,x)", c.Nonterminal, c.Terminal)
	}
	validateWitnesses(t, c) // 内含无环检查

	// B->A 整条可空的可空见证必须绕开 A⇄B 环，取有尽头的 3→2→4。
	wBA := findWitness(t, c, 3, routeFollow)
	if !reflect.DeepEqual(wBA.Chain[0].NullableProof, []int{2, 4}) {
		t.Errorf("B->A 的可空见证 = %v，期望 [2 4]（绕开 A⇄B 环）",
			wBA.Chain[0].NullableProof)
	}
	if !reflect.DeepEqual(witnessPids(wBA), []int{3, 2, 4, 2, 1}) {
		t.Errorf("B->A 的 FOLLOW 见证引用 = %v，期望 [3 2 4 2 1]", witnessPids(wBA))
	}
	// FOLLOW 回填在 A 处终止（不能继续 FOLLOW(A)->FOLLOW(B) 成环），改由 follow-first 取得 x。
	kinds := []string{kindFollowEntry, kindFollowPropagate, kindFollowFirst}
	if len(wBA.Chain) != len(kinds) {
		t.Fatalf("B->A 见证链长 = %d，期望 %d：%+v", len(wBA.Chain), len(kinds), wBA.Chain)
	}
	for i, k := range kinds {
		if wBA.Chain[i].Kind != k {
			t.Errorf("第 %d 环类别 = %s，期望 %s（FOLLOW 循环必须在有尽头处停止）",
				i+1, wBA.Chain[i].Kind, k)
		}
	}
	if wBA.Chain[1].Symbol != "B" || wBA.Chain[1].FromSymbol != "A" {
		t.Errorf("回填环受益者/来源异常：symbol=%s from=%s，期望 B/A",
			wBA.Chain[1].Symbol, wBA.Chain[1].FromSymbol)
	}
	if wBA.Chain[2].Symbol != "A" {
		t.Errorf("follow-first 环应在 A 处取得 x，实际 symbol=%s", wBA.Chain[2].Symbol)
	}

	// 左递归 FIRST 传播同样不得成环：
	// E->E a T | T，M[E,b] 中 E->E a T 的 FIRST 见证必须走 E->T->b 这条有尽头的分支。
	lrText := strings.Join([]string{
		"E -> E a T",
		"E -> T",
		"T -> b",
	}, "\n")
	lr, err := analyze(lrText, "E", nil)
	if err != nil {
		t.Fatalf("analyze 左递归: %v", err)
	}
	if lr.Status != "conflict" {
		t.Fatalf("左递归文法应冲突，status=%q", lr.Status)
	}
	validateWitnesses(t, lr.Conflict)
	wLR := findWitness(t, lr.Conflict, 1, routeFirst)
	if !reflect.DeepEqual(witnessPids(wLR), []int{1, 2, 3}) {
		t.Errorf("E->E a T 的 FIRST 见证引用 = %v，期望 [1 2 3]（跳过自环分支）",
			witnessPids(wLR))
	}

	// "$" 列冲突：全部符号经空后缀逐层回填到开始符锚点，链以 follow-anchor 收尾且不成环。
	dollarText := strings.Join([]string{
		"S -> A",
		"A -> B",
		"B -> A",
		"B ->",
	}, "\n")
	dl, err := analyze(dollarText, "S", nil)
	if err != nil {
		t.Fatalf("analyze 全可空循环: %v", err)
	}
	if dl.Status != "conflict" {
		t.Fatalf("期望 $ 列冲突，status=%q", dl.Status)
	}
	if dl.Conflict.Terminal != "$" {
		t.Fatalf("冲突列 = %s，期望 $", dl.Conflict.Terminal)
	}
	validateWitnesses(t, dl.Conflict)
	wDollar := findWitness(t, dl.Conflict, 3, routeFollow)
	last := wDollar.Chain[len(wDollar.Chain)-1]
	if last.Kind != kindFollowAnchor || last.Symbol != "S" || last.Terminal != "$" {
		t.Errorf("$ 列见证应以开始符 S 的 follow-anchor 收尾，实际 %+v", last)
	}
	// 回填路径 B -> A -> S：不得重新回到 B（B⇄A 环被剪枝）
	propagates := []string{}
	for _, l := range wDollar.Chain {
		if l.Kind == kindFollowPropagate {
			propagates = append(propagates, l.FromSymbol)
		}
	}
	if !reflect.DeepEqual(propagates, []string{"A", "S"}) {
		t.Errorf("回填来源序列 = %v，期望 [A S]（跳过 B⇄A 循环边）", propagates)
	}
}

// W4) 并列路径：多条见证并存时先取引用产生式最少的路径；等长再按产生式原始顺序裁决。
func TestWitnessParallelPaths(t *testing.T) {
	// W4a) 同一终结符经不同父产生式加入同一 FOLLOW：短路径优先。
	// C 同时出现在 S->C a 与 T C->C a 中：经 S 直达只需 1 个父产生式，经 T 要 2 个。
	text := strings.Join([]string{
		"S -> C a",
		"S -> b",
		"T -> C a",
		"C ->",
		"C -> a",
	}, "\n")
	res, err := analyze(text, "S", nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if res.Status != "conflict" {
		t.Fatalf("期望冲突，status=%q", res.Status)
	}
	c := res.Conflict
	if c.Nonterminal != "C" || c.Terminal != "a" {
		t.Fatalf("冲突格 = (%s,%s)，期望 (C,a)", c.Nonterminal, c.Terminal)
	}
	validateWitnesses(t, c)
	wEps := findWitness(t, c, 4, routeFollow)
	if !reflect.DeepEqual(witnessPids(wEps), []int{4, 1}) {
		t.Errorf("C->ε 的 FOLLOW 见证 = %v，期望 [4 1]（引用产生式最少的路径，经 S 直达）",
			witnessPids(wEps))
	}

	// W4b) 两条并列 FIRST 路径等长：按产生式原始顺序（编号序）裁决，不取字典序侥幸。
	// Q->U t | V t，U->t、V->t；Q->t 与两者在 M[Q,t] 冲突，
	// 竞争规则各自唯一；并列裁决体现在 nullable/FOLLOW 同代价时编号小者胜。
	text2 := strings.Join([]string{
		"S -> A x",
		"S -> b",
		"A -> D",
		"A -> C",
		"C ->",
		"D ->",
		"A -> x",
	}, "\n")
	res2, err := analyze(text2, "S", nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if res2.Status != "conflict" {
		t.Fatalf("期望冲突，status=%q", res2.Status)
	}
	c2 := res2.Conflict
	if c2.Nonterminal != "A" || c2.Terminal != "x" {
		t.Fatalf("冲突格 = (%s,%s)，期望 (A,x)", c2.Nonterminal, c2.Terminal)
	}
	validateWitnesses(t, c2)
	// A->D 与 A->C 都可空：可空见证等长（各 2 条产生式），并列时取原始顺序靠前者，
	// 即 A->D（3）的可空见证引用 D->ε（6），A->C（4）引用 C->ε（5）；
	// 两条规则都在格内，各自的可空见证必须是编号字典序最小且无环的那条。
	wD := findWitness(t, c2, 3, routeFollow)
	wC := findWitness(t, c2, 4, routeFollow)
	if !reflect.DeepEqual(wD.Chain[0].NullableProof, []int{6}) {
		t.Errorf("A->D 可空见证 = %v，期望 [6]", wD.Chain[0].NullableProof)
	}
	if !reflect.DeepEqual(wC.Chain[0].NullableProof, []int{5}) {
		t.Errorf("A->C 可空见证 = %v，期望 [5]", wC.Chain[0].NullableProof)
	}
}

// W5) 非法文法整次拒绝：解析失败时绝不返回任何冲突或半份来源见证。
func TestWitnessIllegalGrammarRejected(t *testing.T) {
	text := strings.Join([]string{
		"A -> B a",
		"B -> C",
	}, "\n") // C 在右部出现但没有产生式
	res, err := analyze(text, "A", nil)
	if err == nil {
		t.Fatalf("引用未定义非终结符的文法必须整次拒绝，得到结果：%+v", res)
	}
	if res != nil {
		t.Fatalf("非法文法不得返回半份来源：res=%+v err=%v", res, err)
	}
	if !strings.Contains(err.Error(), "C") {
		t.Errorf("错误信息应指出未定义非终结符 C：%v", err)
	}

	if _, err := parseGrammar("A ->", "Z"); err == nil {
		t.Fatal("开始符没有产生式时必须拒绝")
	}
}

// W6) HTTP 冲突响应中的见证与 Go 分析器共用产生式身份：
//
//	productionId / 链上编号全部落在同一次分析的 Productions 内，恢复模式同样不生成回放。
func TestWitnessHTTPSameIdentities(t *testing.T) {
	text := strings.Join([]string{
		"S -> A t",
		"S -> u",
		"A -> B",
		"A -> t",
		"B -> t",
		"B ->",
	}, "\n")
	body := `{"grammar":"` + strings.ReplaceAll(text, "\n", `\n`) + `","start":"S","tokens":[],"recover":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/analyze", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handleAnalyze(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
	var httpRes AnalyzeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &httpRes); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if httpRes.Status != "conflict" || httpRes.Conflict == nil {
		t.Fatalf("期望 conflict，得到 %+v", httpRes.Status)
	}
	if httpRes.Trace != nil || httpRes.Accepted || httpRes.HadError || httpRes.RecoveryCount != 0 {
		t.Fatalf("冲突（含恢复模式）不得回放：trace=%v hadError=%v count=%d",
			httpRes.Trace, httpRes.HadError, httpRes.RecoveryCount)
	}
	ids := map[int]bool{}
	for _, p := range httpRes.Productions {
		ids[p.ID] = true
	}
	c := httpRes.Conflict
	for _, id := range c.ProductionIDs {
		if !ids[id] {
			t.Errorf("竞争规则编号 %d 不在同次响应的产生式列表中", id)
		}
	}
	for _, w := range c.Witnesses {
		if !ids[w.ProductionID] {
			t.Errorf("见证规则 %d 身份与产生式列表不一致", w.ProductionID)
		}
		for _, l := range w.Chain {
			if l.ProductionID != 0 && !ids[l.ProductionID] {
				t.Errorf("链环产生式 %d 身份与产生式列表不一致", l.ProductionID)
			}
			for _, nid := range l.NullableProof {
				if !ids[nid] {
					t.Errorf("可空见证产生式 %d 身份与产生式列表不一致", nid)
				}
			}
		}
	}
	// 与直接调用 analyzeMode 得到的见证逐字节一致（同一推导、同一身份）。
	direct, err := analyzeMode(text, "S", nil, true)
	if err != nil {
		t.Fatalf("analyzeMode: %v", err)
	}
	if !reflect.DeepEqual(httpRes.Conflict.Witnesses, direct.Conflict.Witnesses) {
		t.Fatalf("HTTP 见证与 Go 分析器不一致：\n%+v\n%+v",
			httpRes.Conflict.Witnesses, direct.Conflict.Witnesses)
	}
}
