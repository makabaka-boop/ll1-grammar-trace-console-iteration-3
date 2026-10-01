package main

// 冲突来源见证（witness）：
// 对首个冲突格中的每条竞争产生式，解释它为何落入该格——
// 经由右部 FIRST 命中（route=first），还是右部可空后经 FOLLOW 传播命中（route=follow），
// 并给出可追溯到原产生式的传播链。同一规则可同时有两份见证，但表格中仍只计一条规则。
//
// 传播链全部以产生式 ID 引用，Go 集合推导 / 冲突响应 / Vue 表格与编辑面板共用同一身份。
// 见证不允许循环：FIRST 链展开非终结符、FOLLOW 链回填时均带已访问集合（visited）。
// 多条见证并列时，先取“引用产生式最少”的路径（含可空见证所引用的产生式，开始符锚点不计），
// 再依次按 产生式原始编号序 / 可空见证编号序 / 右部位置 / 环类别 裁决。

import (
	"fmt"
	"strings"
)

// 见证环类别。
const (
	kindFirstTerm       = "first-term"       // 右部某位即终结符，直接进入 FIRST
	kindFirstDerive     = "first-derive"     // 右部某位非终结符，经其 FIRST 供给
	kindFollowEntry     = "follow-entry"     // 竞争产生式自身右部可空，规则填入 FOLLOW 列
	kindFollowFirst     = "follow-first"     // C->αBβ：FIRST(β) 中的终结符加入 FOLLOW(B)
	kindFollowPropagate = "follow-propagate" // C->αBβ：β 可空，FOLLOW(C) 回填 FOLLOW(B)
	kindFollowAnchor    = "follow-anchor"    // $ 属于开始符的 FOLLOW（开始符规则，链的终点）
)

const (
	routeFirst  = "first"
	routeFollow = "follow"
)

// WitnessLink 是冲突来源传播链上的一环，全部以产生式 ID 引用。
type WitnessLink struct {
	Kind          string `json:"kind"`                    // 环类别，见 kind* 常量
	ProductionID  int    `json:"productionId"`            // 本环引用的产生式（follow-anchor 终点时为 0）
	Symbol        string `json:"symbol"`                  // 相关非终结符：被展开者 / FOLLOW 的受益者 / 开始符
	FromSymbol    string `json:"fromSymbol,omitempty"`    // follow-propagate：回填来源非终结符（FOLLOW 链经它继续上溯）
	Position      int    `json:"position,omitempty"`      // 该非终结符（或终结符）在产生式右部中的位置，1 起
	Terminal      string `json:"terminal,omitempty"`      // 本环命中的终结符（follow-anchor 时为 "$"）
	NullableProof []int  `json:"nullableProof,omitempty"` // 本环依赖的可空性：依次被证明可空的产生式
	Note          string `json:"note"`                    // 中文说明，页面直接展示
}

// Witness 解释一条产生式为何落入冲突格。
type Witness struct {
	ProductionID int           `json:"productionId"` // 竞争产生式
	Route        string        `json:"route"`        // "first"：右部 FIRST 命中；"follow"：右部可空后经 FOLLOW 命中
	Chain        []WitnessLink `json:"chain"`        // 从竞争产生式到终结符来源的完整传播链（无环）
}

type chainKey struct {
	sym string
	vis uint32 // 已在传播路径上的非终结符位集，保证见证无环
}

type witnessBuilder struct {
	g          *grammar
	ntIndex    map[string]int
	eps        map[string][]int // 每个可空非终结符“引用产生式最少”的可空见证
	firstMemo  map[chainKey][]WitnessLink
	followMemo map[chainKey][]WitnessLink
}

func newWitnessBuilder(g *grammar) *witnessBuilder {
	idx := map[string]int{}
	for i, nt := range sortedKeys(g.nonterminals, nil) {
		idx[nt] = i
	}
	return &witnessBuilder{
		g:          g,
		ntIndex:    idx,
		eps:        g.epsilonProofs(),
		firstMemo:  map[chainKey][]WitnessLink{},
		followMemo: map[chainKey][]WitnessLink{},
	}
}

func (wb *witnessBuilder) bit(nt string) uint32 { return 1 << uint(wb.ntIndex[nt]) }

func copyIDs(ids []int) []int {
	if len(ids) == 0 {
		return nil
	}
	return append([]int(nil), ids...)
}

func joinIDs(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return strings.Join(parts, " → ")
}

