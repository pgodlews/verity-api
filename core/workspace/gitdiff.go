package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secretscan"
)

const redactedValue = "***REDACTED***"

// diffProjects compares a HEAD project with the working tree. Steps match on
// suite, case, and step name; a repeated name matches in order (the index is
// the tiebreak). A name that disappears in one place and appears in another
// suite or case is moved. Scalar parts carry before/after text. A value
// secretscan.Check would flag is replaced with redactedValue, including
// values committed before the save-time guard existed.
func diffProjects(head, work *model.Project) *ProjectChanges {
	if head == nil {
		head = &model.Project{}
	}
	if work == nil {
		work = &model.Project{}
	}
	ch := blankChanges(true, true)
	ch.Steps = diffSteps(head, work)
	ch.Environments = diffEnvs(head.Environments, work.Environments)
	ch.Auth = diffAuth(head.Auth, work.Auth)
	ch.Suites = append(diffScopes(suiteItems(head), suiteItems(work), false), diffSuiteFields(head, work)...)
	ch.Cases = append(diffScopes(caseItems(head), caseItems(work), true), diffCaseFields(head, work)...)
	ch.Meta = diffMeta(head, work)
	ensureReported(ch, head, work)
	return ch
}

type stepKey struct {
	suite    string
	suiteN   int
	caseName string
	caseN    int
	name     string
	stepN    int
}

type indexedStep struct {
	key             stepKey
	suite, caseName string
	name            string
	index           int
	step            model.Step
}

func indexSteps(p *model.Project) []indexedStep {
	if p == nil {
		return nil
	}
	var out []indexedStep
	suiteN := map[string]int{}
	for _, s := range p.Suites {
		sn := suiteN[s.Name]
		suiteN[s.Name] = sn + 1
		// caseN -1 keeps hook steps from colliding with a case that is also
		// named "(setup)" or "(teardown)".
		if s.Setup != nil {
			out = appendHookSteps(out, s.Name, sn, "(setup)", s.Setup.Steps)
		}
		caseN := map[string]int{}
		for _, c := range s.Cases {
			cn := caseN[c.Name]
			caseN[c.Name] = cn + 1
			out = append(out, indexCaseSteps(s.Name, sn, c.Name, cn, c.Steps)...)
		}
		if s.Teardown != nil {
			out = appendHookSteps(out, s.Name, sn, "(teardown)", s.Teardown.Steps)
		}
	}
	return out
}

func appendHookSteps(out []indexedStep, suite string, suiteN int, caseName string, steps []model.Step) []indexedStep {
	return append(out, indexCaseSteps(suite, suiteN, caseName, -1, steps)...)
}

func indexCaseSteps(suite string, suiteN int, caseName string, caseN int, steps []model.Step) []indexedStep {
	stepN := map[string]int{}
	out := make([]indexedStep, 0, len(steps))
	for i, st := range steps {
		dn := stepN[st.Name]
		stepN[st.Name] = dn + 1
		out = append(out, indexedStep{
			key:      stepKey{suite, suiteN, caseName, caseN, st.Name, dn},
			suite:    suite,
			caseName: caseName,
			name:     st.Name,
			index:    i,
			step:     st,
		})
	}
	return out
}

func diffSteps(head, work *model.Project) []StepChange {
	hSteps := indexSteps(head)
	wSteps := indexSteps(work)
	hMap := make(map[stepKey]indexedStep, len(hSteps))
	for _, s := range hSteps {
		hMap[s.key] = s
	}
	used := map[stepKey]bool{}
	var out []StepChange
	for _, w := range wSteps {
		h, ok := hMap[w.key]
		if !ok {
			continue
		}
		used[w.key] = true
		parts := stepParts(h.step, w.step)
		if h.index != w.index {
			parts = append([]PartChange{{
				Part:   "order",
				Before: strconv.Itoa(h.index),
				After:  strconv.Itoa(w.index),
			}}, parts...)
		}
		if len(parts) == 0 {
			continue
		}
		out = append(out, StepChange{
			Suite: w.suite, Case: w.caseName, Name: w.name, Index: w.index,
			State: changeModified, Parts: parts,
		})
	}
	var unmatchedH, unmatchedW []indexedStep
	for _, h := range hSteps {
		if !used[h.key] {
			unmatchedH = append(unmatchedH, h)
		}
	}
	for _, w := range wSteps {
		if !used[w.key] {
			unmatchedW = append(unmatchedW, w)
		}
	}
	moves, leftH, leftW := pairMoves(unmatchedH, unmatchedW)
	out = append(out, moves...)
	for _, w := range leftW {
		out = append(out, StepChange{Suite: w.suite, Case: w.caseName, Name: w.name, Index: w.index, State: changeAdded})
	}
	for _, h := range leftH {
		out = append(out, StepChange{Suite: h.suite, Case: h.caseName, Name: h.name, Index: h.index, State: changeRemoved})
	}
	if out == nil {
		return []StepChange{}
	}
	return out
}

