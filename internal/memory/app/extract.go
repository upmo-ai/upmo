// Package app extracts personal facts from one piece of user-authored text.
// Chat and tasks call it when they exist. This package does no SQL.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/memory/domain"
	"github.com/upmo-ai/upmo/internal/platform/trustfence"
)

// ErrRetry is a transient proposer or embed failure. The caller retries.
// Nothing was stored.
var ErrRetry = errors.New("memory: retry")

const proposeTimeout = 30 * time.Second

// A source is extracted at most ExtractLimit times in ExtractWindow, measured
// back from the source time.
const (
	ExtractLimit  = 5
	ExtractWindow = time.Hour
)

// Source is one user-authored text. Kind, ID and Seq identify the job.
// Deny lists claims already shown to the person on that turn.
type Source struct {
	UserID string
	Kind   string
	ID     string
	Seq    int
	Text   string
	At     time.Time
	Deny   []string
}

// Key is the idempotency identity of a source.
type Key struct {
	Kind string
	ID   string
	Seq  int
}

// Fact is one tool row to insert. Trust is always tool.
type Fact struct {
	Claim    string
	Hash     [32]byte
	Vector   []float32
	Model    string
	Kind     string
	SourceID string
	Seq      int
	At       time.Time
}

// Report is what one run did. A duplicate, a skip or a limited run stores no
// new claim.
type Report struct {
	Duplicate  bool
	Skipped    bool
	Limited    bool
	Inserted   int
	Reinforced int
	Dropped    int
}

// Proposer is one gateway propose call. A transient failure wraps ErrRetry.
type Proposer interface {
	Propose(ctx context.Context, prompt string) (string, error)
}

// Embedder embeds every surviving claim in one batch. A transient failure
// wraps ErrRetry. The model name is the one stored on the fact.
type Embedder interface {
	Embed(ctx context.Context, texts []string) (model string, vectors [][]float32, err error)
}

// Store runs fn under a per-person lock in one transaction. An error rolls
// the transaction back, including a retryable embed failure.
type Store interface {
	InTx(ctx context.Context, userID string, fn func(Tx) error) error
}

// Tx is the person's facts inside that transaction.
type Tx interface {
	Already(ctx context.Context, key Key) (bool, error)
	Record(ctx context.Context, key Key, at time.Time) error
	Recent(ctx context.Context, kind, id string, since time.Time) (int, error)
	Hashes(ctx context.Context) (active, forgotten [][32]byte, err error)
	Confirm(ctx context.Context, hash [32]byte, at time.Time) error
	ConfirmID(ctx context.Context, id string, at time.Time) error
	Near(ctx context.Context, vector []float32) (action, id string, err error)
	Add(ctx context.Context, fact Fact) error
}

type kept struct {
	claim string
	hash  [32]byte
}