// intSeqLess 裁决等长见证：先比长度，再按编号字典序（即产生式原始顺序）。
func intSeqLess(a, b []int) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// epsilonProofs 以不动点方式为每个可空非终结符求“引用产生式最少”的可空见证：
// 见证为一串产生式编号，按推导顺序排列；并列时取编号字典序最小者。
// 只可能经无环推导收敛（环不会让原本不可空的符号变可空），故见证天然无环。
func (g *grammar) epsilonProofs() map[string][]int {
	best := map[string][]int{}
	for changed := true; changed; {
		changed = false
		for _, p := range g.prods {
			proof, ok := seqEpsilonProof(p.RHS, best, g.terminals)
			if !ok {
				continue
			}
			cand := make([]int, 0, 1+len(proof))
			cand = append(cand, p.ID)
			cand = append(cand, proof...)
			if cur, has := best[p.Head]; !has || intSeqLess(cand, cur) {
				best[p.Head] = cand
				changed = true
			}
		}
	}
	return best
}

// seqEpsilonProof 求符号串可空时按顺序拼接的各非终结符可空见证；
// 空串天然可空（返回空切片与 true），遇终结符或尚不可空的非终结符则不可空。
func seqEpsilonProof(s string, eps map[string][]int, terminals map[string]bool) ([]int, bool) {
	var out []int
	for i := 0; i < len(s); i++ {
		x := string(s[i])
		if terminals[x] {
			return nil, false
		}
		pr, ok := eps[x]
		if !ok {
			return nil, false
		}
		out = append(out, pr...)
	}
	return out, true
}

// seqProof 是 epsilonProofs 求完后的查询封装。
func (wb *witnessBuilder) seqProof(s string) ([]int, bool) {
	return seqEpsilonProof(s, wb.eps, wb.g.terminals)
}

func kindRank(kind string) int {
	switch kind {
	case kindFirstTerm:
		return 0
	case kindFirstDerive:
		return 1
	case kindFollowEntry:
		return 2
	case kindFollowFirst:
		return 3
	case kindFollowPropagate:
		return 4
	case kindFollowAnchor:
		return 5
	}
	return 6
}

// chainCost 统计一条见证链引用的产生式数（含可空见证；开始符锚点不引用产生式）。
func chainCost(c []WitnessLink) int {
	n := 0
	for _, l := range c {
		if l.Kind != kindFollowAnchor {
			n++
		}
		n += len(l.NullableProof)
	}
	return n
}

// chainTieKey 给出等代价路径的裁决键：产生式原始编号序、可空见证编号序、右部位置、环类别。
func chainTieKey(c []WitnessLink) []int {
	k := make([]int, 0, len(c)*3)
	for _, l := range c {
		k = append(k, l.ProductionID)
		k = append(k, l.NullableProof...)
		k = append(k, l.Position, kindRank(l.Kind))
	}
	return k
}

// betterChain 在两条合法见证中择优：引用产生式更少者优先，再按裁决键取小。
func betterChain(a, b []WitnessLink) []WitnessLink {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	ca, cb := chainCost(a), chainCost(b)
	if cb < ca {
		return b
	}
	if cb > ca {
		return a
	}
	ka, kb := chainTieKey(a), chainTieKey(b)
	for i := 0; i < len(ka) && i < len(kb); i++ {
		if kb[i] != ka[i] {
			if kb[i] < ka[i] {
				return b
			}
			return a
		}
	}
	if len(kb) < len(ka) {
		return b
	}
	return a
}

// ---- 说明文案 ----

func nullablePrefixNote(prefix []int) string {
	if len(prefix) == 0 {
		return ""
	}
	return fmt.Sprintf("；其前的可空前缀依次由产生式 %s 见证", joinIDs(prefix))
}

func firstTermLink(p Production, pos int, t string) WitnessLink {
	return WitnessLink{
		Kind:         kindFirstTerm,
		ProductionID: p.ID,
		Symbol:       p.Head,
		Position:     pos,
		Terminal:     t,
		Note: fmt.Sprintf("产生式 %s -> %s：右部第 %d 位终结符 %s 直接进入 FIRST(%s)",
			p.Head, rhsDisplay(p.RHS), pos, t, p.Head),
	}
}

func firstDeriveLink(p Production, pos int, x, t string, prefix []int) WitnessLink {
	return WitnessLink{
		Kind:          kindFirstDerive,
		ProductionID:  p.ID,
		Symbol:        x,
		Position:      pos,
		Terminal:      t,
		NullableProof: copyIDs(prefix),
		Note: fmt.Sprintf("产生式 %s -> %s：右部第 %d 位非终结符 %s 的 FIRST 含 %s%s",
			p.Head, rhsDisplay(p.RHS), pos, x, t, nullablePrefixNote(prefix)),
	}
}

