package derivation

import (
	"errors"
	"fmt"
	"sort"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/protocolmath"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// deriveReject implements Plan v3 §2.3 step 6 reject branch.
// No gen-ledger on reject; poster gets the worker-share; agreeing
// validators (those who voted Fail) split the validator pool W-weighted;
// treasury absorbs remainders + validator-pool-when-empty.
//
// Mirrors pre-5B settler integer-path arithmetic at
// internal/settlement/verification_consensus_settler.go:282-284:
//   posterAmount   = budget * WorkerShareBP    / SharesDenominator  (note: poster gets worker-share)
//   validatorPool  = budget * ValidatorShareBP / SharesDenominator
//   treasuryAmount = budget - posterAmount - validatorPool
func deriveReject(
	round *taskverification.TaskVerificationRound,
	cutoff cutoffResult,
	wImpl CanonicalWProjection,
	_ DerivationInputs,
	budget uint64,
	posterID crypto.AgentID,
	fundingRef event.EventID,
	treasuryID crypto.AgentID,
) (records []PayoutRecord, status DerivationStatus, cause DeferredCause, summary partialSummary, err error) {
	posterAmount := budget * WorkerShareBP / SharesDenominator
	validatorPool := budget * ValidatorShareBP / SharesDenominator
	treasuryAmount := budget - posterAmount - validatorPool

	settlementKey := SettlementKey{
		RoundID:          string(round.RoundID),
		TaskID:           round.TaskID,
		FundingReference: fundingRef,
	}

	// 1. Poster refund (using worker-share amount per pre-5B convention).
	if posterAmount > 0 {
		records = append(records, PayoutRecord{
			DerivationVersion: DerivationVersion,
			SettlementKey:     settlementKey,
			Recipient:         Recipient{ID: posterID, Role: RolePosterRefund},
			Amount:            Amount{Value: posterAmount, Currency: CurrencyAET},
			Purpose:           Purpose{Tag: TagPosterRefund},
		})
	}

	// 2. Validator distribution: agreeing = those who voted Fail.
	agreeing := collectAgreeingValidators(round, taskverification.VerdictFail)
	summary.AgreeingValidatorCount = uint32(len(agreeing))
	if len(agreeing) == 0 || validatorPool == 0 {
		treasuryAmount += validatorPool
	} else {
		family := ""
		recips := make([]protocolmath.Recipient, 0, len(agreeing))
		sort.Slice(agreeing, func(i, j int) bool { return agreeing[i] < agreeing[j] })
		for _, v := range agreeing {
			w, lookupErr := wImpl.Lookup(v, family, round.Category, posterID, budget, cutoff.epoch)
			if lookupErr != nil {
				if errors.Is(lookupErr, dag.ErrEventNotFound) {
					return nil, StatusDeferred, DeferredCauseWLookup, summary, nil
				}
				return nil, 0, 0, summary, fmt.Errorf("derivation: W.Lookup for validator %s: %w", v, lookupErr)
			}
			if w < 0 {
				w = 0
			}
			recips = append(recips, protocolmath.Recipient{
				CanonicalKey: []byte(v),
				Weight:       w,
			})
		}
		alloc, allocErr := protocolmath.AllocateWithCeiling(recips, protocolmath.MicroAET(validatorPool))
		if allocErr != nil {
			return nil, 0, 0, summary, fmt.Errorf("derivation: validator pool allocate: %w", allocErr)
		}
		keys := make([]string, 0, len(alloc))
		for k := range alloc {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			amt := uint64(alloc[k])
			if amt == 0 {
				continue
			}
			records = append(records, PayoutRecord{
				DerivationVersion: DerivationVersion,
				SettlementKey:     settlementKey,
				Recipient:         Recipient{ID: crypto.AgentID(k), Role: RoleValidator},
				Amount:            Amount{Value: amt, Currency: CurrencyAET},
				Purpose:           Purpose{Tag: TagValidatorDistribution},
			})
		}
	}

	// 3. Treasury (no gen-ledger on reject — generation pool is not
	// allocated at all on reject; the budget split is poster + validator
	// + treasury, no third pool).
	if treasuryAmount > 0 {
		records = append(records, PayoutRecord{
			DerivationVersion: DerivationVersion,
			SettlementKey:     settlementKey,
			Recipient:         Recipient{ID: treasuryID, Role: RoleTreasury},
			Amount:            Amount{Value: treasuryAmount, Currency: CurrencyAET},
			Purpose:           Purpose{Tag: TagTreasuryRemainder},
		})
	}

	return records, StatusDerived, 0, summary, nil
}
