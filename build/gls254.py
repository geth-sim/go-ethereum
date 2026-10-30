#!/usr/bin/env python3
"""Build the optional research GLS254 backend locally (Linux amd64, Python 3.10).

No system packages/services are changed. Sources and build artifacts go under
ignored build/cache/gls254. Then build ./simulator with -tags gls254 and -o.
--seed accepts an existing backend source/download cache;
all archives and git revisions are checked, and libraries are rebuilt from source.
OpenSSL 1.0.2 headers/library are needed by upstream *code generators*, not linked
into the resulting simulator. The adapter only uses the upstream BLAKE2b/ECMH.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / 'build/cache/gls254'
REVISIONS = {
    'ecmh-src': ('jbms/ecmh', '3448c2a7eb9e0b030c4e562c3a152c142102c302'),
    'deps/jbms-utility': ('jbms/jbms-utility', '9557422d318cac9b8e6f9e9b15c4dc7f16052659'),
    'deps/array_view': ('jbms/array_view', '8a6589465033ce52ef81a86752ba813fba1d9847'),
    'deps/jbms-openssl': ('jbms/jbms-openssl', '8a95879974a814f7330e3b6c2d9bb3b67cb9087e'),
}
PACKAGES = [
    ('main/c/cmake', 'cmake-data_3.22.1-1ubuntu1.22.04.2_all.deb', '0009fa5659c702cb654f868a6d1e7ebd4f8d230092df2f086a32aab2fa00b9ba'),
    ('main/c/cmake', 'cmake_3.22.1-1ubuntu1.22.04.2_amd64.deb', '55a46b2f9bbc8899ae6772f0313ca2dd88801a514feaa488c88094c4fafa83c5'),
    ('main/b/boost1.74', 'libboost1.74-dev_1.74.0-14ubuntu3_amd64.deb', '4d9c90e43f0d25db6280d1ee326771cbb76462f73b9430f06bac1de8d05b7a78'),
    ('main/b/boost1.74', 'libboost-program-options1.74-dev_1.74.0-14ubuntu3_amd64.deb', '4200ca1f29978655108bdb00e575ae602d14900df46e590187090d2904b5f679'),
    ('main/b/boost1.74', 'libboost-program-options1.74.0_1.74.0-14ubuntu3_amd64.deb', '8b9611edde085371815615e3529fbbab3ad02965ec24b2cc46e0c1bb60cade5c'),
    ('main/b/boost1.74', 'libboost-test1.74-dev_1.74.0-14ubuntu3_amd64.deb', '575b2e1f91dc75861c712f53b706d278a232efa6600255f78969c075647dbfca'),
    ('main/b/boost1.74', 'libboost-test1.74.0_1.74.0-14ubuntu3_amd64.deb', 'bd64d4d801f1371eeb1e67c9fd06b2babcb006adb4bd4c1ecf466a7cdc2907e3'),
    ('main/libj/libjsoncpp', 'libjsoncpp25_1.9.5-3_amd64.deb', '8844bfab4691b56cf49e741b65389147b03168bef4254b31ae97ad679d199a78'),
    ('main/r/rhash', 'librhash0_1.4.2-1ubuntu1_amd64.deb', '7355f23816c15acd96072a292823f7e33232ae29e794d69e694c7ddf2043ba2f'),
]


def run(args, cwd=OUT, env=None):
    subprocess.run(list(map(str, args)), cwd=cwd, env=env, check=True)


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def download(url, path, digest, seed):
    path.parent.mkdir(parents=True, exist_ok=True)
    if not path.exists():
        if seed and seed.exists():
            shutil.copy2(seed, path)
        else:
            with urllib.request.urlopen(url, timeout=90) as src, path.open('wb') as dst:
                shutil.copyfileobj(src, dst)
    if sha(path) != digest:
        raise RuntimeError(f'archive SHA256 mismatch: {path}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--seed', type=Path)
    args = parser.parse_args()
    if args.seed:
        args.seed = args.seed.resolve()
    OUT.mkdir(parents=True, exist_ok=True)
    for rel, (repo, revision) in REVISIONS.items():
        target = OUT / rel
        if not target.exists():
            source = args.seed / rel if args.seed else None
            run(['git', 'clone', '--no-hardlinks', source if source and source.exists() else f'https://github.com/{repo}.git', target])
            run(['git', 'checkout', '--detach', revision], target)
        actual = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=target, text=True).strip()
        if actual != revision:
            raise RuntimeError(f'wrong source revision: {target}')
    sysroot = OUT / 'sysroot'
    for folder, name, digest in PACKAGES:
        path = OUT / 'downloads' / name
        download(f'https://archive.ubuntu.com/ubuntu/pool/{folder}/{name}', path, digest, args.seed / name if args.seed else None)
        run(['dpkg-deb', '-x', path, sysroot])
    archive = OUT / 'downloads/openssl-1.0.2u.tar.gz'
    download('https://codeload.github.com/openssl/openssl/tar.gz/refs/tags/OpenSSL_1_0_2u', archive,
             '82fa58e3f273c53128c6fe7e3635ec8cda1319a10ce1ad50a987c3df0deeef05',
             args.seed / 'deps/openssl-1.0.2u.tar.gz' if args.seed else None)
    openssl = OUT / 'deps/openssl'
    if not openssl.exists():
        openssl.mkdir()
        run(['tar', '-xzf', archive, '--strip-components=1', '-C', openssl])
    if not (openssl / 'libcrypto.a').exists():
        run(['./config', 'no-shared', '-fPIC'], openssl)
        run(['make', '-j4', 'build_crypto'], openssl)
    src = OUT / 'ecmh-src'
    # Port only Python generator syntax and build discovery; cryptographic
    # source and curve constants stay at the pinned upstream revision.
    if not (OUT / 'python3-converted').exists():
        run(['python3', '-m', 'lib2to3', '-w', '-n', src / 'python'])
        for p in (src / 'python').glob('*.py'):
            p.write_text(p.read_text().replace('python2', 'python3'))
        (OUT / 'python3-converted').touch()
    cmake = subprocess.check_output(['git', 'show', 'HEAD:CMakeLists.txt'], cwd=src, text=True)
    for name, path in [('jbms-utility', OUT/'deps/jbms-utility/src'), ('jbms-array_view', OUT/'deps/array_view/src'),
                       ('jbms-openssl', OUT/'deps/jbms-openssl/src'), ('boost-endian', sysroot/'usr/include')]:
        cmake = cmake.replace(f'find_package({name} REQUIRED)', f'set({name}_INCLUDE_DIRS "{path}")')
    cmake = cmake.replace('-Wall -Werror', '-Wall -include vector -include algorithm').replace('COMMAND python2 ', 'COMMAND python3 ')
    cmake = cmake.replace('include_directories(\n', f'include_directories(BEFORE "{openssl}/include")\ninclude_directories(\n', 1)
    (src/'CMakeLists.txt').write_text(cmake)
    if not (OUT/'pydeps/numpy').is_dir():
        run(['python3', '-m', 'pip', 'install', '--only-binary=:all:', '--no-deps',
             '--target', OUT/'pydeps', 'numpy==1.26.4'])
    env = dict(os.environ, LD_LIBRARY_PATH=str(sysroot/'usr/lib/x86_64-linux-gnu'),
               PYTHONPATH=str(OUT/'pydeps'))
    exe = sysroot/'usr/bin/cmake'
    run([exe, '-S', src, '-B', OUT/'ecmh-build', '-DCMAKE_C_COMPILER=gcc', '-DCMAKE_CXX_COMPILER=g++',
         '-DCMAKE_POSITION_INDEPENDENT_CODE=ON', '-DC_ARCH_FLAGS=-msse4.1;-mpclmul', '-DBoost_NO_BOOST_CMAKE=ON',
         f'-DBOOST_ROOT={sysroot}/usr', f'-DOPENSSL_INCLUDE_DIR={openssl}/include',
         f'-DOPENSSL_LIBRARIES={openssl}/libcrypto.a'], env=env)
    # Upstream deliberately omits this target dependency. Build it first on
    # clean checkouts; its timings choose an inversion chain, not a new codec.
    run([exe, '--build', OUT/'ecmh-build', '--target', 'benchmark_core_GF2_127_63', '--parallel', '4'], env=env)
    empty_benchmark = OUT/'ecmh-build/benchmark_results/benchmark_core_GF2_127_63'
    if empty_benchmark.exists() and empty_benchmark.stat().st_size == 0:
        empty_benchmark.unlink()
    run([exe, '--build', OUT/'ecmh-build', '--target', 'blake2b', 'ec_GLS254', '--parallel', '4'], env=env)
    artifacts = list((OUT/'ecmh-build').glob('*.a')) + list((OUT/'ecmh-build/src').rglob('*.hpp'))
    (OUT/'manifest.json').write_text(json.dumps(dict(revisions=REVISIONS, cxx_flags='-O3 -msse4.1 -mpclmul',
        artifacts={str(p.relative_to(OUT)): sha(p) for p in artifacts},
        compiler=subprocess.check_output(['g++', '--version'], text=True).splitlines()[0]), indent=2)+'\n')
    print('Ready: go build -tags gls254 -o build/bin/simulator-gls254 ./simulator', flush=True)


if __name__ == '__main__':
    main()
