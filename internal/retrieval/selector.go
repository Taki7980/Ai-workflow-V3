package retrieval

import (
	"errors"
	"fmt"
	"sort"
)

var (
	ErrRequiredEvidenceOverBudget         = errors.New("required evidence exceeds token budget")
	ErrRequiredEvidenceOverCandidateLimit = errors.New("required evidence exceeds candidate limit")
)

type Candidate[T any] struct {
	Key             string
	Text            string
	Value           T
	Relevance       float64
	EstimatedTokens int
	Required        bool
}

type SelectorOptions struct {
	MaxTokens         int
	MaxCandidates     int
	Lambda            float64
	MandatoryRequired bool
}

type Selection[T any] struct {
	Items      []Candidate[T]
	UsedTokens int
}

type selectorCandidate[T any] struct {
	Candidate[T]
	order    int
	tokenSet map[string]struct{}
}

// SelectMMR deduplicates candidates by key, then greedily selects items
// within options.MaxCandidates and options.MaxTokens using Maximal Marginal
// Relevance to balance relevance against redundancy. Required candidates are
// always included first, and when options.MandatoryRequired is set, an
// error is returned if their combined token cost exceeds the budget.
func SelectMMR[T any](candidates []Candidate[T], options SelectorOptions) (Selection[T], error) {
	if options.MaxTokens <= 0 {
		return Selection[T]{}, fmt.Errorf("max tokens must be positive")
	}
	if options.MaxCandidates <= 0 {
		return Selection[T]{}, fmt.Errorf("max candidates must be positive")
	}
	if options.Lambda < 0 || options.Lambda > 1 {
		return Selection[T]{}, fmt.Errorf("lambda must be between 0 and 1")
	}

	deduped := make([]selectorCandidate[T], 0, len(candidates))
	byKey := map[string]int{}
	for i, candidate := range candidates {
		if candidate.Key == "" {
			return Selection[T]{}, fmt.Errorf("candidate key must not be empty")
		}
		if candidate.EstimatedTokens <= 0 {
			return Selection[T]{}, fmt.Errorf("candidate %q token estimate must be positive", candidate.Key)
		}

		if idx, ok := byKey[candidate.Key]; ok {
			current := &deduped[idx]
			required := current.Required || candidate.Required
			maxTokens := current.EstimatedTokens
			if candidate.EstimatedTokens > maxTokens {
				maxTokens = candidate.EstimatedTokens
			}
			if candidate.Relevance > current.Relevance {
				order := current.order
				*current = selectorCandidate[T]{
					Candidate: Candidate[T]{
						Key:             candidate.Key,
						Text:            candidate.Text,
						Value:           candidate.Value,
						Relevance:       candidate.Relevance,
						EstimatedTokens: maxTokens,
						Required:        required,
					},
					order: order,
				}
			} else {
				current.Required = required
				current.EstimatedTokens = maxTokens
			}
			continue
		}

		byKey[candidate.Key] = len(deduped)
		deduped = append(deduped, selectorCandidate[T]{Candidate: candidate, order: i})
	}

	required := make([]selectorCandidate[T], 0)
	optional := make([]selectorCandidate[T], 0)
	for _, candidate := range deduped {
		if candidate.Required {
			required = append(required, candidate)
		} else {
			optional = append(optional, candidate)
		}
	}
	if len(required) > options.MaxCandidates {
		return Selection[T]{}, ErrRequiredEvidenceOverCandidateLimit
	}

	sort.SliceStable(required, func(i, j int) bool {
		if required[i].Relevance == required[j].Relevance {
			return required[i].order < required[j].order
		}
		return required[i].Relevance > required[j].Relevance
	})
	sort.SliceStable(optional, func(i, j int) bool {
		if optional[i].Relevance == optional[j].Relevance {
			return optional[i].order < optional[j].order
		}
		return optional[i].Relevance > optional[j].Relevance
	})

	pool := make([]selectorCandidate[T], 0, options.MaxCandidates)
	pool = append(pool, required...)
	remainingSlots := options.MaxCandidates - len(pool)
	if remainingSlots > len(optional) {
		remainingSlots = len(optional)
	}
	pool = append(pool, optional[:remainingSlots]...)

	for i := range pool {
		pool[i].tokenSet = tokenSet(pool[i].Text)
	}

	result := Selection[T]{Items: []Candidate[T]{}}
	selectedSets := make([]map[string]struct{}, 0, len(pool))
	selected := map[string]struct{}{}

	if options.MandatoryRequired {
		requiredTokens := 0
		for _, candidate := range required {
			requiredTokens += candidate.EstimatedTokens
		}
		if requiredTokens > options.MaxTokens {
			return Selection[T]{}, ErrRequiredEvidenceOverBudget
		}
		for _, candidate := range required {
			result.Items = append(result.Items, candidate.Candidate)
			result.UsedTokens += candidate.EstimatedTokens
			selected[candidate.Key] = struct{}{}
			selectedSets = append(selectedSets, candidate.tokenSet)
		}
	}

	maxRelevance := 0.0
	for _, candidate := range pool {
		if _, ok := selected[candidate.Key]; ok {
			continue
		}
		if candidate.Relevance > maxRelevance {
			maxRelevance = candidate.Relevance
		}
	}
	if maxRelevance <= 0 {
		return result, nil
	}

	for {
		best := -1
		bestMMR := 0.0
		bestRelevance := 0.0
		haveBest := false

		for i := range pool {
			candidate := pool[i]
			if _, ok := selected[candidate.Key]; ok {
				continue
			}
			if candidate.Relevance <= 0 {
				continue
			}
			if result.UsedTokens+candidate.EstimatedTokens > options.MaxTokens {
				continue
			}

			normalized := candidate.Relevance / maxRelevance
			maxSimilarity := 0.0
			for _, set := range selectedSets {
				if similarity := Jaccard(candidate.tokenSet, set); similarity > maxSimilarity {
					maxSimilarity = similarity
				}
			}
			score := options.Lambda*normalized - (1-options.Lambda)*maxSimilarity

			if !haveBest ||
				score > bestMMR ||
				(score == bestMMR && candidate.Relevance > bestRelevance) ||
				(score == bestMMR && candidate.Relevance == bestRelevance && candidate.order < pool[best].order) {
				best = i
				bestMMR = score
				bestRelevance = candidate.Relevance
				haveBest = true
			}
		}

		if !haveBest {
			break
		}
		candidate := pool[best]
		result.Items = append(result.Items, candidate.Candidate)
		result.UsedTokens += candidate.EstimatedTokens
		selected[candidate.Key] = struct{}{}
		selectedSets = append(selectedSets, candidate.tokenSet)
	}

	return result, nil
}

// tokenSet tokenizes text and returns the resulting tokens as a set.
func tokenSet(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, token := range Tokenize(text) {
		out[token] = struct{}{}
	}
	return out
}
