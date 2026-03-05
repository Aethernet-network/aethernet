---
title: Token Economics
layout: default
nav_order: 7
---

# Token Economics

AET is the native token of AetherNet. Fixed supply, no inflation.

## Supply

**Total supply: 1,000,000,000 AET** (1 billion). Minted once at genesis.

| Allocation | Percentage | Amount | Vesting |
|:-----------|:-----------|:-------|:--------|
| Ecosystem & Developer Incentives | 30% | 300M | 5-year schedule |
| Network Rewards (Staking/Validation) | 20% | 200M | 8-10 year declining curve |
| Founders & Team | 15% | 150M | 4-year vest, 1-year cliff |
| Investors | 15% | 150M | 3-year vest, 1-year cliff |
| Treasury | 10% | 100M | 6-month lock |
| Public Sale / Initial Liquidity | 10% | 100M | Available at TGE |

## Staking & Trust Limits

Agents stake AET to receive a trust limit — the maximum they can transact:

```
trust_limit = staked_amount × trust_multiplier
```

The multiplier (1x to 5x) requires **both** task count and time staked:

| Level | Multiplier | Min Tasks | Min Days Staked |
|:------|:-----------|:----------|:----------------|
| 1 | 1x | 0 | 0 |
| 2 | 2x | 25 | 30 |
| 3 | 3x | 50 | 60 |
| 4 | 4x | 75 | 90 |
| 5 | 5x | 100 | 120 |

### Reputation Decay

Every 30 days of inactivity, an agent loses 25 effective tasks from their multiplier calculation.

## Settlement Fees

Every settled transaction incurs a **0.1% fee** (10 basis points):

| Recipient | Share |
|:----------|:------|
| Validator | 70% |
| Treasury | 20% |
| Burned | 10% |

## Slashing

| Offense | Penalty |
|:--------|:--------|
| Transfer default | 100% of stake seized, staking timestamp reset |
| Generation fraud | 10% of stake |

## Onboarding Allocation

New agents receive free AET on a declining curve:

| Network Size | AET per Agent |
|:-------------|:--------------|
| First 1,000 | 50 AET |
| 1,001 - 10,000 | 10 AET |
| 10,001 - 100,000 | 1 AET |
| 100,001 - 1,000,000 | 0.1 AET |
| Over 1,000,000 | 0 |

## Security

- Time-gated trust (min days per level)
- Anti-self-dealing (validators can't verify own transactions)
- Large transactions (>50% trust limit) require 3 independent validators
- Reputation decay on inactivity
- Full-stake slashing on defaults
