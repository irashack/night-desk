package model

import (
	"errors"
	"sync"
)

var ErrBudget = errors.New("model: run spending limit reached")

type Limits struct {
	MaxCalls  int `json:"max_calls"`
	MaxTokens int `json:"max_tokens"`
}

// Spending is a run's calls and tokens, and its estimated cost in millionths of a
// US dollar from the tiers' configured prices (zero for a tier without one).
type Spending struct {
	Calls, Tokens int
	CostMicros    int64
}

// Budget is shared by both tiers for exactly one run. Tokens includes input and
// output. Before each HTTP attempt it reserves UTF-8 bytes plus framing for input
// and the requested output ceiling. Failed/unknown attempts keep the reservation;
// successful attempts settle to provider usage. Providers must honor max_tokens.
// A tokenizer-independent estimate cannot enforce a remote provider's billing;
// usage above the reservation exhausts the budget before another call is allowed.
type Budget struct {
	mu     sync.Mutex
	limits Limits
	spent  Spending
}

func NewBudget(l Limits) (*Budget, error) {
	if l.MaxCalls <= 0 {
		return nil, Problem{"max_calls", "must be positive"}
	}
	if l.MaxTokens <= 0 {
		return nil, Problem{"max_tokens", "must be positive"}
	}
	return &Budget{limits: l}, nil
}
func (b *Budget) Spending() Spending { b.mu.Lock(); defer b.mu.Unlock(); return b.spent }
func (b *Budget) reserve(n int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.spent.Calls >= b.limits.MaxCalls || n > b.limits.MaxTokens-b.spent.Tokens {
		return ErrBudget
	}
	b.spent.Calls++
	b.spent.Tokens += n
	return nil
}

// addCost records a call's estimated cost: tokens times US dollars per million
// tokens is millionths of a dollar. Input is priced at the base rate, so cached
// input is overestimated.
func (b *Budget) addCost(p *Price, input, output int) {
	if p == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent.CostMicros += int64(float64(input)*p.InputPerMTok + float64(output)*p.OutputPerMTok + 0.5)
}
func (b *Budget) settle(reserved, actual int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent.Tokens += actual - reserved
	if b.spent.Tokens > b.limits.MaxTokens {
		return ErrBudget
	}
	return nil
}
