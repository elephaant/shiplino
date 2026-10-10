package model

import (
	"crypto/rand"
	"errors"
	"sync"
	"time"
)

// ULIDs are 26-character, time-sortable ids: 48 bits of Unix milliseconds
// followed by 80 bits of randomness, in Crockford base32
// (https://github.com/ulid/spec). Ids from one generator are strictly
// increasing, even within the same millisecond.

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var ulidGen struct {
	sync.Mutex
	lastMS uint64
	rnd    [10]byte
}

// NewULID returns a new monotonic ULID for time t.
func NewULID(t time.Time) string {
	ms := uint64(t.UnixMilli())

	ulidGen.Lock()
	if ms <= ulidGen.lastMS {
		// Same (or earlier) millisecond: keep the last timestamp and
		// increment the random part so ids stay strictly increasing.
		ms = ulidGen.lastMS
		incr(&ulidGen.rnd)
	} else {
		ulidGen.lastMS = ms
		_, _ = rand.Read(ulidGen.rnd[:])
	}
	var b [16]byte
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (40 - 8*i))
	}
	copy(b[6:], ulidGen.rnd[:])
	ulidGen.Unlock()

	return encode(b)
}

func incr(r *[10]byte) {
	for i := len(r) - 1; i >= 0; i-- {
		r[i]++
		if r[i] != 0 {
			return
		}
	}
}

func encode(b [16]byte) string {
	var out [26]byte
	// 128 bits → 26 groups of 5 bits (the first group holds the top 3 bits).
	var acc uint64
	bits := 0
	pos := 25
	for i := 15; i >= 0; i-- {
		acc |= uint64(b[i]) << bits
		bits += 8
		for bits >= 5 && pos >= 0 {
			out[pos] = crockford[acc&31]
			acc >>= 5
			bits -= 5
			pos--
		}
	}
	if pos >= 0 {
		out[pos] = crockford[acc&31]
	}
	return string(out[:])
}

// ULIDTime returns the timestamp encoded in a ULID.
func ULIDTime(id string) (time.Time, error) {
	if len(id) != 26 {
		return time.Time{}, errors.New("ulid: wrong length")
	}
	var ms uint64
	for i := 0; i < 10; i++ {
		v := indexCrockford(id[i])
		if v < 0 {
			return time.Time{}, errors.New("ulid: invalid character")
		}
		ms = ms<<5 | uint64(v)
	}
	// 26 chars carry 130 bits; the top 2 are padding, so the first 10
	// characters (50 bits) are exactly the 48-bit timestamp.
	return time.UnixMilli(int64(ms)), nil
}

func indexCrockford(c byte) int {
	for i := 0; i < len(crockford); i++ {
		if crockford[i] == c {
			return i
		}
	}
	return -1
}