// pairMoves pairs unmatched steps that share a name when the request identity
// matches, including the body. A step is paired only when that match is
// unique on both sides, so two crossed URLs meet their own partners and two
// GraphQL calls that share a URL but not a body stay an add and a removal.
// The same suite and case is not a move.
func pairMoves(head, work []indexedStep) (moves []StepChange, leftH, leftW []indexedStep) {
	order := make([]string, 0)
	seen := map[string]bool{}
	byH := map[string][]indexedStep{}
	byW := map[string][]indexedStep{}
	for _, h := range head {
		if !seen[h.name] {
			seen[h.name] = true
			order = append(order, h.name)
		}
		byH[h.name] = append(byH[h.name], h)
	}
	for _, w := range work {
		if !seen[w.name] {
			seen[w.name] = true
			order = append(order, w.name)
		}
		byW[w.name] = append(byW[w.name], w)
	}
	for _, name := range order {
		hs, ws := byH[name], byW[name]
		type pair struct{ hi, wi int }
		var pairs []pair
		for hi, h := range hs {
			for wi, w := range ws {
				if h.suite == w.suite && h.caseName == w.caseName {
					continue
				}
				if !sameRequest(h.step, w.step) {
					continue
				}
				pairs = append(pairs, pair{hi, wi})
			}
		}
		countH := map[int]int{}
		countW := map[int]int{}
		for _, p := range pairs {
			countH[p.hi]++
			countW[p.wi]++
		}
		usedH := map[int]bool{}
		usedW := map[int]bool{}
		for _, p := range pairs {
			if countH[p.hi] != 1 || countW[p.wi] != 1 {
				continue
			}
			h, w := hs[p.hi], ws[p.wi]
			usedH[p.hi] = true
			usedW[p.wi] = true
			moves = append(moves, StepChange{
				Suite: w.suite, Case: w.caseName, Name: w.name, Index: w.index,
				State:     changeMoved,
				FromSuite: h.suite, FromCase: h.caseName, FromName: h.name, FromIndex: h.index,
				Parts: stepParts(h.step, w.step),
			})
		}
		for i, h := range hs {
			if !usedH[i] {
				leftH = append(leftH, h)
			}
		}
		for i, w := range ws {
			if !usedW[i] {
				leftW = append(leftW, w)
			}
		}
	}
	return moves, leftH, leftW
}

func sameRequest(a, b model.Step) bool {
	if a.Protocol != b.Protocol {
		return false
	}
	if requestString(a, "method") != requestString(b, "method") || requestString(a, "url") != requestString(b, "url") {
		return false
	}
	return bodyText(a) == bodyText(b)
}

func stepParts(before, after model.Step) []PartChange {
	var parts []PartChange
	parts = addScalar(parts, "protocol", "", before.Protocol, after.Protocol, false)
	parts = addScalar(parts, "method", "", requestString(before, "method"), requestString(after, "method"), false)
	parts = addScalar(parts, "url", "", requestString(before, "url"), requestString(after, "url"), false)
	parts = append(parts, mapDiff("headers", headerMap(before), headerMap(after), true)...)
	parts = append(parts, mapDiff("query", queryParams(before), queryParams(after), true)...)
	if b, a := bodyText(before), bodyText(after); b != a {
		parts = append(parts, PartChange{Part: "body", Before: redactBody(b), After: redactBody(a)})
	}
	if ba, aa := authRefText(before.Auth), authRefText(after.Auth); ba != aa {
		parts = append(parts, PartChange{Part: "auth", Before: ba, After: aa})
	}
	if !jsonEqual(before.Assertions, after.Assertions) {
		parts = append(parts, PartChange{Part: "assertions", Before: redactJSON(before.Assertions), After: redactJSON(after.Assertions)})
	}
	if !jsonEqual(before.Transfers, after.Transfers) {
		parts = append(parts, PartChange{Part: "transfers", Before: redactJSON(before.Transfers), After: redactJSON(after.Transfers)})
	}
	parts = append(parts, mapDiff("vars", before.Vars, after.Vars, true)...)
	parts = addScalar(parts, "timeout", "", timeoutText(before.TimeoutMs), timeoutText(after.TimeoutMs), false)
	parts = addScalar(parts, "retry", "", retryText(before.Retry), retryText(after.Retry), false)
	parts = addScalar(parts, "skip", "", strconv.FormatBool(before.Skip), strconv.FormatBool(after.Skip), false)
	return parts
}

