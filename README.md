# State replay simulator

`cmd/statesim` replays signed block RLP through Geth v1.17.10's native
`BlockChain.InsertChain`, with experimental trie keys and authentication.
It preserves transaction execution, sender recovery, fork rules and trie commits.
Use dedicated experiment databases; they are not ordinary Geth datadirs.

## Build and run

```sh
make statesim
build/bin/statesim -blocks /data/mainnet.rlp \
  -until 6000000 -scheme H -db-engine leveldb
```

The default root is `/ethereum/key-simulator-geth-v1.17.10` (`-root` overrides it).
Each run creates `dbs/<timestamp-scheme-engine-auth>/` and
`results/<timestamp-scheme-engine-auth>/`. Results include metrics, configuration
and checkpoints; DB size measures only the DB directory.

For repeated test runs, add `-careful-run`: it removes **only**
`<root>/dbs/careful-run/` before starting, and creates a new results directory.
The DB remains after exit, until the next careful run or manual deletion.
An in-use DB is rejected. This option performs neither TRIM nor OS cache reset.
It cannot be combined with `-resume` or `-db-dir`.

For named locations, set both `-datadir /experiments/results/h-01` and
`-db-dir /experiments/dbs/h-01`. `-datadir` is the result/checkpoint directory;
an explicit `-datadir` alone keeps the legacy `<datadir>/chaindata` layout.
Fresh runs reject existing result directories. To resume, use
`-resume -datadir /experiments/results/h-01`; the DB path is read from `replay.json`.
Results from a replaced careful-run DB remain readable but cannot resume it.

Performance builds use upstream engines pinned in `go.mod`. Detailed LevelDB
measurement requires the instrumentation fork, supplied as a local checkout or
published `module@branch-or-commit`:

```sh
make statesim-measure DB_ENGINE=leveldb DB_SOURCE=/path/to/goleveldb
build/bin/statesim-leveldb-measure -blocks /data/mainnet.rlp \
  -datadir /results/h-detail -until 6000000 -scheme H \
  -db-engine leveldb -measure -db-measure
```

The fork must expose `EnableReadStats`, `ReadStatsVersion() == 3`,
`GetWithReadStats` and `ReadStatsJSON`. The build uses a temporary modfile;
repository dependencies remain unchanged. A remote branch resolves to a concrete
version recorded in result headers; use that version/commit to reproduce a run.
Local checkout revisions and uncommitted edits are **not** recorded automatically.
Keep the same upstream engine revision beneath performance/diagnostic builds.
`DB_ENGINE=pebble` supports the same replacement mechanism, but **detailed Pebble
instrumentation is not implemented**. Unsupported `-db-measure` requests fail.

Optional GLS authentication requires the pinned native backend (Linux amd64,
Python 3.10 and a C++ compiler; dependencies are prepared under `build/cache`):

```sh
python3 build/gls254.py
make statesim STATESIM_TAGS=gls254
```

## Prepare block input