// Extract redacts and fences the person's words, proposes at most five
// claims, gates them, then embeds the survivors. A drop does not fail the
// call. A retryable embed error stores nothing.
func Extract(ctx context.Context, store Store, proposer Proposer, embedder Embedder, src Source) (Report, error) {
	src.Kind = strings.TrimSpace(src.Kind)
	if err := src.ok(); err != nil {
		return Report{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, proposeTimeout)
	defer cancel()

	var rep Report
	err := store.InTx(ctx, src.UserID, func(tx Tx) error {
		done, err := tx.Already(ctx, src.key())
		if err != nil {
			return err
		}
		if done {
			rep.Duplicate = true
			return nil
		}
		recent, err := tx.Recent(ctx, src.Kind, src.ID, src.At.Add(-ExtractWindow))
		if err != nil {
			return err
		}
		if recent >= ExtractLimit {
			rep.Limited = true
			return nil
		}
		prepared, ok := domain.Prepare(src.Text)
		if !ok {
			rep.Skipped = true
			return tx.Record(ctx, src.key(), src.At)
		}
		fenced := trustfence.Wrap(trustfence.TagUntrust, "user", trustfence.NeutralizeSlot(domain.Redact(prepared)))
		batch, giveUp, err := proposeTwice(ctx, proposer, prompt(src.At, fenced))
		if err != nil {
			return err
		}
		if giveUp {
			return tx.Record(ctx, src.key(), src.At)
		}
		active, forgotten, err := tx.Hashes(ctx)
		if err != nil {
			return err
		}
		held := map[[32]byte]bool{}
		for _, hash := range active {
			held[hash] = true
		}
		var keep []kept
		for _, item := range batch {
			got := domain.Gate(domain.Input{
				PreparedSource:  prepared,
				Candidate:       domain.Candidate{Claim: item.Claim, ChangesBehavior: item.ChangesBehavior},
				DenyClaims:      src.Deny,
				ActiveHashes:    active,
				ForgottenHashes: forgotten,
			})
			switch got.Action {
			case domain.ActionDrop:
				rep.Dropped++
			case domain.ActionReinforce:
				if err := tx.Confirm(ctx, got.Hash, src.At); err != nil {
					return err
				}
				rep.Reinforced++
			default:
				if held[got.Hash] {
					if err := tx.Confirm(ctx, got.Hash, src.At); err != nil {
						return err
					}
					rep.Reinforced++
					continue
				}
				held[got.Hash] = true
				active = append(active, got.Hash)
				keep = append(keep, kept{claim: domain.Redact(item.Claim), hash: got.Hash})
			}
		}
		if len(keep) > 0 {
			if err := embedKeep(ctx, tx, embedder, src, keep, &rep); err != nil {
				return err
			}
		}
		return tx.Record(ctx, src.key(), src.At)
	})
	if err != nil {
		return Report{}, err
	}
	return rep, nil
}

func embedKeep(ctx context.Context, tx Tx, embedder Embedder, src Source, keep []kept, rep *Report) error {
	texts := make([]string, len(keep))
	for i, item := range keep {
		texts[i] = item.claim
	}
	model, vectors, err := embedder.Embed(ctx, texts)
	if err != nil {
		return err
	}
	if model == "" {
		return fmt.Errorf("memory: embed model is empty")
	}
	if len(vectors) != len(keep) {
		return fmt.Errorf("memory: embed count %d, want %d", len(vectors), len(keep))
	}
	for i, item := range keep {
		if err := domain.Valid(vectors[i]); err != nil {
			return err
		}
		action, id, err := tx.Near(ctx, vectors[i])
		if err != nil {
			return err
		}
		switch action {
		case "drop":
			rep.Dropped++
		case "reinforce":
			if err := tx.ConfirmID(ctx, id, src.At); err != nil {
				return err
			}
			rep.Reinforced++
		default:
			if err := tx.Add(ctx, Fact{
				Claim:    item.claim,
				Hash:     item.hash,
				Vector:   vectors[i],
				Model:    model,
				Kind:     src.Kind,
				SourceID: src.ID,
				Seq:      src.Seq,
				At:       src.At,
			}); err != nil {
				return err
			}
			rep.Inserted++
		}
	}
	return nil
}

// proposeTwice calls the model once, and once more when the body is not a
// valid batch. A second bad body ends the run. A proposer error is returned.
func proposeTwice(ctx context.Context, proposer Proposer, body string) ([]domain.Proposal, bool, error) {
	raw, err := proposer.Propose(ctx, body)
	if err != nil {
		return nil, false, err
	}
	batch, err := domain.ParseProposals([]byte(raw))
	if err == nil {
		return batch, false, nil
	}
	raw, err = proposer.Propose(ctx, body)
	if err != nil {
		return nil, false, err
	}
	batch, err = domain.ParseProposals([]byte(raw))
	if err != nil {
		return nil, true, nil
	}
	return batch, false, nil
}

func prompt(at time.Time, fenced string) string {
	return "The person's message was written at " + at.UTC().Format(time.RFC3339) + ".\n" +
		"List at most 5 atomic claims from the fenced message that would change a future reply.\n" +
		"Do not list standing instructions or preferences.\n" +
		"Reply with a JSON array. Each item has claim and changes_behavior.\n" +
		fenced
}

func (s Source) key() Key {
	return Key{Kind: s.Kind, ID: s.ID, Seq: s.Seq}
}

func (s Source) ok() error {
	if _, err := uuid.Parse(s.UserID); err != nil {
		return fmt.Errorf("memory: user id")
	}
	if _, err := uuid.Parse(s.ID); err != nil {
		return fmt.Errorf("memory: source id")
	}
	if s.Kind == "" || s.Seq < 0 {
		return fmt.Errorf("memory: source")
	}
	if s.At.IsZero() {
		return fmt.Errorf("memory: source time")
	}
	return nil
}