func addScalar(parts []PartChange, part, key, before, after string, place bool) []PartChange {
	if before == after {
		return parts
	}
	name := key
	if name == "" {
		name = part
	}
	return append(parts, PartChange{Part: part, Key: key, Before: redact(name, before, place), After: redact(name, after, place)})
}

func mapDiff(part string, before, after map[string]string, place bool) []PartChange {
	keys := make([]string, 0, len(before)+len(after))
	seen := map[string]bool{}
	for k := range before {
		seen[k] = true
		keys = append(keys, k)
	}
	for k := range after {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []PartChange
	for _, k := range keys {
		b, bok := before[k]
		a, aok := after[k]
		if bok && aok && b == a {
			continue
		}
		ch := PartChange{Part: part, Key: k}
		if bok {
			ch.Before = redact(k, b, place)
		}
		if aok {
			ch.After = redact(k, a, place)
		}
		out = append(out, ch)
	}
	return out
}

func requestString(step model.Step, key string) string {
	if step.Request == nil {
		return ""
	}
	return scalarString(step.Request[key])
}

func headerMap(step model.Step) map[string]string {
	if step.Request == nil {
		return nil
	}
	return stringMap(step.Request["headers"])
}

func queryParams(step model.Step) map[string]string {
	if step.Request == nil {
		return nil
	}
	v, ok := step.Request["query"]
	if !ok || v == nil {
		return nil
	}
	switch v.(type) {
	case map[string]any, map[string]string:
		return stringMap(v)
	default:
		return nil
	}
}

func bodyText(step model.Step) string {
	req := step.Request
	if len(req) == 0 {
		return ""
	}
	keys := bodyKeys(req)
	if len(keys) == 0 {
		return ""
	}
	if len(keys) == 1 && keys[0] == "body" {
		if s, ok := req["body"].(string); ok {
			return s
		}
	}
	m := make(map[string]any, len(keys))
	for _, k := range keys {
		m[k] = req[k]
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Sprint(m)
	}
	return string(b)
}

func bodyKeys(req map[string]any) []string {
	queryIsMap := false
	if v, ok := req["query"]; ok && v != nil {
		switch v.(type) {
		case map[string]any, map[string]string:
			queryIsMap = true
		}
	} else {
		queryIsMap = true
	}
	var keys []string
	for k := range req {
		switch k {
		case "method", "url", "headers":
			continue
		case "query":
			if queryIsMap {
				continue
			}
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func stringMap(v any) map[string]string {
	switch m := v.(type) {
	case nil:
		return nil
	case map[string]string:
		if len(m) == 0 {
			return nil
		}
		out := make(map[string]string, len(m))
		for k, val := range m {
			out[k] = val
		}
		return out
	case map[string]any:
		if len(m) == 0 {
			return nil
		}
		out := make(map[string]string, len(m))
		for k, val := range m {
			out[k] = scalarString(val)
		}
		return out
	default:
		return nil
	}
}

func scalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if t == math.Trunc(t) && !math.IsInf(t, 0) && !math.IsNaN(t) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
}

func authRefText(a *model.AuthRef) string {
	if a == nil {
		return ""
	}
	inherit := "true"
	if a.Inherit != nil && !*a.Inherit {
		inherit = "false"
	}
	return "use: [" + strings.Join(a.Use, ", ") + "]; inherit: " + inherit
}

func timeoutText(ms int) string {
	if ms == 0 {
		return ""
	}
	return strconv.Itoa(ms)
}

func retryText(r *model.Retry) string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("count=%d delayMs=%d until=%s", r.Count, r.DelayMs, r.Until)
}

func jsonEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return fmt.Sprint(a) == fmt.Sprint(b)
	}
	return bytes.Equal(ab, bb)
}

func redact(key, value string, place bool) string {
	if value == "" {
		return ""
	}
	if secretscan.Sensitive(key, value, place) {
		return redactedValue
	}
	return value
}