Input is an uncompressed stream of signed Ethereum blocks, not a state dump.
MariaDB is optional. For mainnet 0..10M, Geth can download Era1 history, import
blocks/receipts into a preparation database, and export RLP without executing
transactions or rebuilding state. Choose a server from the
[history endpoints](https://eth-clients.github.io/history-endpoints/).

```sh
make geth
build/bin/geth --mainnet --datadir /data/block-source download-era \
  --server https://mainnet.era1.nimbus.team --block 0-10000000
```

Era1 files cover 8,192-block epochs and downloads are checksum-verified.
This Geth version does not create the manifest needed by `import-history`;
generate it from the built-in checksums for exactly the downloaded files:

```sh
python3 - <<'PYCODE'
from pathlib import Path
folder = Path('/data/block-source/geth/chaindata/ancient/chain/era')
known = {name: digest for digest, name in
         (line.split() for line in Path('internal/era/eradl/checksums_mainnet.txt').read_text().splitlines())}
files = sorted(folder.glob('mainnet-*.era1'))
assert files, 'No Era1 files downloaded'
(folder / 'checksums.txt').write_text(''.join('0x' + known[f.name] + '\n' for f in files))
PYCODE
build/bin/geth --mainnet --datadir /data/block-source import-history --era.format era1 \
  /data/block-source/geth/chaindata/ancient/chain/era

test ! -e /data/mainnet.rlp.partial && test ! -e /data/mainnet.rlp && \
  build/bin/geth --datadir /data/block-source export /data/mainnet.rlp.partial 0 10000000 && \
  mv /data/mainnet.rlp.partial /data/mainnet.rlp
```

Use a fresh preparation DB: `import-history` expects contiguous history from
genesis. Allow space for downloads, preparation DB and RLP. If a stopped Geth DB
already contains all block bodies, only the export step is needed; historical
state is unnecessary. Export includes both endpoints and appends to existing
files when given a range, hence the fresh-file checks above.

The optional SQL adapter requires `pymysql`, `rlp` and `eth_hash`. Keep connection
credentials in an external local module:

```sh
python3 cmd/statesim/export.py --db-utils /private/db_utils.py \
  --last 10000000 --output /data/mainnet.rlp
```

The adapter verifies reconstructed hashes and supports transaction types 0/1/2
through London. It writes `.partial`, syncs and publishes the final name only on
success, without overwriting existing files. Failed exports retain the partial
file; automatic export resume is not supported. `--output -` streams to stdout.

A larger RLP serves any smaller `-until` target. A partial file is usable only if
the target is fully written; replay does not wait at EOF. To extend the input,
export only the new range (`--first` for the SQL adapter) and append its completed
suffix. Preserve the original length to recover an interrupted append. Finish
input preparation before performance measurements.

## Configuration and restart

Use `build/bin/statesim -h` for all flags. Sizes below are MiB.

| Option | Default and meaning |
| --- | --- |
| `-db-engine`, `-compression` | `pebble`, `snappy`; LevelDB/Pebble, Snappy/none, and Zstd for Pebble. Other engine policies stay native. |
| `-archive` | `true` for hashdb: flush each block and retain history. `false` uses native dirty buffering/GC. PBSS supports native full mode only. |
| `-store-chain-data` | `false`: persist trie nodes/code; chain records/root mappings use a bounded 512-block memory window. `true` also persists chain data. |
| `-prefetch`, `-snapshot-cache` | `false`, `0`. A positive snapshot budget enables hashdb snapshots; PBSS always stores flat state. |
| `-cache`, `-db-cache`, `-trie-cache` | Base 4096; DB gets 50%, clean trie gets the remainder after dirty/snapshot budgets. Explicit DB/trie sizes override allocation. |
| `-auth-cache` | Additional authentication cache, separate from Geth/DB caches; default 0. |
| `-drop-os-cache` | `false`; Linux `sync` + `drop_caches=3` before DB open. Requires noninteractive sudo; failure aborts. |
| `-measure`, `-db-measure` | `false`; either enables diagnostic mode, whose timings are not performance results. |
| `-disk-size-interval`, `-db-stats-interval` | 10,000 and 100,000 blocks; 0 disables periodic samples. |
| `-checkpoint-interval` | 10,000 for archive hashdb; non-archive/PBSS checkpoint only on clean stop. |
| `-verify-block-roots`, `-verify-state` | `false`; skip trusted-input tx/uncle/receipt commitment rechecks and final full-state audit. State/auth roots are still calculated. |
| `-skip-intermediate-root` | `true`; hash at block end, including pre-Byzantium blocks. Transaction finalisation remains. |

With snapshots off, archive DB/clean budgets are 2048/2048; non-archive
DB/clean/dirty budgets are 2048/1024/1024. PBSS caps its write buffer at 256.
These are budgets, not RSS limits. Preimages are disabled. State-only mode still
serializes chain records in memory. Snapshots and PBSS history count toward disk size.

For unattended Linux runs, an administrator can use
`sudo visudo -f /etc/sudoers.d/statesim-drop-caches` to grant only the cache-reset command
(replace `USERNAME` with the experiment account):

```sudoers
USERNAME ALL=(root) NOPASSWD: /usr/sbin/sysctl -w vm.drop_caches\=3
```

Each job must include `-drop-os-cache`. The simulator runs `sync` as the user,
then invokes that exact command with `sudo -n`, before opening the DB.
The simulator itself runs unprivileged. A failed sync or denied reset aborts
the run. This resets host OS caches, not SSD caches or internal device state.

Start each scheme from genesis in a fresh datadir. `-resume -until N` continues a
compatible checkpoint; state-only resume scans the input prefix for chain context
before opening the DB. Genesis, codec, auth suite, engine, compression, retention
and storage policy must match. Cache/worker/measurement options may change;
counters and metadata receive a new RunID.

SIGINT/SIGTERM finish the current batch. Archive checkpoints sync state writes
before atomically publishing `checkpoint.json`; resume trims later result rows.
Non-archive/PBSS use native shutdown flush/journaling and reject unclean restart.
State-only DBs need checkpoint metadata because chain context is not persisted.
Use cleanly stopped copies for comparisons; archive crashes can leave later,
unreferenced nodes. Resumed application caches start cold.

Replay supports append-only MPT input before Amsterdam, with versions below 2^32.
PoW seals are skipped; sender recovery, execution, gas/bloom and header/uncle checks
remain. H/PBSS check canonical state roots; experimental roots are separate.
`-verify-state` adds an untimed authentication audit and canonical reconstruction
from persisted leaves. Historical receipt verification requires native
intermediate roots as well as `-verify-block-roots`.

## Sequential experiments

`queue.py` runs jobs sequentially on Linux using the Python standard library.
Copy the binary to a fixed location and create a JSON plan:

```json
{
  "version": 1,
  "lock_file": "/results/statesim.lock",
  "jobs": [{
    "argv": ["/bench/statesim", "-blocks", "/data/mainnet.rlp", "-datadir", "/results/h-pebble", "-db-dir", "/dbs/h-pebble", "-until", "6000000", "-scheme", "H", "-db-engine", "pebble"],
    "binary_sha256": "REPLACE_WITH_SHA256SUM_OF_BINARY",
    "cwd": "/bench",
    "env": {"GOMAXPROCS": "32"},
    "min_free_gib": 1500
  }]
}
```

Each output parent must exist and each job datadir must be new. Add jobs with
explicit options and distinct datadirs; set the free-space reserve for the workload.
Queued careful runs require an explicit `-root` and a unique `-datadir` per job;
they reuse only `<root>/dbs/careful-run`, with no `-db-dir` or `clone_from`.

```sh
python3 cmd/statesim/queue.py /results/queue.json --check
screen -L -Logfile /results/queue.log -dmS statesimQueue \
  python3 -u "$PWD/cmd/statesim/queue.py" /results/queue.json
```

Transitions require exit code zero, a `complete` result at the target and a
matching checkpoint. Early stops/errors stop the queue. A shared lock prevents
concurrent queues and remains held by a child if its runner dies. Binary checksums
prevent silent rebuilds. `queue.state.json` records transitions/failures; screen
survives disconnection, not reboot. Running jobs are never automatically restarted
or overwritten; inspect failures and use a new plan for retries.

For checkpoint comparisons, add `"clone_from": {"datadir": "/results/source",
"checkpoint_sha256": "..."}` and `-resume`. The closed source must be archive,
state-only hashdb on a filesystem supporting `cp --reflink=always`. Clones are
independent and retain the source; exclude warmup and reverse run order in repeats.
An optional `after` object waits for an existing wrapper process: supply
`process` from `queue.identity(pid)`, `exit_file`, `datadir` and `target`.
The wrapper must record its exit code before exiting.

## Schemes and authentication

`common/research.go` defines configuration and encoders. Hashdb schemes retain
32-byte child reference = DB key. VP/PV are VP*/PV* (`JMT_fixed`/`PrefixTree_fixed`).
Other presets include Outward/OutwardStorage, OutwardSplit, VPRight, EpochPath,
TPV, SplitPVHot, DepthSplit, DepthEpoch, ShardVP, DualVP, RunPath, ForestVP,
ATileVP, JMT, PrefixTree, their balanced variants, HalfPath and TH.
PBSS uses native pathdb and Keccak references/proofs. Custom encoders use
`ReplayConfig.EncodeKey` with a unique versioned codec ID and thread-safe code.

`-auth none` selects plain keys (H retains Keccak); `keccak` computes full-node
myHash-style commitments. `ristretto255`, `p256`, `gls254` use incremental curves.
`-auth-mode auto` selects full for Keccak and incremental for curves.
`-auth-workers` accepts 1..16; curve cache modes are unified/accumulator/contribution.
Keccak caches child commitments. `trie/authentication.go` integrates native tries
with `trie/incauth`; its reference trie is for tests. Experimental proofs use
`AuthenticationProof`/`incauth.Verify`, separately from Ethereum consensus roots.

## Results and analysis

- `simblocks.jsonl`: block/batch timings, roots, transactions, disk/cache samples
  and Geth diagnostics. Disabled detailed counters are zero, not observations.
- `leveldb_metrics.jsonl` or `pebble_metrics.jsonl`: native counters and optional
  `Detailed` DB statistics. Sampling does not flush the DB.
- `disk_io.jsonl`: Linux device I/O counters, sampled every second by default
  in both performance and detailed modes (`-disk-stats-interval=0` disables).
- `replay.json`, `checkpoint.json`: configuration and durable restart metadata.

Metric files contain matching run headers with effective options, cache budgets,
build/dependency versions and performance/diagnostic mode. `Flags` includes all
CLI defaults after resolving automatic values; `RuntimeEnvironment` records
`GOGC`, `GOMEMLIMIT`, `GODEBUG` and `GOMAXPROCS` (empty means runtime defaults).
`make statesim` and `make statesim-measure` stamp `SourceRevision` with the full
Git commit and a `-dirty` suffix for local edits, including in Git worktrees.
Use a clean committed build for comparisons; direct Go builds may report
`unknown` when automatic VCS stamping is unavailable.
The comparison script also checks these settings, block pacing, OS cache reset
and device sampling options before accepting a comparison. Counter differences
must stay within one RunID. Run headers and `simblocks.jsonl` disk samples also
record `Filesystems.home` and `Filesystems.database`: paths, filesystem types,
total/used/available bytes and usage percentages for the filesystems containing
the user's home and DB. These are filesystem totals, not directory sizes;
`free` means user-available space and `usedPercent` is `100*used/(used+free)`.
Disk samples include UTC timestamps, including at clean stop/completion.
Device I/O samples have UTC time, monotonic `ElapsedNS`, last imported block
`Number`, and compact counter arrays whose names/units are in `diskstats-schema`.
Sector size is 512 bytes. For adjacent samples within a RunID, divide read/write
milliseconds by completed I/Os for average latency, counts/bytes by elapsed time
for IOPS/throughput, and weighted I/O milliseconds by elapsed milliseconds for
average outstanding I/Os. Skip counter resets and zero-I/O latency intervals;
older kernels omit unsupported trailing fields. These are whole-device statistics,
including other processes and compaction, without per-request latency percentiles.
RAID/dm and member devices are separate views; do not sum them together. I/O busy
time is approximate and is not an NVMe capacity percentage.
The sampler retains no history in memory, uses a 256KiB output buffer, flushes
the buffer every 10 seconds without fsync, and writes start/end samples. It runs
independently of block processing, ends before optional verification/DB shutdown,
and propagates read/write errors. Resumes append a new RunID; earlier I/O samples
are retained, including any work beyond the restored checkpoint.
Progress redraws at most 10Hz; summaries every 1,000
blocks show total/recent speed. Remaining time uses a rolling 10,000-block window.

```sh
python3 cmd/statesim/compare.py --interval after --start 5000000 --end 6000000 \
  --window 100000 --compare H=/results/h VP=/results/vp Outward=/results/outward \
  --out /results/comparison
```

`after` means `(start,end]`; default `legacy` means `[start,end)`. Disk size is after
`end`; missing values are NA. The first input is the reference. `--vary-auth`,
`--vary-engine`, `--vary-compression`, `--vary-retention` permit the named differences;
other mismatched conditions and restart-spanning intervals are rejected.
Outputs include summary/window/engine/read-path/operation CSVs, summary JSON and
paper metrics with coverage. `--plot FILE` optionally uses matplotlib.

| Metric | Meaning |
| --- | --- |
| `block_ms` | ProcessBlock time including execution/auth/commit; excludes RLP decode, output and periodic sampling/checkpoints. |
| `import_ms` | InsertChain batch wall time; reported only for complete-batch intervals. |
| `wall_ms` | Replay wall time including input/output/checkpoints, excluding setup/final audit; requires exact progress boundaries. |
| Read/write/hash/auth | Potentially overlapping component timers, not an additive wall-time partition or pure SSD latency. Auth root-pass time excludes update-time recovery. |
| `codec_work_ms`, `no_hash_ms` | Detailed diagnostics; hash-subtracted time additionally requires serial hashing and `auth=none`. |
| `speedup` | Reference/current time for the selected metric. Paper output uses block time. |
| `disk_gib` | Logical chaindata file sizes including WAL/manifest/PBSS history; excludes input/results. |

`-measure` serializes trie updates/hashing/auth and records lookup source/time/bytes
and child histograms. Lookup latency includes earlier cache probes; disk lookups
may hit OS cache. Histograms count encoding visits, not unique nodes. Payment/call
classification uses receipt gas == 21000; transaction subtotals and snapshot timers
can overlap other timers. PBSS HistorySize is included in disk size.

LevelDB detailed schema v3 separates Get, Has, iterator, compaction and other work.
It counts each SST probe, including overlapping L0 tables, and cache misses even
with DontFillCache. `ByKind` splits Get into trie/code/other; `ByOperation` sums to
`AllOperations`. Bloom false positives mean positive probes without a visible key.
SST read bytes include trailers but exclude WAL/footer/manifest and can hit OS cache.
Counters are process-wide; use one experimental DB per process. Legacy aggregates
cannot be corrected retrospectively. Table 3 uses endpoint cumulative counters;
other performance/read comparisons use interval deltas. LevelDB compaction includes
flush work, whereas Pebble compaction duration excludes it. Pebble's aggregate cache
rate cannot substitute for trie-data-block hit rate. SST entry counts, rewrite
compression, padding and version-wrap results require separate experiments.

Diagnostic helpers: `-block-delay 20ms` adds waits excluded from block time but
included in import/wall time; `pacing` rows record them. `-cpuprofile FILE` profiles
replay and marks results diagnostic. `-pprof-port N` exposes localhost-only pprof
(default off); requested profiles affect timing. `-drop-os-cache` affects the whole
host and records preparation time outside replay. It resets OS caches, not SSD or
compaction state; use identical settings on an idle host for comparisons.

---

## Go Ethereum


Golang execution layer implementation of the Ethereum protocol.

[![API Reference](
https://pkg.go.dev/badge/github.com/ethereum/go-ethereum
)](https://pkg.go.dev/github.com/ethereum/go-ethereum?tab=doc)
[![Go Report Card](https://goreportcard.com/badge/github.com/ethereum/go-ethereum)](https://goreportcard.com/report/github.com/ethereum/go-ethereum)
[![Travis](https://app.travis-ci.com/ethereum/go-ethereum.svg?branch=master)](https://app.travis-ci.com/github/ethereum/go-ethereum)
[![Discord](https://img.shields.io/badge/discord-join%20chat-blue.svg)](https://discord.gg/nthXNEv)
[![Twitter](https://img.shields.io/twitter/follow/go_ethereum)](https://x.com/go_ethereum)

Automated builds are available for stable releases and the unstable master branch. Binary
archives are published at https://geth.ethereum.org/downloads/.

## Building the source

For prerequisites and detailed build instructions please read the [Installation Instructions](https://geth.ethereum.org/docs/getting-started/installing-geth).

Building `geth` requires both a Go (version 1.25 or later) and a C compiler. You can install
them using your favourite package manager. Once the dependencies are installed, run

```shell
make geth
```

or, to build the full suite of utilities:

```shell
make all
```

## Executables

The go-ethereum project comes with several wrappers/executables found in the `cmd`
directory.

|  Command   | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| :--------: | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **`geth`** | Our main Ethereum CLI client. It is the entry point into the Ethereum network (main-, test- or private net), capable of running as a full node (default) or archive node (retaining all historical state). It can be used by other processes as a gateway into the Ethereum network via JSON RPC endpoints exposed on top of HTTP, WebSocket and/or IPC transports. `geth --help` and the [CLI page](https://geth.ethereum.org/docs/fundamentals/command-line-options) for command line options. |
|  `devp2p`  | Utilities to interact with nodes on the networking layer, without running a full blockchain.                                                                                                                                                                                                                                                                                                                                                                                                                                       |
|  `abigen`  | Source code generator to convert Ethereum contract definitions into easy-to-use, compile-time type-safe Go packages. It operates on plain [Ethereum contract ABIs](https://docs.soliditylang.org/en/develop/abi-spec.html) with expanded functionality if the contract bytecode is also available. It accepts contract ABIs via `--abi` (optionally with bytecode) or compiler `--combined-json` output. Please see our [Native DApps](https://geth.ethereum.org/docs/developers/dapp-developer/native-bindings) page for details.                                  |
|   `evm`    | Developer utility version of the EVM (Ethereum Virtual Machine) that is capable of running bytecode snippets within a configurable environment and execution mode. Its purpose is to allow isolated, fine-grained debugging of EVM opcodes (e.g. `evm run --trace 60ff60ff`).                                                                                                                                                                                                                                               |
| `rlpdump`  | Developer utility tool to convert binary RLP ([Recursive Length Prefix](https://ethereum.org/en/developers/docs/data-structures-and-encoding/rlp)) dumps (data encoding used by the Ethereum protocol both network as well as consensus wise) to user-friendlier hierarchical representation (e.g. `rlpdump --hex CE0183FFFFFFC4C304050583616263`).                                                                                                                                                                                |

## Running `geth`

Going through all the possible command line flags is out of scope here (please consult our
[CLI Wiki page](https://geth.ethereum.org/docs/fundamentals/command-line-options)),
but we've enumerated a few common parameter combos to get you up to speed quickly
on how you can run your own `geth` instance.

### Hardware Requirements

Minimum:

* CPU with 4+ cores
* 8GB RAM
* High-performance SSD with at least 2TB of free space
* 8 MBit/sec download Internet service

Recommended:

* Fast CPU with 8+ cores
* 16GB+ RAM
* High-performance NVMe SSD with 2TB-4TB of space for long term growth and maintenance headroom
* 25+ MBit/sec download Internet service

### Full node on the main Ethereum network

By far the most common scenario is people wanting to simply interact with the Ethereum
network: create accounts; transfer funds; deploy and interact with contracts. For this
particular use case, the user doesn't care about years-old historical data, so we can
sync quickly to the current state of the network. To do so:

```shell
$ geth console
```

This command will:
 * Start `geth` in snap sync mode (default, can be changed with the `--syncmode` flag),
   causing it to download more data in exchange for avoiding processing the entire history
   of the Ethereum network, which is very CPU intensive.
 * Start the built-in interactive [JavaScript console](https://geth.ethereum.org/docs/interacting-with-geth/javascript-console),
   (via the trailing `console` subcommand) through which you can interact using [`web3` methods](https://github.com/ChainSafe/web3.js/blob/0.20.7/DOCUMENTATION.md) 
   (note: the `web3` version bundled within `geth` is very old, and not up to date with official docs),
   as well as `geth`'s own [management APIs](https://geth.ethereum.org/docs/interacting-with-geth/rpc).
   This tool is optional and if you leave it out you can always attach it to an already running
   `geth` instance with `geth attach`.

### A Full node on the Sepolia test network

Transitioning towards developers, if you'd like to play around with creating Ethereum
contracts, you almost certainly would like to do that without any real money involved until
you get the hang of the entire system. In other words, instead of attaching to the main
network, you want to join the **test** network with your node, which is fully equivalent to
the main network, but with play-Ether only.

```shell
$ geth --sepolia console
```

The `console` subcommand has the same meaning as above and is equally
useful on the testnet too.

Specifying the `--sepolia` flag, however, will reconfigure your `geth` instance a bit:

 * Instead of connecting to the main Ethereum network, the client will connect to the Sepolia
   test network, which uses different P2P bootnodes, different network IDs and genesis
   states.
 * Instead of using the default data directory (`~/.ethereum` on Linux for example), `geth`
   will nest itself one level deeper into a `sepolia` subfolder (`~/.ethereum/sepolia` on
   Linux). Note, on OSX and Linux this also means that attaching to a running testnet node
   requires the use of a custom endpoint since `geth attach` will try to attach to a
   production node endpoint by default, e.g.,
   `geth attach <datadir>/sepolia/geth.ipc`. Windows users are not affected by
   this.

*Note: Although some internal protective measures prevent transactions from
crossing over between the main network and test network, you should always
use separate accounts for play and real money. Unless you manually move
accounts, `geth` will by default correctly separate the two networks and will not make any
accounts available between them.*

### Configuration

As an alternative to passing the numerous flags to the `geth` binary, you can also pass a
configuration file via:

```shell
$ geth --config /path/to/your_config.toml
```

To get an idea of how the file should look like you can use the `dumpconfig` subcommand to
export your existing configuration:

```shell
$ geth --your-favourite-flags dumpconfig
```

#### Docker quick start

One of the quickest ways to get Ethereum up and running on your machine is by using
Docker:

```shell
docker run -d --name ethereum-node -v /Users/alice/ethereum:/root \
           -p 8545:8545 -p 30303:30303 \
           ethereum/client-go
```

This will start `geth` in snap-sync mode with a DB memory allowance of 1GB, as the
above command does.  It will also create a persistent volume in your home directory for
saving your blockchain as well as map the default ports. There is also an `alpine` tag
available for a slim version of the image.

Do not forget `--http.addr 0.0.0.0`, if you want to access RPC from other containers
and/or hosts. By default, `geth` binds to the local interface and RPC endpoints are not
accessible from the outside.

### Programmatically interfacing `geth` nodes

As a developer, sooner rather than later you'll want to start interacting with `geth` and the
Ethereum network via your own programs and not manually through the console. To aid
this, `geth` has built-in support for a JSON-RPC based APIs ([standard APIs](https://ethereum.org/en/developers/docs/apis/json-rpc/)
and [`geth` specific APIs](https://geth.ethereum.org/docs/interacting-with-geth/rpc)).
These can be exposed via HTTP, WebSockets and IPC (UNIX sockets on UNIX based
platforms, and named pipes on Windows).

The IPC interface is enabled by default and exposes all the APIs supported by `geth`,
whereas the HTTP and WS interfaces need to manually be enabled and only expose a
subset of APIs due to security reasons. These can be turned on/off and configured as
you'd expect.

HTTP based JSON-RPC API options:

  * `--http` Enable the HTTP-RPC server
  * `--http.addr` HTTP-RPC server listening interface (default: `localhost`)
  * `--http.port` HTTP-RPC server listening port (default: `8545`)
  * `--http.api` API's offered over the HTTP-RPC interface (default: `eth,net,web3`)
  * `--http.corsdomain` Comma separated list of domains from which to accept cross-origin requests (browser enforced)
  * `--ws` Enable the WS-RPC server
  * `--ws.addr` WS-RPC server listening interface (default: `localhost`)
  * `--ws.port` WS-RPC server listening port (default: `8546`)
  * `--ws.api` API's offered over the WS-RPC interface (default: `eth,net,web3`)
  * `--ws.origins` Origins from which to accept WebSocket requests
  * `--ipcdisable` Disable the IPC-RPC server
  * `--ipcpath` Filename for IPC socket/pipe within the datadir (explicit paths escape it)

You'll need to use your own programming environments' capabilities (libraries, tools, etc) to
connect via HTTP, WS or IPC to a `geth` node configured with the above flags and you'll
need to speak [JSON-RPC](https://www.jsonrpc.org/specification) on all transports. You
can reuse the same connection for multiple requests!

**Note: Please understand the security implications of opening up an HTTP/WS based
transport before doing so! Hackers on the internet are actively trying to subvert
Ethereum nodes with exposed APIs! Further, all browser tabs can access locally
running web servers, so malicious web pages could try to subvert locally available
APIs!**

### Operating a private network

Maintaining your own private network is more involved as a lot of configurations taken for
granted in the official networks need to be manually set up.

Unfortunately since [the Merge](https://ethereum.org/en/roadmap/merge/) it is no longer possible
to easily set up a network of geth nodes without also setting up a corresponding beacon chain.

There are three different solutions depending on your use case:

  * If you are looking for a simple way to test smart contracts from go in your CI, you can use the [Simulated Backend](https://geth.ethereum.org/docs/developers/dapp-developer/native-bindings#blockchain-simulator).
  * If you want a convenient single node environment for testing, you can use our [Dev Mode](https://geth.ethereum.org/docs/developers/dapp-developer/dev-mode).
  * If you are looking for a multiple node test network, you can set one up quite easily with [Kurtosis](https://geth.ethereum.org/docs/fundamentals/kurtosis).

## Contribution

Thank you for considering helping out with the source code! We welcome contributions
from anyone on the internet, and are grateful for even the smallest of fixes!

If you'd like to contribute to go-ethereum, please fork, fix, commit and send a pull request
for the maintainers to review and merge into the main code base. If you wish to submit
more complex changes though, please check up with the core devs first on [our Discord Server](https://discord.gg/invite/nthXNEv)
to ensure those changes are in line with the general philosophy of the project and/or get
some early feedback which can make both your efforts much lighter as well as our review
and merge procedures quick and simple.

Please make sure your contributions adhere to our coding guidelines:

 * Code must adhere to the official Go [formatting](https://golang.org/doc/effective_go.html#formatting)
   guidelines (i.e. uses [gofmt](https://golang.org/cmd/gofmt/)).
 * Code must be documented adhering to the official Go [commentary](https://golang.org/doc/effective_go.html#commentary)
   guidelines.
 * Pull requests need to be based on and opened against the `master` branch.
 * Commit messages should be prefixed with the package(s) they modify.
   * E.g. "eth, rpc: make trace configs optional"

Please see the [Developers' Guide](https://geth.ethereum.org/docs/developers/geth-developer/dev-guide)
for more details on configuring your environment, managing project dependencies, and
testing procedures.

### Contributing to geth.ethereum.org

For contributions to the [go-ethereum website](https://geth.ethereum.org), please checkout and raise pull requests against the `website` branch.
For more detailed instructions please see the `website` branch [README](https://github.com/ethereum/go-ethereum/tree/website#readme) or the 
[contributing](https://geth.ethereum.org/docs/developers/geth-developer/contributing) page of the website.

## License

The go-ethereum library (i.e. all code outside of the `cmd` directory) is licensed under the
[GNU Lesser General Public License v3.0](https://www.gnu.org/licenses/lgpl-3.0.en.html),
also included in our repository in the `COPYING.LESSER` file.

The go-ethereum binaries (i.e. all code inside of the `cmd` directory) are licensed under the
[GNU General Public License v3.0](https://www.gnu.org/licenses/gpl-3.0.en.html), also
included in our repository in the `COPYING` file.
