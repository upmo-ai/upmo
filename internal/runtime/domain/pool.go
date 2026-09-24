package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

const (
	// PoolFloor is the fewest idle base containers the pool keeps.
	PoolFloor = 1
	// PoolCap is the most idle base containers the pool holds.
	PoolCap = 2
)

// ErrPool reports a claim on an empty pool or a blank, duplicate, or excess supply.
var ErrPool = errors.New("runtime: pool")

// ErrCache reports an empty lockfile.
var ErrCache = errors.New("runtime: cache")

// Pool holds the idle base container ids, oldest first, and the number of refills owed.
type Pool struct {
	Idle   []string
	Refill int
}

// OpenPool returns a pool with no idle containers that owes PoolFloor refills.
func OpenPool() Pool {
	return Pool{Idle: []string{}, Refill: PoolFloor}
}

// Claim returns the pool after it hands out its oldest idle container, and that container's id.
func Claim(pool Pool) (Pool, string, error) {
	if len(pool.Idle) == 0 {
		next := Pool{Idle: []string{}, Refill: pool.Refill}
		if next.Refill < PoolFloor {
			next.Refill = PoolFloor
		}
		return next, "", ErrPool
	}

	id := pool.Idle[0]
	idle := make([]string, len(pool.Idle)-1)
	copy(idle, pool.Idle[1:])

	next := Pool{Idle: idle, Refill: pool.Refill}
	if len(idle) < PoolFloor {
		next.Refill += PoolFloor - len(idle)
	}

	return next, id, nil
}

// Supply returns the pool after it takes one new idle container.
func Supply(pool Pool, id string) (Pool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return pool, ErrPool
	}
	for _, idle := range pool.Idle {
		if idle == id {
			return pool, ErrPool
		}
	}
	if len(pool.Idle) >= PoolCap {
		return pool, ErrPool
	}

	idle := make([]string, len(pool.Idle), len(pool.Idle)+1)
	copy(idle, pool.Idle)
	idle = append(idle, id)

	next := Pool{Idle: idle, Refill: pool.Refill}
	if next.Refill > 0 {
		next.Refill--
	}

	return next, nil
}

// LockKey returns the hex SHA-256 of a lockfile as its cache key.
func LockKey(lockfile []byte) (string, error) {
	if len(lockfile) == 0 {
		return "", ErrCache
	}

	sum := sha256.Sum256(lockfile)
	return hex.EncodeToString(sum[:]), nil
}
