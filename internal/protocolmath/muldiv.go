package protocolmath

import "math/big"

// mulDivBig computes (a * b) / c using math/big.Int and returns the result
// as MicroAET. The multiplication is performed at arbitrary precision so
// the intermediate product cannot overflow. The division is integer
// (floor) division.
//
// Panics if c is zero, or if the quotient does not fit in uint64. In
// protocolmath's production usage the result is bounded by the allocation
// pool (a MicroAET value, which fits in uint64 by construction), so the
// panic is a true impossibility-check rather than a runtime concern. The
// overflow test exercises the panic path directly to prove it fires when
// the invariant is broken.
func mulDivBig(a, b, c *big.Int) MicroAET {
	if c.Sign() == 0 {
		panic("protocolmath: mulDivBig divide by zero")
	}
	var prod, quot big.Int
	prod.Mul(a, b)
	quot.Quo(&prod, c)
	if !quot.IsUint64() {
		panic("protocolmath: mulDivBig result exceeds uint64")
	}
	return MicroAET(quot.Uint64())
}