func followFirstLink(p Production, pos int, b, t string, tailPrefix []int) WitnessLink {
	return WitnessLink{
		Kind:          kindFollowFirst,
		ProductionID:  p.ID,
		Symbol:        b,
		Position:      pos,
		Terminal:      t,
		NullableProof: copyIDs(tailPrefix),
		Note: fmt.Sprintf("产生式 %s -> %s：第 %d 位 %s 之后后缀的 FIRST 含 %s，%s 加入 FOLLOW(%s)%s",
			p.Head, rhsDisplay(p.RHS), pos, b, t, t, b, nullablePrefixNote(tailPrefix)),
	}
}

func followPropagateLink(p Production, pos int, b string, tailProof []int) WitnessLink {
	tail := p.RHS[pos:] // pos 为 1 起，后缀即从该位开始
	note := ""
	if len(tail) == 0 {
		note = fmt.Sprintf("产生式 %s -> %s：第 %d 位 %s 位于右部末尾（空后缀即 ε），FOLLOW(%s) 回填 FOLLOW(%s)",
			p.Head, rhsDisplay(p.RHS), pos, b, p.Head, b)
	} else {
		note = fmt.Sprintf("产生式 %s -> %s：第 %d 位 %s 之后的后缀 %s 可空，FOLLOW(%s) 回填 FOLLOW(%s)（可空性依次由产生式 %s 见证）",
			p.Head, rhsDisplay(p.RHS), pos, b, rhsDisplay(tail), p.Head, b, joinIDs(tailProof))
	}
	return WitnessLink{
		Kind:          kindFollowPropagate,
		ProductionID:  p.ID,
		Symbol:        b,
		FromSymbol:    p.Head,
		Position:      pos,
		NullableProof: copyIDs(tailProof),
		Note:          note,
	}
}

func followAnchorLink(start, t string) WitnessLink {
	return WitnessLink{
		Kind:     kindFollowAnchor,
		Symbol:   start,
		Terminal: t,
		Note:     fmt.Sprintf("%s 是开始符，$ 属于 FOLLOW(%s)（开始符规则，传播链终点）", start, start),
	}
}

// ---- FIRST 见证 ----

// firstChain 解释 t 为何属于 FIRST(nt)：展开 nt 的候选式，环环追溯到终结符。
// vis 记录已在当前路径上展开的非终结符，递归文法中绕开循环、只留有尽头的见证。
func (wb *witnessBuilder) firstChain(nt, t string, vis uint32) []WitnessLink {
	if vis&wb.bit(nt) != 0 {
		return nil
	}
	key := chainKey{sym: nt, vis: vis}
	if v, ok := wb.firstMemo[key]; ok {
		return v
	}
	nextVis := vis | wb.bit(nt)

	var best []WitnessLink
	var prefix []int
	for _, pid := range wb.g.byHead[nt] {
		p := wb.g.prods[pid-1]
		prefix = prefix[:0]
		for i := 0; i < len(p.RHS); i++ {
			x := string(p.RHS[i])
			if wb.g.terminals[x] {
				if x == t {
					best = betterChain(best, []WitnessLink{firstTermLink(p, i+1, t)})
				}
				break // 终结符之后不再贡献 FIRST
			}
			if wb.g.first[x][t] {
				if sub := wb.firstChain(x, t, nextVis); sub != nil {
					cand := append([]WitnessLink{firstDeriveLink(p, i+1, x, t, prefix)}, sub...)
					best = betterChain(best, cand)
				}
			}
			if !wb.g.nullable[x] {
				break
			}
			prefix = append(prefix, wb.eps[x]...)
		}
	}
	wb.firstMemo[key] = best
	return best
}

// firstWitness 构造竞争产生式 p 经右部 FIRST 命中列 t 的见证。
func (wb *witnessBuilder) firstWitness(p Production, t string) Witness {
	var best []WitnessLink
	var prefix []int
	for i := 0; i < len(p.RHS); i++ {
		x := string(p.RHS[i])
		if wb.g.terminals[x] {
			if x == t {
				best = betterChain(best, []WitnessLink{firstTermLink(p, i+1, t)})
			}
			break
		}
		if wb.g.first[x][t] {
			if sub := wb.firstChain(x, t, 0); sub != nil { // 竞争规则自身不在 visited 中，从它开始记录
				cand := append([]WitnessLink{firstDeriveLink(p, i+1, x, t, prefix)}, sub...)
				best = betterChain(best, cand)
			}
		}
		if !wb.g.nullable[x] {
			break
		}
		prefix = append(prefix, wb.eps[x]...)
	}
	return Witness{ProductionID: p.ID, Route: routeFirst, Chain: best}
}

