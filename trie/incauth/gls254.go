//go:build gls254 && cgo && amd64

package incauth

/*
#include <stddef.h>
#cgo CXXFLAGS: -std=c++14 -O3 -march=westmere -I${SRCDIR}/../../build/cache/gls254/deps/openssl/include -I${SRCDIR}/../../build/cache/gls254/deps/jbms-utility/src -I${SRCDIR}/../../build/cache/gls254/deps/array_view/src -I${SRCDIR}/../../build/cache/gls254/deps/jbms-openssl/src -I${SRCDIR}/../../build/cache/gls254/sysroot/usr/include -I${SRCDIR}/../../build/cache/gls254/ecmh-src/src -I${SRCDIR}/../../build/cache/gls254/ecmh-build/src
#cgo LDFLAGS: ${SRCDIR}/../../build/cache/gls254/ecmh-build/libblake2b.a ${SRCDIR}/../../build/cache/gls254/ecmh-build/libec_GLS254.a ${SRCDIR}/../../build/cache/gls254/ecmh-build/libGF2_254.a ${SRCDIR}/../../build/cache/gls254/ecmh-build/libGF2_127_63.a -lstdc++
int incauth_gls_decode(unsigned char*, const unsigned char*);
int incauth_gls_map(unsigned char*, const unsigned char*, size_t);
int incauth_gls_apply(unsigned char*, unsigned char*, const unsigned char*, const unsigned char*, size_t);
*/
import "C"

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/cpu"
)

// The internal lambda-projective point has three 32-byte field elements.
// It is Go-owned, pointer-free and immutable once cached. C copies via memcpy
// into aligned locals, and never retains a Go pointer. Only compressed32 is
// persisted; this 96-byte representation is neither a codec nor a public input.
type glsPoint [96]byte

func (p *glsPoint) copyForCache() interface{} { q := *p; return &q }

type glsBackend struct {
	fixed       map[string]*glsPoint
	cache       *ComputationCache
	cachePrefix string
}

func init() {
	newGLS254Backend = func() (Backend, error) {
		if !cpu.X86.HasSSE41 || !cpu.X86.HasSSE42 || !cpu.X86.HasPCLMULQDQ || !cpu.X86.HasAES || !cpu.X86.HasPOPCNT {
			return nil, errors.New("GLS254 requires Westmere instructions (SSE4.1/4.2, PCLMULQDQ, AES, POPCNT)")
		}
		return &glsBackend{}, nil
	}
}

func (*glsBackend) ID() string       { return GLS254 }
func (*glsBackend) Size() int        { return 32 }
func (*glsBackend) canonicalOutput() {} // Apply only returns checked-codec output.
func (b *glsBackend) bindScheme(s *Scheme) Backend {
	clone := *b
	clone.fixed = make(map[string]*glsPoint, 16)
	for i := uint8(0); i < 16; i++ {
		m := s.edge(i, Child{})
		p, err := glsMap(m)
		if err != nil {
			panic(err) // Fixed local constants; no external input or mutable context.
		}
		clone.fixed[string(m)] = p
	}
	return &clone
}
func (b *glsBackend) withCache(c *ComputationCache, prefix string) Backend {
	clone := *b
	clone.cache, clone.cachePrefix = c, prefix
	return &clone
}

func glsPtr(raw []byte) *C.uchar {
	if len(raw) == 0 {
		return nil
	}
	return (*C.uchar)(unsafe.Pointer(&raw[0]))
}
func glsMap(m []byte) (*glsPoint, error) {
	p := new(glsPoint)
	if C.incauth_gls_map(glsPtr(p[:]), glsPtr(m), C.size_t(len(m))) != 0 {
		return nil, errors.New("GLS254 hash-to-point failed")
	}
	return p, nil
}
func (b *glsBackend) decode(raw []byte) (*glsPoint, error) {
	if len(raw) != 32 {
		return nil, errors.New("GLS254 accumulator length")
	}
	var key string
	if b.cache.enabled(0) {
		key = b.cachePrefix + "\x00" + string(raw)
		if p, ok := b.cache.getValue(key, 0); ok {
			return p.(*glsPoint), nil
		}
	}
	p := new(glsPoint)
	if C.incauth_gls_decode(glsPtr(p[:]), glsPtr(raw)) != 0 {
		return nil, errors.New("invalid or noncanonical GLS254 accumulator")
	}
	if key != "" {
		b.cache.putValue(key, 0, p)
	}
	return p, nil
}
func (b *glsBackend) Validate(raw []byte) error { _, err := b.decode(raw); return err }
func (b *glsBackend) contribution(m []byte) (*glsPoint, error) {
	if p := b.fixed[string(m)]; p != nil {
		return p, nil
	}
	var key string
	if b.cache.enabled(1) {
		key = b.cachePrefix + "\x01" + string(m)
		if p, ok := b.cache.getValue(key, 1); ok {
			return p.(*glsPoint), nil
		}
	}
	p, err := glsMap(m)
	if err == nil && key != "" {
		b.cache.putValue(key, 1, p)
	}
	return p, err
}
func (b *glsBackend) Apply(previous []byte, remove, add [][]byte) ([]byte, error) {
	var previousPoint []byte
	if previous != nil {
		p, err := b.decode(previous)
		if err != nil {
			return nil, err
		}
		previousPoint = p[:]
	}
	// One C call combines all resolved points, with no C allocations and no
	// compressed-point roundtrips between operations. Both cache kinds use the
	// same 512+key-bytes accounting as R255, including state/storage together.
	points := make([]byte, 0, (len(remove)+len(add))*97)
	for sign, list := range [][][]byte{add, remove} {
		for _, m := range list {
			p, err := b.contribution(m)
			if err != nil {
				return nil, err
			}
			points = append(points, byte(sign))
			points = append(points, p[:]...)
		}
	}
	next := new(glsPoint)
	raw := make([]byte, 32)
	if C.incauth_gls_apply(glsPtr(next[:]), glsPtr(raw), glsPtr(previousPoint), glsPtr(points), C.size_t(len(points))) != 0 {
		return nil, errors.New("GLS254 accumulator update failed")
	}
	if b.cache.enabled(0) {
		b.cache.putValue(b.cachePrefix+"\x00"+string(raw), 0, next)
	}
	return raw, nil
}
