//go:build gls254 && cgo && amd64

// Adapter for jbms/ecmh 3448c2a7eb9e0b030c4e562c3a152c142102c302.
// GF(2^127): z^127+z^63+1; extension u^2+u+1; y^2+xy=x^3+u*x^2+b,
// b=(59C8202CB9E6E0AE2E6D944FA54DE7E5,0). SWChar2 + BLAKE2b,
// unblinded (public trie commitments). The full curve group has cofactor 2;
// infinity and the order-2 point are valid distinct canonical states. This is
// NOT the Ristretto prime-order quotient or a generic GLS254 codec.
#include <vector>
#include <algorithm>
#include <cstring>
#include <type_traits>
#include "jbms/binary_elliptic_curve/GLS254.hpp"
#include "jbms/multiset_hash/ECMH.hpp"
#include "jbms/hash/blake2b.hpp"

using Hash = jbms::multiset_hash::ECMH<jbms::binary_elliptic_curve::GLS254,
                                    jbms::hash::blake2b, false>;
using Point = Hash::State;
static_assert(sizeof(Point) == 96, "GLS254 internal ABI changed");
static_assert(std::is_trivially_copyable<Point>::value, "point must be pointer-free");

static const Hash& hash() { static const Hash h{}; return h; }
static Point readPoint(const unsigned char* p) {
    Point s;
    std::memcpy(&s, p, sizeof(s));
    return s;
}
static void writePoint(unsigned char* p, const Point& s) {
    std::memcpy(p, &s, sizeof(s));
}
static void encode(unsigned char* p, const Point& s) {
    assign(hash(), jbms::little_endian(jbms::array_view<unsigned char>(p, 32)), s);
}

extern "C" int incauth_gls_decode(unsigned char* out, const unsigned char* raw) {
    try {
        Point s{};
        assign(hash(), s, jbms::little_endian(jbms::array_view<const unsigned char>(raw, 32)));
        // Upstream decompression alone does not enforce membership/canonicality.
        if (!is_rational(hash().curve(), s)) return -1;
        unsigned char canonical[32];
        encode(canonical, s);
        if (std::memcmp(canonical, raw, 32) != 0) return -1;
        writePoint(out, s);
        return 0;
    } catch (...) { return -1; }
}
extern "C" int incauth_gls_map(unsigned char* out, const unsigned char* msg, size_t n) {
    try {
        Point s{};
        initialize(hash(), s);
        add(hash(), s, jbms::array_view<const unsigned char>(msg, n));
        writePoint(out, s);
        return 0;
    } catch (...) { return -1; }
}
extern "C" int incauth_gls_apply(unsigned char* out, unsigned char* raw,
                                const unsigned char* previous,
                                const unsigned char* points, size_t size) {
    try {
        if (size % 97 != 0) return -1;
        Point s{};
        if (previous) s = readPoint(previous);
        else initialize(hash(), s);
        for (size_t off = 0; off < size; off += 97) {
            auto p = readPoint(points + off + 1);
            if (points[off] == 0) add_hash(hash(), s, p);
            else if (points[off] == 1) remove_hash(hash(), s, p);
            else return -1;
        }
        encode(raw, s);
        writePoint(out, s);
        return 0;
    } catch (...) { return -1; }
}