func redactBody(s string) string {
	if s == "" {
		return ""
	}
	// The whole side is replaced. Scanning the original text (not a map
	// round-trip) keeps duplicate JSON keys and numeric secrets visible.
	if secretscan.Sensitive("body", s, false) {
		return redactedValue
	}
	return s
}

func redactJSON(v any) string {
	if v == nil {
		return ""
	}
	if secretscan.Sensitive("", v, false) {
		return redactedValue
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func diffEnvs(head, work []model.Environment) []EnvChange {
	type item struct {
		n   int
		env model.Environment
	}
	index := func(list []model.Environment) ([]item, map[string]item) {
		n := map[string]int{}
		var order []item
		by := map[string]item{}
		for _, e := range list {
			k := e.Name + "#" + strconv.Itoa(n[e.Name])
			n[e.Name]++
			it := item{n: n[e.Name] - 1, env: e}
			order = append(order, it)
			by[k] = it
		}
		return order, by
	}
	hOrder, hMap := index(head)
	wOrder, _ := index(work)
	var out []EnvChange
	seen := map[string]bool{}
	for _, w := range wOrder {
		k := w.env.Name + "#" + strconv.Itoa(w.n)
		seen[k] = true
		h, ok := hMap[k]
		if !ok {
			out = append(out, EnvChange{Name: w.env.Name, State: changeAdded, Parts: envParts(model.Environment{}, w.env)})
			continue
		}
		parts := envParts(h.env, w.env)
		if len(parts) == 0 {
			continue
		}
		out = append(out, EnvChange{Name: w.env.Name, State: changeModified, Parts: parts})
	}
	for _, h := range hOrder {
		k := h.env.Name + "#" + strconv.Itoa(h.n)
		if seen[k] {
			continue
		}
		out = append(out, EnvChange{Name: h.env.Name, State: changeRemoved, Parts: envParts(h.env, model.Environment{})})
	}
	if out == nil {
		return []EnvChange{}
	}
	return out
}

func envParts(before, after model.Environment) []PartChange {
	var parts []PartChange
	parts = append(parts, mapDiff("variables", before.Variables, after.Variables, true)...)
	if ba, aa := authRefText(before.Auth), authRefText(after.Auth); ba != aa {
		if before.Name == "" {
			ba = ""
		}
		if after.Name == "" {
			aa = ""
		}
		parts = append(parts, PartChange{Part: "auth", Before: ba, After: aa})
	}
	if before.Default != after.Default {
		b, a := strconv.FormatBool(before.Default), strconv.FormatBool(after.Default)
		// A side that is not an environment (the zero value, used for add/remove)
		// has no default to show.
		if before.Name == "" {
			b = ""
		}
		if after.Name == "" {
			a = ""
		}
		parts = append(parts, PartChange{Part: "default", Before: b, After: a})
	}
	return parts
}

func diffAuth(head, work []model.AuthDef) []AuthChange {
	type item struct {
		n int
		a model.AuthDef
	}
	index := func(list []model.AuthDef) ([]item, map[string]item) {
		n := map[string]int{}
		var order []item
		by := map[string]item{}
		for _, a := range list {
			k := a.Name + "#" + strconv.Itoa(n[a.Name])
			it := item{n: n[a.Name], a: a}
			n[a.Name]++
			order = append(order, it)
			by[k] = it
		}
		return order, by
	}
	hOrder, hMap := index(head)
	wOrder, _ := index(work)
	var out []AuthChange
	seen := map[string]bool{}
	for _, w := range wOrder {
		k := w.a.Name + "#" + strconv.Itoa(w.n)
		seen[k] = true
		h, ok := hMap[k]
		if !ok {
			out = append(out, AuthChange{Name: w.a.Name, State: changeAdded, Parts: authParts(model.AuthDef{}, w.a)})
			continue
		}
		parts := authParts(h.a, w.a)
		if len(parts) == 0 {
			continue
		}
		out = append(out, AuthChange{Name: w.a.Name, State: changeModified, Parts: parts})
	}
	for _, h := range hOrder {
		k := h.a.Name + "#" + strconv.Itoa(h.n)
		if seen[k] {
			continue
		}
		out = append(out, AuthChange{Name: h.a.Name, State: changeRemoved, Parts: authParts(h.a, model.AuthDef{})})
	}
	if out == nil {
		return []AuthChange{}
	}
	return out
}

func authParts(before, after model.AuthDef) []PartChange {
	var parts []PartChange
	if before.Name == "" {
		parts = addScalar(parts, "type", "", "", after.Type, false)
	} else if after.Name == "" {
		parts = addScalar(parts, "type", "", before.Type, "", false)
	} else {
		parts = addScalar(parts, "type", "", before.Type, after.Type, false)
	}
	parts = append(parts, configDiff(before.Config, after.Config)...)
	return parts
}

// configDiff scans each config value as the original tree, with auth config
// as a credential place, before anything is turned into a display string.
// A nested password is therefore not scanned under the parent key.
func configDiff(before, after map[string]any) []PartChange {
	keys := make([]string, 0, len(before)+len(after))
	seen := map[string]bool{}
	for k := range before {
		seen[k] = true
		keys = append(keys, k)
	}
	for k := range after {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []PartChange
	for _, k := range keys {
		b, bok := before[k]
		a, aok := after[k]
		if bok && aok && jsonEqual(b, a) {
			continue
		}
		ch := PartChange{Part: "config", Key: k}
		if bok {
			ch.Before = displayConfig(k, b)
		}
		if aok {
			ch.After = displayConfig(k, a)
		}
		out = append(out, ch)
	}
	return out
}

func displayConfig(key string, v any) string {
	if secretscan.Sensitive(key, v, true) {
		return redactedValue
	}
	return scalarString(v)
}

type scopeItem struct {
	key    scopeKey
	parent string
	name   string
}

type scopeKey struct {
	parent  string
	parentN int
	name    string
	n       int
}

func suiteItems(p *model.Project) []scopeItem {
	if p == nil {
		return nil
	}
	n := map[string]int{}
	var out []scopeItem
	for _, s := range p.Suites {
		k := scopeKey{name: s.Name, n: n[s.Name]}
		n[s.Name]++
		out = append(out, scopeItem{key: k, name: s.Name})
	}
	return out
}

func caseItems(p *model.Project) []scopeItem {
	if p == nil {
		return nil
	}
	suiteN := map[string]int{}
	var out []scopeItem
	for _, s := range p.Suites {
		sn := suiteN[s.Name]
		suiteN[s.Name] = sn + 1
		caseN := map[string]int{}
		for _, c := range s.Cases {
			k := scopeKey{parent: s.Name, parentN: sn, name: c.Name, n: caseN[c.Name]}
			caseN[c.Name]++
			out = append(out, scopeItem{key: k, parent: s.Name, name: c.Name})
		}
	}
	return out
}

func diffScopes(head, work []scopeItem, withParent bool) []ScopeChange {
	hset := map[scopeKey]scopeItem{}
	for _, h := range head {
		hset[h.key] = h
	}
	wset := map[scopeKey]bool{}
	var out []ScopeChange
	for _, w := range work {
		wset[w.key] = true
		if _, ok := hset[w.key]; ok {
			continue
		}
		ch := ScopeChange{Name: w.name, State: changeAdded}
		if withParent {
			ch.Suite = w.parent
		}
		out = append(out, ch)
	}
	for _, h := range head {
		if wset[h.key] {
			continue
		}
		ch := ScopeChange{Name: h.name, State: changeRemoved}
		if withParent {
			ch.Suite = h.parent
		}
		out = append(out, ch)
	}
	if out == nil {
		return []ScopeChange{}
	}
	return out
}

func diffMeta(head, work *model.Project) []PartChange {
	var parts []PartChange
	parts = addScalar(parts, "name", "", head.Name, work.Name, false)
	parts = addScalar(parts, "description", "", head.Description, work.Description, false)
	parts = addScalar(parts, "schemaVersion", "", head.SchemaVersion, work.SchemaVersion, false)
	return parts
}

func diffSuiteFields(head, work *model.Project) []ScopeChange {
	if head == nil || work == nil {
		return nil
	}
	type item struct {
		n int
		s model.Suite
	}
	index := func(list []model.Suite) map[string]item {
		n := map[string]int{}
		by := map[string]item{}
		for _, s := range list {
			k := s.Name + "#" + strconv.Itoa(n[s.Name])
			by[k] = item{n: n[s.Name], s: s}
			n[s.Name]++
		}
		return by
	}
	hMap := index(head.Suites)
	n := map[string]int{}
	var out []ScopeChange
	for _, s := range work.Suites {
		k := s.Name + "#" + strconv.Itoa(n[s.Name])
		n[s.Name]++
		h, ok := hMap[k]
		if !ok {
			continue
		}
		parts := suiteParts(h.s, s)
		if len(parts) == 0 {
			continue
		}
		out = append(out, ScopeChange{Name: s.Name, State: changeModified, Parts: parts})
	}
	return out
}

func suiteParts(before, after model.Suite) []PartChange {
	var parts []PartChange
	parts = append(parts, mapDiff("vars", before.Vars, after.Vars, true)...)
	if ba, aa := authRefText(before.Auth), authRefText(after.Auth); ba != aa {
		parts = append(parts, PartChange{Part: "auth", Before: ba, After: aa})
	}
	parts = addScalar(parts, "parallel", "", strconv.Itoa(before.Parallel), strconv.Itoa(after.Parallel), false)
	if hookLabel(before.Setup) != hookLabel(after.Setup) {
		parts = append(parts, PartChange{Part: "setup", Before: hookLabel(before.Setup), After: hookLabel(after.Setup)})
	}
	if hookLabel(before.Teardown) != hookLabel(after.Teardown) {
		parts = append(parts, PartChange{Part: "teardown", Before: hookLabel(before.Teardown), After: hookLabel(after.Teardown)})
	}
	return parts
}

func hookLabel(h *model.Hook) string {
	if h == nil {
		return ""
	}
	return h.Name
}

func diffCaseFields(head, work *model.Project) []ScopeChange {
	type item struct {
		key    scopeKey
		parent string
		name   string
		c      model.TestCase
	}
	list := func(p *model.Project) []item {
		if p == nil {
			return nil
		}
		suiteN := map[string]int{}
		var out []item
		for _, s := range p.Suites {
			sn := suiteN[s.Name]
			suiteN[s.Name] = sn + 1
			caseN := map[string]int{}
			for _, c := range s.Cases {
				k := scopeKey{parent: s.Name, parentN: sn, name: c.Name, n: caseN[c.Name]}
				caseN[c.Name]++
				out = append(out, item{key: k, parent: s.Name, name: c.Name, c: c})
			}
		}
		return out
	}
	hMap := map[scopeKey]item{}
	for _, it := range list(head) {
		hMap[it.key] = it
	}
	var out []ScopeChange
	for _, it := range list(work) {
		before, ok := hMap[it.key]
		if !ok {
			continue
		}
		parts := caseParts(before.c, it.c)
		if len(parts) == 0 {
			continue
		}
		out = append(out, ScopeChange{Suite: it.parent, Name: it.name, State: changeModified, Parts: parts})
	}
	return out
}

func caseParts(before, after model.TestCase) []PartChange {
	var parts []PartChange
	parts = append(parts, mapDiff("vars", before.Vars, after.Vars, true)...)
	if ba, aa := authRefText(before.Auth), authRefText(after.Auth); ba != aa {
		parts = append(parts, PartChange{Part: "auth", Before: ba, After: aa})
	}
	if !jsonEqual(before.Feed, after.Feed) {
		parts = append(parts, PartChange{Part: "feed", Before: redactJSON(before.Feed), After: redactJSON(after.Feed)})
	}
	if bp, ap := boolPtrText(before.ContinueOnError), boolPtrText(after.ContinueOnError); bp != ap {
		parts = append(parts, PartChange{Part: "continueOnError", Before: bp, After: ap})
	}
	return parts
}

func boolPtrText(b *bool) string {
	if b == nil {
		return ""
	}
	return strconv.FormatBool(*b)
}

// ensureReported adds a generic project entry when the two loaded projects
// are not equal and nothing more specific was emitted. The Changes view
// treats an empty semantic diff as "no changes".
func ensureReported(ch *ProjectChanges, head, work *model.Project) {
	if ch == nil || hasReported(ch) || projectsEqual(head, work) {
		return
	}
	ch.Meta = append(ch.Meta, PartChange{Part: "project", After: "modified"})
}

func hasReported(ch *ProjectChanges) bool {
	return len(ch.Steps)+len(ch.Environments)+len(ch.Auth)+len(ch.Suites)+len(ch.Cases)+len(ch.Meta) > 0
}

func projectsEqual(a, b *model.Project) bool {
	if a == nil {
		a = &model.Project{}
	}
	if b == nil {
		b = &model.Project{}
	}
	return jsonEqual(comparableProject(a), comparableProject(b))
}

func comparableProject(p *model.Project) model.Project {
	c := *p
	c.Include = nil
	c.SourceDir = ""
	c.SourceFiles = nil
	return c
}