// ---- FOLLOW 见证 ----

// followChain 解释 t 为何属于 FOLLOW(nt)：
// 经 C->αBβ 中 FIRST(β) 直接取得，或 β 可空时由 FOLLOW(C) 回填，直到开始符的 $ 锚点。
func (wb *witnessBuilder) followChain(nt, t string, vis uint32) []WitnessLink {
	if vis&wb.bit(nt) != 0 {
		return nil
	}
	key := chainKey{sym: nt, vis: vis}
	if v, ok := wb.followMemo[key]; ok {
		return v
	}
	nextVis := vis | wb.bit(nt)

	var best []WitnessLink
	if nt == wb.g.start && t == "$" {
		best = []WitnessLink{followAnchorLink(nt, t)}
	}

	for _, p := range wb.g.prods {
		for i := 0; i < len(p.RHS); i++ {
			b := string(p.RHS[i])
			if !wb.g.nonterminals[b] || b != nt {
				continue
			}
			tail := p.RHS[i+1:]

			// 途径一：后缀 β 的 FIRST 直接提供 t（β 内同样允许越过可空前缀）。
			var tailPrefix []int
			for j := 0; j < len(tail); j++ {
				x := string(tail[j])
				if wb.g.terminals[x] {
					if x == t {
						best = betterChain(best, []WitnessLink{followFirstLink(p, i+1, b, t, tailPrefix)})
					}
					break
				}
				if wb.g.first[x][t] {
					if sub := wb.firstChain(x, t, 0); sub != nil { // FIRST 子链另开访问集
						cand := append([]WitnessLink{followFirstLink(p, i+1, b, t, tailPrefix)}, sub...)
						best = betterChain(best, cand)
					}
				}
				if !wb.g.nullable[x] {
					break
				}
				tailPrefix = append(tailPrefix, wb.eps[x]...)
			}

			// 途径二：后缀 β 整体可空，FOLLOW(C) 回填 FOLLOW(B)。
			if tailProof, ok := wb.seqProof(tail); ok {
				if sub := wb.followChain(p.Head, t, nextVis); sub != nil {
					cand := append([]WitnessLink{followPropagateLink(p, i+1, b, tailProof)}, sub...)
					best = betterChain(best, cand)
				}
			}
		}
	}
	wb.followMemo[key] = best
	return best
}

// followEntryLink 构造“竞争产生式右部可空，按 FOLLOW 列填入”的首环。
func followEntryLink(p Production, head string, proof []int) WitnessLink {
	if p.Epsilon {
		return WitnessLink{
			Kind:          kindFollowEntry,
			ProductionID:  p.ID,
			Symbol:        head,
			NullableProof: nil,
			Note:          fmt.Sprintf("产生式 %s -> ε：空右部天然可空，规则按 FOLLOW(%s) 各列填入", head, head),
		}
	}
	return WitnessLink{
		Kind:          kindFollowEntry,
		ProductionID:  p.ID,
		Symbol:        head,
		NullableProof: copyIDs(proof),
		Note: fmt.Sprintf("产生式 %s -> %s：整条右部可空，规则按 FOLLOW(%s) 各列填入（可空性依次由产生式 %s 见证）",
			head, rhsDisplay(p.RHS), head, joinIDs(proof)),
	}
}

// followWitness 构造竞争产生式 p（左部 head）右部可空后经 FOLLOW 命中列 t 的见证。
func (wb *witnessBuilder) followWitness(p Production, head, t string) Witness {
	proof, _ := wb.seqProof(p.RHS)
	sub := wb.followChain(head, t, 0) // 竞争规则自己作为首环，FOLLOW 子链从 head 开始
	chain := append([]WitnessLink{followEntryLink(p, head, proof)}, sub...)
	return Witness{ProductionID: p.ID, Route: routeFollow, Chain: chain}
}

// witnesses 为冲突格 (nt, t) 中的全部竞争产生式（编号序）构造来源见证：
// 右部 FIRST 含 t 给 first 见证；右部可空给 follow 见证；同一规则两途皆中时保留两份。
func (wb *witnessBuilder) witnesses(nt, t string, ids []int) []Witness {
	out := make([]Witness, 0, len(ids)*2)
	for _, pid := range ids {
		p := wb.g.prods[pid-1]
		terms, nullable := wb.g.firstSeq(p.RHS)
		if terms[t] {
			out = append(out, wb.firstWitness(p, t))
		}
		if nullable {
			out = append(out, wb.followWitness(p, nt, t))
		}
	}
	return out
}
