package secrets

import (
	"context"
	"regexp"
)

type attemptRedactorKey struct{}

// AttemptScope creates a redactor view for one request attempt. Ordinary Add
// calls still register run-wide secrets; AddTemporary keeps values only in the
// returned view. The cleanup function discards those temporary values.
func (r *Redactor) AttemptScope(ctx context.Context) (context.Context, *Redactor, func()) {
	scoped := NewRedactor()
	scoped.parent = r
	ctx = context.WithValue(ctx, attemptRedactorKey{}, scoped)
	return ctx, scoped, func() {
		scoped.mu.Lock()
		scoped.literals = map[string]struct{}{}
		scoped.short = map[string]*regexp.Regexp{}
		scoped.ordered = nil
		scoped.shortList = nil
		scoped.mu.Unlock()
	}
}

// AddPerRequest registers a value that changes with every request (a
// signature): only for the current attempt when ctx belongs to an
// AttemptScope, so the run-wide literal set does not grow with every
// request; run-wide otherwise, so the value is never left unregistered.
func (r *Redactor) AddPerRequest(ctx context.Context, secret string) {
	if ctx != nil {
		if scoped, ok := ctx.Value(attemptRedactorKey{}).(*Redactor); ok {
			scoped.addLocal(secret)
			return
		}
	}
	r.Add(secret)
}

// AddTemporary registers a value only when ctx belongs to an AttemptScope.
// Outside an attempt the structural redaction patterns remain the fallback.
func (r *Redactor) AddTemporary(ctx context.Context, secret string) {
	if scoped, ok := ctx.Value(attemptRedactorKey{}).(*Redactor); ok {
		scoped.addLocal(secret)
	}
}

func (r *Redactor) addLocal(secret string) {
	if secret == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(secret) < 4 {
		if _, ok := r.short[secret]; !ok {
			re := shortSecretPattern(secret)
			r.short[secret] = re
			list := make([]*regexp.Regexp, len(r.shortList), len(r.shortList)+1)
			copy(list, r.shortList)
			r.shortList = append(list, re)
		}
		return
	}
	if _, ok := r.literals[secret]; ok {
		return
	}
	r.literals[secret] = struct{}{}
	r.ordered = insertOrdered(r.ordered, secret)
}
