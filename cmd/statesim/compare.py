#!/usr/bin/env python3
"""Compare replay JSONL or legacy simBlocks with explicit timing scopes.

Intervals use the legacy [start,end) convention; disk size is sampled at end.
Use --interval after for (start,end] and exact engine-counter boundaries.
Based on geth-sim/data-analysis/analyze_evm_simulation_results.py.
Never subtract sums of parallel hashing work from elapsed block time.
"""
import argparse
import hashlib
import json
import re
from pathlib import Path
import sys

class _JSONStream:
    """Read one JSON value at a time so a 10M-block file fits in memory."""

    def __init__(self, source):
        self.source, self.buffer, self.pos, self.eof = source, "", 0, False
        self.decoder = json.JSONDecoder()

    def fill(self):
        self.buffer = self.buffer[self.pos:]
        self.pos = 0
        chunk = self.source.read(1024 * 1024)
        self.buffer += chunk
        self.eof = not chunk

    def peek(self):
        while True:
            while self.pos < len(self.buffer) and self.buffer[self.pos].isspace():
                self.pos += 1
            if self.pos < len(self.buffer):
                return self.buffer[self.pos]
            if self.eof:
                return ""
            self.fill()

    def expect(self, char):
        if self.peek() != char:
            raise ValueError(f"expected {char!r} in SimBlocks JSON")
        self.pos += 1

    def value(self):
        self.peek()
        while True:
            try:
                value, end = self.decoder.raw_decode(self.buffer, self.pos)
                # A number may end at a chunk boundary before its final digit.
                if end == len(self.buffer) and not self.eof:
                    self.fill()
                    continue
                self.pos = end
                return value
            except json.JSONDecodeError:
                if self.eof:
                    raise
                if len(self.buffer) - self.pos > 64 * 1024 * 1024:
                    raise ValueError("single JSON value exceeds 64 MiB")
                self.fill()

    def keys(self):
        self.expect("{")
        if self.peek() != "}":
            while True:
                key = self.value()
                if not isinstance(key, str):
                    raise ValueError("JSON object key is not a string")
                self.expect(":")
                yield key
                if self.peek() == "}":
                    break
                self.expect(",")
        self.expect("}")


def iter_simblocks(path, metadata):
    """Stream legacy flat maps and versioned envelopes, including compact JSON."""
    with Path(path).open(encoding="utf-8") as source:
        reader = _JSONStream(source)
        for key in reader.keys():
            if key == "blocks":
                for number in reader.keys():
                    yield int(number), reader.value()
            elif key.isdigit():
                yield int(key), reader.value()
            else:
                if key in metadata:
                    raise ValueError(f"duplicate metadata key {key}")
                metadata[key] = reader.value()
        if reader.peek():
            raise ValueError("trailing content after SimBlocks JSON")


# Key names are explicit: component work can overlap and is not a partition of wall time.
FIELDS = ('payment_ms', 'call_ms', 'payment_ms_per_tx', 'call_ms_per_tx',
          'snapshot_account_read_ms', 'snapshot_storage_read_ms', 'snapshot_commit_ms',
          'HistorySize', 'StateHistorySize', 'TrienodeHistorySize', 'block_ms', 'import_ms', 'read_ms', 'write_ms', 'account_read_ms',
          'storage_read_ms', 'code_read_ms', 'read_with_code_ms', 'hash_work_ms', 'auth_work_ms',
          'codec_work_ms', 'disk_commit_ms', 'no_hash_ms', 'disk_gib', 'tx_per_s', 'mgas_per_s',
          'wall_seconds', 'wall_ms', 'utc_start', 'utc_end', 'wall_speedup', 'timing_metric', 'speedup', 'block_speedup', 'no_hash_speedup', 'read_speedup', 'write_speedup', 'import_speedup', 'storage_reduction_pct',
          'disk_bytes', 'disk_gb', 'disk_tb', 'disk_tib', 'payment_txs', 'call_txs',
          'child_nil_pct', 'child_dirty_pct', 'child_clean_pct', 'full_node_visits',
          'trie_lookup_count', 'trie_clean_hit_pct', 'trie_db_get_successes', 'auth_node_reads')


def checked(value, name):
    if type(value) is not int or value < 0:
        raise ValueError(f'invalid nonnegative integer {name}: {value!r}')
    return value


def make_bucket(start, end, interval):
    lo, hi = (start + 1, end + 1) if interval == 'after' else (start, end)
    return dict(start=start, end=end, first=lo, last=hi-1, blocks=0, sums={},
                disk_gib=None, disk_block=None, import_ns=0, import_blocks=0,
                counts={}, children=[0]*17, diagnostics_available=None,
                workload=hashlib.sha256(), block_hashes=hashlib.sha256())


def add_block(bucket, number, values, block):
    expected = bucket['first'] + bucket['blocks']
    if number != expected:
        raise ValueError(f'missing/duplicate/out-of-order block {number}; expected {expected}')
    bucket['blocks'] += 1
    for name, value in values.items():
        bucket['sums'][name] = bucket['sums'].get(name, 0) + checked(value, name)
    txs = checked(block['PaymentTxLen'], 'PaymentTxLen') + checked(block['CallTxLen'], 'CallTxLen')
    gas = checked(block['GasUsed'], 'GasUsed')
    for kind, field in [('payment_txs', 'PaymentTxLen'), ('call_txs', 'CallTxLen')]:
        bucket['sums'][kind] = bucket['sums'].get(kind, 0) + block[field]
    bucket['sums']['txs'] = bucket['sums'].get('txs', 0) + txs
    bucket['sums']['gas'] = bucket['sums'].get('gas', 0) + gas
    bucket['workload'].update(f"{number}:{block['PaymentTxLen']}:{block['CallTxLen']}:{gas}\n".encode())
    if 'BlockHash' in block:
        bucket['block_hashes'].update(f"{number}:{block['BlockHash']}\n".encode())


def finish_bucket(bucket, serial, auth_enabled, native):
    count = bucket['last'] - bucket['first'] + 1
    if bucket['blocks'] != count:
        raise ValueError(f"incomplete interval: expected {count}, got {bucket['blocks']} blocks")
    sums = bucket.pop('sums')
    for name, value in sums.items():
        if name.endswith('_ms'):
            bucket[name] = value / count / 1e6
    bucket['total_seconds'] = sums['block_ms'] / 1e9
    bucket['txs'], bucket['gas'] = sums['txs'], sums['gas']
    bucket['payment_txs'], bucket['call_txs'] = sums['payment_txs'], sums['call_txs']
    seconds = bucket['total_seconds']
    bucket['tx_per_s'] = sums['txs'] / seconds if seconds else None
    bucket['mgas_per_s'] = sums['gas'] / 1e6 / seconds if seconds else None
    bucket['import_ms'] = bucket.pop('import_ns') / count / 1e6 if bucket['import_blocks'] == count else None
    bucket.pop('import_blocks')
    bucket['codec_work_ms'] = bucket.get('codec_work_ms') if serial else None
    bucket['no_hash_ms'] = None
    if native and serial and not auth_enabled:
        remaining = sums['block_ms'] - sums['hash_work_ms']
        if remaining < 0:
            raise ValueError('hash work exceeds block wall time; refuse subtraction')
        bucket['no_hash_ms'] = remaining / count / 1e6
    bucket['workload_sha256'] = bucket.pop('workload').hexdigest()
    block_hashes = bucket.pop('block_hashes')
    bucket['block_hash_sha256'] = block_hashes.hexdigest() if native else None
    bucket.update(storage_units(round(bucket['disk_gib']*2**30) if bucket['disk_gib'] is not None else None))
    counts = bucket.pop('counts')
    bucket['auth_node_reads'] = counts.get('AuthNodeReads')
    bucket['written_nodes'] = counts.get('WrittenNodes')
    histogram = bucket.pop('children')
    bucket['modified_children_histogram'] = histogram if bucket['diagnostics_available'] else None
    for name in ('child_nil_pct', 'child_dirty_pct', 'child_clean_pct', 'full_node_visits', 'trie_lookup_count', 'trie_clean_hit_pct', 'trie_db_get_successes'):
        bucket[name] = None
    if bucket['diagnostics_available']:
        slots = sum(counts[n] for n in CHILD_FIELDS[1:])
        if slots != 16*counts['FullNodes'] or sum(bucket['modified_children_histogram']) != counts['FullNodes']:
            raise ValueError('child counters/histogram do not cover the encoded branches')
        bucket['full_node_visits'] = counts['FullNodes']
        for kind, name in [('nil', 'NilChildren'), ('dirty', 'DirtyChildren'), ('clean', 'CleanChildren')]:
            bucket['child_'+kind+'_pct'] = ratio(counts[name], slots, 100)
        lookups = sum(counts[n] for n in TRIE_FIELDS)
        bucket.update(trie_lookup_count=lookups, trie_clean_hit_pct=ratio(counts['CleanHits'], lookups, 100), trie_db_get_successes=counts['DiskHits'])
    if 'node_Clean_Count' in counts:
        reads = {}
        for location in ('Clean', 'Dirty', 'Disk', 'Diff', 'Missing'):
            stats = {name: counts['node_'+location+'_'+name] for name in ('Count', 'Bytes', 'TimeNS', 'LayerDepthSum')}
            stats['average_us'] = ratio(stats['TimeNS'], stats['Count'], 0.001)
            stats['average_layer_depth'] = ratio(stats['LayerDepthSum'], stats['Count'])
            reads[location] = stats
        bucket['trie_reads'] = reads
        bucket['trie_lookup_count'] = sum(v['Count'] for v in reads.values())
        bucket['trie_clean_hit_pct'] = ratio(reads['Clean']['Count'], bucket['trie_lookup_count'], 100)
    for kind in ('payment', 'call'):
        if kind+'_ms' in bucket:
            bucket[kind+'_ms_per_tx'] = ratio(sums[kind+'_ms'], sums[kind+'_txs'], 1e-6)
    return bucket


# Supplemental paper metrics use recorded counters only, never timing/size proxies.
CHILD_FIELDS = ('FullNodes', 'NilChildren', 'CleanChildren', 'DirtyChildren')
TRIE_FIELDS = ('CleanHits', 'DirtyHits', 'DiskHits', 'MissingNodes')


def add_diagnostics(bucket, research, measured):
    available = measured and all(k in research for k in CHILD_FIELDS + TRIE_FIELDS + ('ModifiedChildren',))
    bucket['diagnostics_available'] = available if bucket['diagnostics_available'] is None else bucket['diagnostics_available'] and available
    names = ('AuthNodeReads', 'WrittenNodes') + (CHILD_FIELDS + TRIE_FIELDS if available else ())
    for name in names:
        if name in research:
            bucket['counts'][name] = bucket['counts'].get(name, 0) + checked(research[name], name)
    if measured and 'NodeReads' in research:
        for location, values in research['NodeReads'].items():
            for name, value in values.items():
                key = 'node_' + location + '_' + name
                bucket['counts'][key] = bucket['counts'].get(key, 0) + checked(value, key)
    if available and 'ModifiedChildren' in research:
        histogram = research['ModifiedChildren']
        if len(histogram) != 17:
            raise ValueError('expected a 17-bin modified-child histogram')
        for i, count in enumerate(histogram):
            bucket['children'][i] += checked(count, 'ModifiedChildren')


def ratio(numerator, denominator, scale=1):
    return scale*numerator/denominator if numerator is not None and denominator else None


def storage_units(value):
    return {f'disk_{unit}': (value if divisor == 1 else value/divisor) if value is not None else None
            for unit, divisor in [('bytes', 1), ('gb', 10**9), ('tb', 10**12), ('gib', 2**30), ('tib', 2**40)]}


def cache_snapshot(stats, head, starting_head):
    out = dict(head=head, starting_head=starting_head,
               scope='process/cache lifetime, including setup; not a selected-interval delta',
               mode=stats['Mode'], budget_mib=stats['BudgetMiB'], raw=stats)
    myhash = stats.get('MyHash')
    out['myhash_hit_pct'] = ratio(myhash['GetCalls']-myhash['Misses'], myhash['GetCalls'], 100) if myhash else None
    comp = stats.get('Computation', {})
    for name in ('accumulator', 'contribution'):
        h, m = comp.get(name+'_hits'), comp.get(name+'_misses')
        out[name+'_hit_pct'] = ratio(h, h+m, 100) if h is not None and m is not None else None
    return out


def relative_metrics(rows, metric='block'):
    reference = rows[0]
    for row in rows:
        for cur, base in [(row, reference)] + list(zip(row['windows'], reference['windows'])):
            cur['reference'] = reference['label']
            for target, source in [('block_speedup', 'block_ms'), ('no_hash_speedup', 'no_hash_ms'), ('read_speedup', 'read_ms'), ('write_speedup', 'write_ms'), ('import_speedup', 'import_ms'), ('wall_speedup', 'wall_ms')]:
                cur[target] = ratio(base.get(source), cur.get(source))
            cur['timing_metric'] = metric
            cur['speedup'] = cur['block_speedup' if metric == 'block' else 'no_hash_speedup']
            cur['storage_ratio'] = ratio(cur.get('disk_gib'), base.get('disk_gib'))
            cur['storage_reduction_pct'] = 100*(1-cur['storage_ratio']) if cur['storage_ratio'] is not None else None
            gets = lambda r: r.get('engine', {}).get('read_stats', {}).get('db_gets')
            cur['db_gets_vs_reference'] = ratio(gets(cur), gets(base))


def paper_metrics(row):
    # Table 3 is cumulative at the endpoint, unlike the 5M..10M performance interval.
    cumulative = row['engine'].get('cumulative', {})
    out = {k: row.get(k) for k in ('label', 'reference', 'start', 'end', 'block_ms', 'speedup', 'read_ms', 'read_speedup', 'code_read_ms', 'read_with_code_ms', 'write_ms', 'write_speedup', 'disk_bytes', 'disk_gb', 'disk_tb', 'disk_gib', 'disk_tib', 'child_nil_pct', 'child_dirty_pct', 'child_clean_pct')}
    # Paper output pairs speedup with block_ms, independent of the CLI selection.
    out['speedup'] = row['block_speedup']
    out['timing_metric'] = 'block'
    performance = row['profile'].get('PerformanceMode') if row['format'] == 'native' else None
    out['measurement_mode'] = 'performance' if performance is True else 'diagnostic' if performance is False else 'unknown'
    out['read_time_definition'] = 'account + storage read work; excludes code reads and authentication/hash work'
    out['table3_performance_run'] = performance
    for name in ('compaction_count', 'compaction_move_count', 'compaction_seconds', 'compaction_read_bytes', 'compaction_write_bytes', 'flush_count', 'sst_count_end', 'sst_bytes_end', 'write_stall_count', 'write_stall_seconds'):
        out[name] = cumulative.get(name)
    out['compaction_scope'] = cumulative.get('scope')
    out['compaction_time_definition'] = cumulative.get('time_definition')
    out['l0_compactions_excluding_moves'] = cumulative.get('l0_compaction_count') if cumulative.get('engine') == 'leveldb' else None
    out['non_l0_compactions_excluding_moves'] = cumulative.get('non_l0_compaction_count') if cumulative.get('engine') == 'leveldb' else None
    read = row['engine'].get('read_stats', {})
    out['read_stats_version'] = read.get('version')
    out['negative_sst_lookups_per_read'] = read.get('negative_sst_lookups_per_read')
    out['data_block_cache_hit_pct'] = read.get('data_block_cache_hit_pct')
    trie_read = read.get('by_kind', {}).get('trie', {})
    out['trie_data_block_cache_hit_pct'] = trie_read.get('data_block_cache_hit_pct')
    out['trie_negative_sst_lookups_per_read'] = trie_read.get('negative_sst_lookups_per_read')
    out['table3_sst_size_gb'] = ratio(cumulative.get('sst_bytes_end'), 10**9)
    out['read_scope'] = read.get('scope')
    out['db_gets_vs_reference'] = row.get('db_gets_vs_reference')
    for kind in ('myhash', 'accumulator', 'contribution'):
        out[kind+'_cache_hit_pct'] = (row.get('auth_cache_interval') or {}).get(kind+'_hit_pct')
    # The artifact's payload estimates concern writes, not unique live nodes,
    # and exclude RLP/Snappy/compaction effects. Never label these disk savings.
    out['written_nodes'] = row.get('written_nodes')
    out['payload_estimate_scope'] = 'selected block interval; updated-node visits, uncompressed payload only; not live unique nodes or disk savings'
    for name, per_node in [('myhash_write_payload_bytes_estimate', 32), ('child_version4_write_payload_bytes_estimate', 16*4), ('child_version2_5_write_payload_bytes_estimate', 16*2.5)]:
        out[name] = row['written_nodes']*per_node if row.get('written_nodes') is not None else None
    for name in ('compaction_read', 'compaction_write'):
        value = out[name+'_bytes']
        out[name+'_gb'] = value/10**9 if value is not None else None
        out[name+'_gib'] = value/2**30 if value is not None else None
    return out


def paper_coverage(row):
    native = row['format'] == 'native'
    cumulative = row['engine'].get('cumulative', {})
    has_engine = cumulative.get('available', False)
    diagnostic = row.get('diagnostics_available', False)
    auth_enabled = native and row['profile']['Config']['Replay'].get('Authentication', 'none') not in ('none', '')
    entries = [
        ('DB diagnostics', 'LevelDB read/cache/bloom aggregate', bool(cumulative.get('read_stats')), 'summary.json engine.read_stats, read_path_summary.csv and engine_read_stats.csv; explicit metric version and request scope'),
        ('Fig.5/6/7', 'block/read/write window averages', True, 'windows.csv; read=account+storage, code is separate; H-relative speedups need other matching runs; diagnostic timings are not performance-mode results'),
        ('Table 2', 'negative SST lookups per DB Get', row['engine'].get('read_stats', {}).get('version', 1) >= 2, 'v2 sums negative probes across ALL SST levels, excluding memtables; v1 artifact formulas remain available with a source-quality warning'),
        ('Table 2', 'trie-data-block-only cache hit rate', 'trie' in row['engine'].get('read_stats', {}).get('by_kind', {}), 'v2 Get attribution; legacy artifact all-data-block ratio is separate; native PBSS keys are not classified as hashdb locators'),
        ('Table 3', 'compaction time/bytes/counts, SST count and endpoint size', has_engine, 'paper_metrics.csv + engine.csv; the paper uses performance-mode runs; diagnostic values remain diagnostic; LevelDB totals include flushes; Pebble definitions differ'),
        ('Table 3', 'L0/non-L0 compactions excluding moves', has_engine and cumulative.get('engine') == 'leveldb', 'Pebble events include moves; per-level move-excluded counts are not recorded'),
        ('Section 5.3.1', 'nil/dirty/clean child proportions', diagnostic, 'diagnostic -measure run, weighted over all 16 slots per encoded branch; not unique stored nodes'),
        ('Section 5.3.1', 'myHash cache hit rate', row.get('auth_cache', {}).get('myhash_hit_pct') is not None, 'auth_cache is lifetime; auth_cache_interval requires numbered snapshots at both boundaries'),
        ('Fig.8', 'authenticated run timing and storage', auth_enabled, 'actual keccak/GLS auth via --vary-auth; requires matching plain/auth/cache runs for overhead comparisons; plain H alone is insufficient; differs from the paper random-hash simulation'),
        ('Fig.9', 'no-Snappy plus 1.125x node-size authentication simulation', False, 'compression comparison is supported, but random padding/size multiplier is not ported'),
        ('Fig.10', 'version wraparound at 2 and 2.5 bytes', False, 'requires version-wrap experiment/codec; current 32-bit encoders do not implement this option'),
        ('Fig.11/Table 4', 'single-run engine/compression time and endpoint storage', native, 'comparison requires other engine/compression runs via --vary-engine --vary-compression at a common endpoint; the paper uses 9M'),
        ('Section 5.2', 'key/value-randomized DB rewrite sizes', False, 'normal replay output cannot provide separate randomized-rewrite experiments or a true compression ratio'),
        ('Section 5.4', 'structured hashdb non-archive controls', native and row['profile'].get('StateScheme') == 'hash' and row['profile'].get('ArchiveMode') is False, 'requires -archive=false runs; native PBSS is a distinct retention control; freeze the same flush/checkpoint policy'),
        ('Section 5.1', 'trie nodes per 4KiB data block', False, 'requires SST block entry inspection; WrittenNodes or average node size is not a valid substitute'),
    ]
    return [dict(label=row['label'], reference=ref, metric=metric, available=bool(available), requirement=note) for ref, metric, available, note in entries]


def native_profile(header):
    keys = ('ConsoleInterval', 'Preimages', 'TxTimingScope', 'SchemaVersion', 'Config', 'Version', 'SourceRevision', 'Go', 'OS', 'Arch', 'ChainConfig',
            'StateScheme', 'ArchiveMode', 'VerifyPoW', 'SnapshotLimit', 'Prefetch', 'SnapshotWait', 'StartingHead',
            'GOMAXPROCS', 'RuntimeEnvironment', 'BatchSize', 'PerformanceMode', 'DetailedMetrics', 'EngineOptions', 'AuthFormat', 'AuthImplementation',
            'AuthParallelPolicy', 'CheckpointInterval', 'DiskSizeInterval', 'DBStatsInterval', 'StateHistory',
            'TrienodeHistory', 'TrieDirtyMiB', 'EffectiveTrieDirtyMiB', 'CheckpointPolicy', 'Build', 'TxClassification')
    result = {key: header.get(key) for key in keys}
    result['DBStatsInterval'] = header.get('DBStatsInterval', header.get('DiskSizeInterval'))
    result['TxClassification'] = header.get('TxClassification', 'transaction-gas-limit-21000')
    result['RunOptions'] = {key: header.get('Flags', {}).get(key)
                            for key in ('block-delay', 'drop-os-cache', 'disk-stats-interval')}
    return result


def read_rows(path, metadata):
    if path.suffix == '.jsonl':
        with path.open() as source:
            for lineno, line in enumerate(source, 1):
                try:
                    yield json.loads(line)
                except json.JSONDecodeError as error:
                    if not line.endswith('\n'):
                        yield {'Type': 'partial-tail', 'source': f'{path}:{lineno}'}
                        return
                    raise ValueError(f'{path}:{lineno}: invalid JSONL') from error
    else:
        for number, block in iter_simblocks(path, metadata):
            if block.get('Number') != number:
                raise ValueError('legacy block key/number mismatch')
            yield block


def summarize(path, start, end, window=0, interval='legacy'):
    path = Path(path)
    if path.is_dir():
        path /= 'simblocks.jsonl'
    if not 0 <= start < end or window < 0:
        raise ValueError('require 0 <= start < end and window >= 0')
    native = path.suffix == '.jsonl'
    total = make_bucket(start, end, interval)
    if native and total['first'] == 0:
        raise ValueError('genesis is not an executed block; use --interval after --start 0')
    width = window or end-start
    bins = [make_bucket(n, min(n+width, end), interval) for n in range(start, end, width)]
    disk_targets = {bucket['end']: bucket for bucket in bins}
    metadata, headers, profile = {}, {}, None
    current = None
    batch_last = None
    run_ids = set()
    auth_cache = {}
    last_block = None
    progress, caches, completion, snapshots = {}, {}, {}, {}
    targets = {n for bucket in [total]+bins for n in (bucket['first']-1, bucket['last'])}
    for row in read_rows(path, metadata):
        kind = row.get('Type')
        if kind == 'partial-tail':
            if total['blocks'] == total['last']-total['first']+1 and total['disk_gib'] is not None:
                break
            raise ValueError(f"{row['source']}: incomplete requested interval")
        if kind == 'run':
            current = row
            batch_last = row['StartingHead']
            headers[row['RunID']] = row
            continue
        if kind == 'snapshot':
            if current is None or row['RunID'] != current['RunID']:
                raise ValueError('snapshot without matching run header')
            if row['Number'] in targets:
                snapshots[row['RunID'], row['Number']] = row['Counters']
            continue
        if kind in ('progress', 'auth-cache'):
            if current is None:
                raise ValueError('progress/cache sample without run header')
            run = current['RunID']
            if row.get('RunID', run) != run:
                raise ValueError('progress/cache RunID differs from header')
            number = row.get('Number', last_block)
            if kind == 'progress':
                if row.get('Phase') == 'complete' and number == end:
                    completion[run] = row
                elif number in targets:
                    progress[run, number] = row
            else:
                if number in targets and 'Number' in row:
                    caches[run, number] = row['Stats']
                if run in run_ids and number == end:
                    auth_cache = cache_snapshot(row['Stats'], number, current['StartingHead'])
            continue
        if kind == 'disk' or not native:
            if native and (current is None or current['RunID'] not in run_ids):
                continue
            number, size = row['Number'], row.get('DiskSize', 0)
            if size and number in disk_targets:
                checked(size, 'DiskSize')
                bucket = disk_targets[number]
                bucket['disk_gib'], bucket['disk_block'] = size / 2**30, number
                if number == end:
                    total['disk_gib'], total['disk_block'] = size / 2**30, number
                if 'HistorySize' in row:
                    for target in ([bucket, total] if number == end else [bucket]):
                        for name in ('HistorySize', 'StateHistorySize', 'TrienodeHistorySize'):
                            target[name] = checked(row[name], name)

            if kind == 'disk':
                continue
        if kind == 'batch':
            if current is None:
                raise ValueError('batch without run header')
            first, last = row['First'], row['Last']
            if first > last or first != batch_last + 1:
                raise ValueError('missing/duplicate/out-of-order batch range')
            batch_last = last
            for bucket in [total] + bins:
                if bucket['first'] <= first <= last <= bucket['last']:
                    bucket['import_ns'] += checked(row['ImportTimeNS'], 'ImportTimeNS')
                    bucket['import_blocks'] += last-first+1
            continue
        if 'Number' not in row or (native and 'Research' not in row):
            continue
        n = row['Number']
        last_block = n
        if n > max(end, total['last']):
            break
        if not total['first'] <= n <= total['last']:
            continue
        if native:
            if current is None:
                raise ValueError('block without run header')
            signature = native_profile(current)
            if profile is not None and profile != signature:
                raise ValueError('execution settings or restart boundary changed inside interval')
            profile = signature
            run_ids.add(current['RunID'])
            st, rs = row['Stats'], row['Research']
            values = dict(block_ms=st['TotalTime'], account_read_ms=st['AccountReads'],
                          storage_read_ms=st['StorageReads'], code_read_ms=st['CodeReads'],
                          hash_work_ms=rs['AccountHashes']+rs['StorageHashes'],
                          auth_work_ms=rs['AuthHashes'], codec_work_ms=rs['ModifyHashes'],
                          disk_commit_ms=rs['DiskCommits'],
                          write_ms=st['AccountCommits']+st['StorageCommits']+st['DatabaseCommit']+rs['DiskCommits'])
            for dest, source in (('payment_ms', 'PaymentTxTime'), ('call_ms', 'CallTxTime')):
                if source in row:
                    values[dest] = row[source]
            if profile['Config']['Replay']['Measure']:
                for dest, source in (('snapshot_account_read_ms', 'SnapshotAccountReads'),
                                     ('snapshot_storage_read_ms', 'SnapshotStorageReads'),
                                     ('snapshot_commit_ms', 'SnapshotCommits')):
                    if source in rs:
                        values[dest] = rs[source]
        else:
            values = dict(block_ms=row['BlockExecuteTime'],
                          account_read_ms=row['AccountReads']+row.get('SnapshotAccountReads', 0),
                          storage_read_ms=row['StorageReads']+row.get('SnapshotStorageReads', 0),
                          hash_work_ms=row['AccountHashes']+row['StorageHashes'],
                          disk_commit_ms=row['DiskCommits'],
                          write_ms=sum(row.get(k, 0) for k in ('AccountCommits', 'StorageCommits', 'SnapshotCommits', 'TrieDBCommits', 'DiskCommits')))
        # Match the paper/workbook's account + storage read metric. Contract
        # code reads stay available separately and in an explicit broader sum.
        values['read_ms'] = values['account_read_ms'] + values['storage_read_ms']
        if 'code_read_ms' in values:
            values['read_with_code_ms'] = values['read_ms'] + values['code_read_ms']
        add_block(total, n, values, row)
        sub = bins[(n-total['first'])//width]
        add_block(sub, n, values, row)
        if native:
            for bucket in (total, sub):
                add_diagnostics(bucket, rs, bool(profile['Config']['Replay']['Measure']))
    if native and len(run_ids) != 1:
        raise ValueError('select an interval within one RunID; resumes have different cache/counter state')
    if not native:
        profile = metadata.get('config', {})
    if total['disk_gib'] is None:
        raise ValueError(f'no exact disk sample after block {end}')
    replay = profile['Config']['Replay'] if native else {}
    serial = bool(replay.get('Measure'))
    auth_enabled = replay.get('Authentication', 'none') not in ('none', '')
    result = finish_bucket(total, serial, auth_enabled, native)
    result.update(path=str(path), format='native' if native else 'legacy', profile=profile,
                  metadata=metadata, interval=interval,
                  windows=[finish_bucket(b, serial, auth_enabled, native) for b in bins],
                  run_id=next(iter(run_ids), None),
                  run_header=headers.get(next(iter(run_ids), None)), auth_cache=auth_cache,
                  notes=['Component timers are overlapping work, not an additive wall-time breakdown.'])
    for bucket in [result]+result['windows']:
        apply_progress_and_cache(bucket, result['run_id'], progress, caches)
    result['completion'] = completion.get(result['run_id'])
    if not native:
        result['notes'].append('Legacy timing scope and hashing policy are not established by this analyzer; no-hash is unavailable.')
    elif not serial:
        result['notes'].append('Parallel hash sums cannot be subtracted from elapsed block time; codec time was not measured.')
    else:
        result['notes'].append('Diagnostic serial run; this is not representative performance-mode timing.')
    if native and not serial and result['run_header'].get('PerformanceMode') is False:
        result['notes'].append('DB profiling is enabled; parallel execution timings are still diagnostic.')
    if result['import_ms'] is None and native:
        result['notes'].append('Import time unavailable: the requested interval cuts batch boundaries or has no batch records.')
    result['engine'] = summarize_engine(path.parent, result) if native else {'available': False, 'reason': 'legacy engine files use a separate format'}
    if native:
        run = next(iter(run_ids))
        for bucket in [result] + result['windows']:
            a = snapshots.get((run, bucket['first']-1))
            b = snapshots.get((run, bucket['last']))
            if a is not None and b is not None:
                bucket['snapshot_counters'] = {k: difference(a[k], b[k]) for k in a.keys() & b.keys()}
    if result['engine'].get('wal_write_bytes_note'):
        result['notes'].append(result['engine']['wal_write_bytes_note'])
    if native and profile.get('TxClassification') == 'transaction-gas-limit-21000':
        result['notes'].append('Older modern payment/call split used gas limit, not legacy receipt gas used; total tx/gas counts are unaffected.')
    if result['engine'].get('read_stats', {}).get('version') == 1:
        result['notes'].append('Legacy read-stats v1 contains known L0/Bloom/cache-scope errors; derived ratios cannot repair the source counters.')
    return result


def summarize_replay(path, start, end):
    return summarize(path, start, end)


def summarize_legacy(path, start, end):
    return summarize(path, start, end)


def difference(first, last):
    if type(first) is not int or type(last) is not int or last < first:
        raise ValueError('invalid/reset cumulative engine counter inside a RunID')
    return last-first


def engine_totals(engine, a, b, begin, end):
    # a=None means counters since DB open, including genesis/setup. Gauges use b.
    def delta(x, y):
        return difference(x, y) if a is not None else checked(y, 'engine counter')

    def counter(x, y, field):
        return delta(x.get(field, 0), y[field])

    answer = dict(available=True, engine=engine, after_start=begin, after_end=end)
    if engine == 'pebble':
        x, y = (a or b)['Engine'], b['Engine']
        hits = counter(x['BlockCache'], y['BlockCache'], 'Hits')
        misses = counter(x['BlockCache'], y['BlockCache'], 'Misses')
        answer.update(cache_hits=hits, cache_misses=misses, cache_hit_ratio=ratio(hits, hits+misses),
                      cache_scope='all Pebble block kinds; not trie-data-block-only',
                      cache_bytes_end=y['BlockCache']['Size'], memtable_bytes_end=y['MemTable']['Size'],
                      sst_bytes_end=sum(v['TablesSize'] for v in y['Levels']),
                      sst_count_end=sum(v['TablesCount'] for v in y['Levels']),
                      wal_bytes_end=y['WAL']['PhysicalSize'],
                      flush_count=counter(x['Flush'], y['Flush'], 'Count'),
                      compaction_count=counter(x['Compact'], y['Compact'], 'Count'),
                      compaction_move_count=counter(x['Compact'], y['Compact'], 'MoveCount'),
                      compaction_seconds=counter(x['Compact'], y['Compact'], 'Duration')/1e9,
                      time_definition='sum of completed compaction durations; excludes memtable flushes')
        for name, field in [('compaction_read_bytes', 'TableBytesRead'), ('compaction_write_bytes', 'TableBytesCompacted'), ('flush_write_bytes', 'TableBytesFlushed')]:
            answer[name] = delta(sum(v[field] for v in x['Levels']), sum(v[field] for v in y['Levels']))
        answer['logical_wal_input_bytes'] = counter(x['WAL'], y['WAL'], 'BytesIn')
        # Pebble v2.1.4 derives BytesWritten from L0 input + live WAL size.
        # A short PBSS replay observed this decreasing across a flush. Preserve
        # the raw samples and mark that delta unavailable, never clamp it to 0.
        if a is not None and y['WAL']['BytesWritten'] < x['WAL']['BytesWritten']:
            answer['wal_write_bytes'] = None
            answer['wal_write_bytes_note'] = 'Native WAL.BytesWritten decreased; interval write bytes unavailable.'
        else:
            answer['wal_write_bytes'] = counter(x['WAL'], y['WAL'], 'BytesWritten')
        for name, field, divisor in [('write_stall_seconds', 'WriteStallTime', 1e9), ('write_stall_count', 'WriteStalls', 1), ('compaction_active_seconds', 'CompactionActiveTime', 1e9), ('l0_compactions_started_including_moves', 'Level0Compactions', 1), ('non_l0_compactions_started_including_moves', 'NonLevel0Compactions', 1)]:
            value = counter(a or b, b, field)
            answer[name] = value/divisor if divisor != 1 else value
    else:
        for name, field in [('engine_read_bytes', 'IORead'), ('engine_write_bytes', 'IOWrite'),
                            ('flush_count', 'MemComp'), ('l0_compaction_count', 'Level0Comp'),
                            ('non_l0_compaction_count', 'NonLevel0Comp'), ('write_stall_count', 'WriteDelayCount')]:
            answer[name] = counter(a or b, b, field)
        if 'SeekComp' in b:
            answer['seek_compaction_count'] = counter(a or b, b, 'SeekComp')
        for name, field in [('compaction_read_bytes', 'LevelRead'), ('compaction_write_bytes', 'LevelWrite'), ('compaction_seconds', 'LevelDurations')]:
            value = delta(sum((a or b)[field] or []), sum(b[field] or []))
            answer[name] = value/1e9 if field == 'LevelDurations' else value
        answer.update(sst_bytes_end=sum(b['LevelSizes'] or []), sst_count_end=sum(b['LevelTablesCounts'] or []),
                      cache_bytes_end=b['BlockCacheSize'],
                      opened_tables_end=b.get('OpenedTablesCount'), alive_iterators_end=b.get('AliveIterators'),
                      alive_snapshots_end=b.get('AliveSnapshots'), write_paused_end=b.get('WritePaused'),
                      write_stall_seconds=counter(a or b, b, 'WriteDelayDuration')/1e9,
                      time_definition='sum of compaction durations, including memtable flushes')
        answer['compaction_count'] = sum(answer[k] for k in ('flush_count', 'l0_compaction_count', 'non_l0_compaction_count')) + answer.get('seek_compaction_count', 0)
    answer['levels'] = []
    if engine == 'leveldb':
        def at(sample, field, i):
            values = sample.get(field) or []
            return values[i] if i < len(values) else 0
        for i in range(max(len(b.get(field) or []) for field in ('LevelSizes', 'LevelTablesCounts', 'LevelRead'))):
            level = dict(level=i, bytes_end=at(b, 'LevelSizes', i), tables_end=at(b, 'LevelTablesCounts', i))
            for name, field in [('compaction_read_bytes', 'LevelRead'), ('compaction_write_bytes', 'LevelWrite'), ('compaction_seconds', 'LevelDurations')]:
                value = delta(at(a or b, field, i), at(b, field, i))
                level[name] = value/1e9 if field == 'LevelDurations' else value
            answer['levels'].append(level)
    else:
        for i, level in enumerate(b['Engine']['Levels']):
            old = (a or b)['Engine']['Levels'][i]
            out = dict(level=i, bytes_end=level['TablesSize'], tables_end=level['TablesCount'])
            for name, field in [('compaction_read_bytes', 'TableBytesRead'), ('compaction_write_bytes', 'TableBytesCompacted'), ('flush_write_bytes', 'TableBytesFlushed')]:
                out[name] = delta(old[field], level[field])
            answer['levels'].append(out)
    if engine == 'leveldb':
        for kind in ('read', 'write'):
            answer['non_compaction_'+kind+'_bytes_residual'] = answer['engine_'+kind+'_bytes']-answer['compaction_'+kind+'_bytes']
    answer['note'] = 'Non-compaction residuals follow the artifact subtraction, not independently measured foreground I/O. Engine counters include background work; file reads are not necessarily physical SSD I/O.'
    return answer


def derive_read_metrics(raw):
    # Formulas follow the artifact's extract_storage_metrics.py. v1 source
    # counters remain identifiable; applying the formula cannot repair old data.
    fake, real = raw.get('FakeLevels', {}), raw.get('RealLevels', {})
    hits, misses = raw.get('CacheHitCounts', {}), raw.get('CacheMissCounts', {})
    requests = raw['ReadRequestCount']
    negative = sum(fake.values())
    bloom_hit, bloom_miss, bloom_fp = (raw.get(k, 0) for k in ('BloomHitCount', 'BloomMissCount', 'BloomFalsePositiveCount'))
    out = dict(db_gets=requests, negative_sst_probes=negative,
               negative_sst_lookups_per_read=ratio(negative, requests),
               fake_mem_lookups=sum(raw.get('FakeMems', {}).values()),
               fake_l0_lookups=fake.get('0', 0),
               legacy_non_l0_negative_probes=sum(v for k, v in fake.items() if int(k) > 0),
               real_sst_probes=sum(real.values()),
               bloom_false_positive_rate=ratio(bloom_fp, bloom_fp+bloom_miss),
               bloom_positive_predictive_value=1-ratio(bloom_fp, bloom_hit) if bloom_hit else None,
               bloom_skip_rate=ratio(bloom_miss, bloom_hit+bloom_miss))
    out['all_negative_lookups_per_read'] = ratio(negative+out['fake_mem_lookups'], requests)
    for kind in ('data', 'index', 'filter'):
        h, m = hits.get(kind+'-block', 0), misses.get(kind+'-block', 0)
        out[kind+'_block_cache_hit_pct'] = ratio(h, h+m, 100)
    return out


def derive_operation_metrics(raw, total):
    def count(field, stats=raw):
        return sum(stats.get(field, {}).values())
    lookups = count('CacheHitCounts')+count('CacheMissCounts')
    all_lookups = count('CacheHitCounts', total)+count('CacheMissCounts', total)
    out = dict(requests=raw.get('Requests', 0), cache_lookups=lookups,
               cache_hit_pct=ratio(count('CacheHitCounts'), lookups, 100),
               cache_lookup_share_pct=ratio(lookups, all_lookups, 100),
               block_reads=count('BlockReadCounts'), block_read_bytes=count('BlockReadBytes'),
               block_decoded_bytes=count('BlockDecodedBytes'), block_load_errors=count('BlockLoadErrors'),
               block_read_seconds=count('BlockReadNanoseconds')/1e9,
               block_decode_seconds=count('BlockDecodeNanoseconds')/1e9,
               block_read_byte_share_pct=ratio(count('BlockReadBytes'), count('BlockReadBytes', total), 100))
    for kind in ('data', 'index', 'filter'):
        h = raw.get('CacheHitCounts', {}).get(kind+'-block', 0)
        m = raw.get('CacheMissCounts', {}).get(kind+'-block', 0)
        out[kind+'_block_cache_hit_pct'] = ratio(h, h+m, 100)
    return out


def validate_operation_totals(raw):
    total, parts = raw.get('AllOperations', {}), raw.get('ByOperation', {})
    for key, value in total.items():
        if isinstance(value, dict):
            sums = {}
            for part in parts.values():
                for kind, n in part.get(key, {}).items():
                    sums[kind] = sums.get(kind, 0)+n
            valid = sums == value
        else:
            valid = sum(part.get(key, 0) for part in parts.values()) == value
        if not valid:
            raise ValueError('operation counters do not sum to overall '+key)
    if raw.get('ReadRequestCount', 0) != parts.get('get', {}).get('Requests', 0):
        raise ValueError('Get request counts disagree between compatibility and operation metrics')


def read_stats_delta(first, last):
    raw = {}
    for key, value in last.items():
        if key == 'SchemaVersion':
            if first and first.get(key) != value:
                raise ValueError('read-stats schema changed within interval')
            raw[key] = value
            continue
        old = first.get(key, {} if isinstance(value, dict) else 0)
        raw[key] = read_stats_delta(old, value)['raw'] if isinstance(value, dict) else difference(old, value)
    result = dict(raw=raw)
    if 'ReadRequestCount' in raw:
        result.update(derive_read_metrics(raw))
        result['version'] = raw.get('SchemaVersion', 1)
        result['scope'] = ('Get-family requests only; Has/iterators/compaction excluded; caller-classified trie/code/other; Bloom FP means positive without a visible matching key'
                           if result['version'] >= 2 else 'legacy mixed-operation aggregate; incomplete DontFillCache misses and L0/Bloom outcomes; old counters cannot be repaired')
        result['by_kind'] = {k: derive_read_metrics(v) for k, v in raw.get('ByKind', {}).items()}
        if result['version'] >= 3 and ('AllOperations' in raw or 'ByOperation' in raw):
            validate_operation_totals(raw)
            total = raw.get('AllOperations', {})
            result['all_operations'] = derive_operation_metrics(total, total)
            result['by_operation'] = {k: derive_operation_metrics(v, total) for k, v in raw.get('ByOperation', {}).items()}
    return result


def apply_progress_and_cache(bucket, run_id, progress, caches):
    begin, end = (run_id, bucket['first']-1), (run_id, bucket['last'])
    bucket.update(wall_seconds=None, wall_ms=None, utc_start=None, utc_end=None)
    if begin in progress and end in progress:
        a, b = progress[begin], progress[end]
        seconds = difference(a['ReplayElapsedNS'], b['ReplayElapsedNS'])/1e9
        bucket.update(wall_seconds=seconds, wall_ms=seconds*1000/bucket['blocks'],
                      utc_start=a['TimestampUTC'], utc_end=b['TimestampUTC'],
                      process_elapsed_seconds_at_end=b['ProcessElapsedNS']/1e9)
    bucket['auth_cache_interval'] = None
    if begin in caches and end in caches:
        a, b = caches[begin], caches[end]
        out = dict(scope='selected interval; cumulative probe-counter differences', after_start=begin[1], after_end=end[1])
        if a.get('MyHash') and b.get('MyHash'):
            gets = difference(a['MyHash']['GetCalls'], b['MyHash']['GetCalls'])
            misses = difference(a['MyHash']['Misses'], b['MyHash']['Misses'])
            out['myhash_hit_pct'] = ratio(gets-misses, gets, 100)
        for kind in ('accumulator', 'contribution'):
            h, m = kind+'_hits', kind+'_misses'
            if not all(h in v.get('Computation', {}) and m in v.get('Computation', {}) for v in (a, b)):
                continue
            hits = difference(a['Computation'][h], b['Computation'][h])
            misses = difference(a['Computation'][m], b['Computation'][m])
            out[kind+'_hit_pct'] = ratio(hits, hits+misses, 100)
        bucket['auth_cache_interval'] = out


def summarize_engine(directory, result):
    engine = result['profile']['Config']['Engine']
    path = directory / f'{engine}_metrics.jsonl'
    if not path.exists():
        return dict(available=False, reason='engine metrics file is absent')
    # Counter intervals follow executed blocks; Table 3 separately uses the size endpoint.
    buckets = [result] + result['windows']
    targets = {n for bucket in buckets for n in (bucket['first']-1, bucket['last'])} | {result['end']}
    samples, details, header = {}, {}, None
    for row in read_rows(path, {}):
        if row.get('Type') == 'partial-tail':
            break
        if row.get('Type') == 'run':
            if row.get('RunID') == result['run_id']:
                header = row
            continue
        if row.get('RunID') != result['run_id']:
            continue
        if row.get('Number') in targets:
            samples[row['Number']] = row['Metrics']
            if row.get('Detailed') is not None:
                details[row['Number']] = row['Detailed']
        if row.get('Number', 0) > max(targets):
            break
    if header != result['run_header']:
        raise ValueError(f'{path}: engine/simblocks run headers differ')
    for bucket in buckets:
        begin, end = bucket['first']-1, bucket['last']
        if begin in samples and end in samples:
            bucket['engine'] = engine_totals(engine, samples[begin], samples[end], begin, end)
            if begin in details and end in details:
                bucket['engine']['read_stats'] = read_stats_delta(details[begin], details[end])
        else:
            bucket['engine'] = dict(available=False, reason='no exact engine snapshots at both boundaries; use --interval after and sampled endpoints', after_start=begin, after_end=end)
    answer = result['engine']
    if result['end'] in samples:
        cumulative = engine_totals(engine, None, samples[result['end']], header['StartingHead'], result['end'])
        cumulative['scope'] = f"since DB open at head {header['StartingHead']}, including setup; endpoint after block {result['end']}"
        if result['end'] in details:
            cumulative['read_stats'] = read_stats_delta({}, details[result['end']])
        answer['cumulative'] = cumulative
    return answer


def comparison_profile(row, vary_auth=False, vary_engine=False, vary_compression=False, vary_retention=False):
    profile = json.loads(json.dumps(row['profile']))
    if row['format'] == 'native':
        profile['Config'].pop('DatabasePath', None)
        replay = profile['Config']['Replay']
        for key in ('KeyScheme', 'Codec', 'EpochSize', 'StateDepth', 'StorageDepth', 'ShardPrefix', 'TileBlocks', 'TilePathPrefix', 'RunTargetNodes'):
            replay.pop(key, None)
        if vary_auth:
            for key in ('Authentication', 'AuthMode', 'AuthWorkers', 'AuthCacheMiB', 'AuthCacheMode'):
                replay.pop(key, None)
            for key in ('AuthFormat', 'AuthImplementation', 'AuthParallelPolicy'):
                profile.pop(key, None)
        if vary_compression:
            profile['Config'].pop('Compression', None)
            options = profile.get('EngineOptions', '')
            if options.startswith('{'):
                options = json.loads(options)
                options.pop('Compression', None)
                profile['EngineOptions'] = json.dumps(options, sort_keys=True)
            else:
                profile['EngineOptions'] = re.sub(r'(?m)^\s*compression=.*$', '', options)
        if vary_retention:
            profile['Config'].pop('NonArchive', None)
            for key in ('StateScheme', 'ArchiveMode', 'StateHistory', 'TrienodeHistory', 'CheckpointPolicy', 'CheckpointInterval'):
                profile.pop(key, None)
        if vary_engine:
            profile['Config'].pop('Engine', None)
            profile.pop('EngineOptions', None)
    else:
        profile.pop('key_scheme', None)
        if vary_auth:
            profile.pop('features', None)
        # Legacy files are inspectable, but unknown run settings are not proof of equivalence.
        if not profile:
            raise ValueError('legacy result has no configuration; inspect each file separately')
        profile.get('simulation', {}).pop('server_port', None)
        for key in ('path', 'delete_disk'):
            profile.get('database', {}).pop(key, None)
    return profile


def compare_rows(rows, vary_auth=False, vary_engine=False, vary_compression=False, vary_retention=False):
    for row in rows[1:]:
        base = rows[0]
        if (row['format'] != base['format'] or row['workload_sha256'] != base['workload_sha256'] or
                row['block_hash_sha256'] != base['block_hash_sha256'] or
                comparison_profile(row, vary_auth, vary_engine, vary_compression, vary_retention) != comparison_profile(base, vary_auth, vary_engine, vary_compression, vary_retention)):
            raise ValueError(f"{row['label']}: workload or execution/storage settings differ; legacy/modern timings cannot be equated")


def save_outputs(rows, directory):
    import csv
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    fields = ['label', 'reference', 'start', 'end', 'first', 'last', 'blocks', 'total_seconds', 'txs', 'gas'] + list(FIELDS)
    for name, entries in [('summary', rows), ('windows', [dict(w, label=r['label']) for r in rows for w in r['windows']])]:
        with (directory/f'{name}.csv').open('w', newline='') as output:
            writer = csv.DictWriter(output, fieldnames=fields, extrasaction='ignore')
            writer.writeheader(); writer.writerows(entries)
    engines, levels, read_rows, operation_rows, detail_rows = [], [], [], [], []
    for row in rows:
        for scope, stats in [('interval', row['engine']), ('cumulative', row['engine'].get('cumulative', {}))]:
            if stats:
                engines.append(dict(label=row['label'], scope=scope, **{k: v for k, v in stats.items() if k not in ('cumulative', 'scope', 'read_stats', 'levels')}))
                levels.extend(dict(label=row['label'], scope=scope, **level) for level in stats.get('levels', []))
                read = stats.get('read_stats', {})
                for kind, metrics in [('all', read)] + list(read.get('by_kind', {}).items()):
                    if metrics:
                        read_rows.append(dict(label=row['label'], scope=scope, request_kind=kind,
                                              version=read['version'], request_scope=read['scope'],
                                              **{k: v for k, v in metrics.items() if k not in ('raw', 'by_kind', 'by_operation', 'all_operations', 'scope', 'version')}))
                for operation, metrics in [('all', read.get('all_operations', {}))] + list(read.get('by_operation', {}).items()):
                    if metrics:
                        operation_rows.append(dict(label=row['label'], scope=scope, operation=operation,
                                                   version=read['version'], **metrics))
                def flatten(value, path=()):
                    for key, val in value.items():
                        if isinstance(val, dict):
                            yield from flatten(val, path+(key,))
                        else:
                            yield dict(label=row['label'], scope=scope, metric='.'.join(path+(key,)), value=val)
                detail_rows.extend(flatten(read.get('raw', {})))
    for name, entries in [('engine', engines), ('paper_metrics', [paper_metrics(r) for r in rows]),
                          ('paper_coverage', [entry for r in rows for entry in paper_coverage(r)]),
                          ('engine_levels', levels), ('read_path_summary', read_rows), ('engine_operations', operation_rows), ('engine_read_stats', detail_rows)]:
        with (directory/f'{name}.csv').open('w', newline='') as output:
            writer = csv.DictWriter(output, fieldnames=list(dict.fromkeys(k for entry in entries for k in entry)))
            writer.writeheader(); writer.writerows(entries)
    with (directory/'summary.json').open('w') as output:
        json.dump(rows, output, indent=2, allow_nan=False)


def plot(rows, path):
    # Optional, same plotting library as geth-sim/data-analysis; no dependency for CSV/JSON.
    try:
        import matplotlib
        matplotlib.use('Agg')
        import matplotlib.pyplot as plt
    except ImportError as error:
        raise ValueError('--plot requires matplotlib in the analysis environment') from error
    fig, axes = plt.subplots(2, 3, figsize=(16, 8), constrained_layout=True)
    timing_field = 'block_ms' if rows[0]['timing_metric'] == 'block' else 'no_hash_ms'
    timing_title = 'Block elapsed' if timing_field == 'block_ms' else 'Block minus hash work (diagnostic)'
    for field, ax, title in zip((timing_field, 'speedup', 'disk_gib', 'read_ms', 'write_ms', 'auth_work_ms'), axes.flat,
                               (timing_title + ' (ms/block)', 'Selected-metric speedup over first input (x)', 'DB files (GiB)', 'Read timer sum (ms/block)', 'Write timer sum (ms/block)', 'Auth work (ms/block; overlaps hashing)')):
        for row in rows:
            pts = [(w['end']/1e6, w.get(field)) for w in row['windows'] if w.get(field) is not None]
            if pts:
                x, y = zip(*pts); ax.plot(x, y, label=row['label'])
        ax.set(title=title, xlabel='Block (millions)'); ax.grid(alpha=.3)
        if ax.lines:
            ax.legend()
    fig.savefig(path, dpi=160)
    plt.close(fig)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--compare', nargs='+', required=True, metavar='LABEL=FILE_OR_DIR')
    p.add_argument('--start', type=int, required=True)
    p.add_argument('--end', type=int, required=True)
    p.add_argument('--window', type=int, default=0, help='non-overlapping block windows; 0 means whole interval')
    p.add_argument('--interval', choices=('legacy', 'after'), default='legacy', help='legacy: [start,end); after: (start,end], recommended for native DB snapshots')
    p.add_argument('--metric', choices=('block', 'no-hash'), default='block')
    p.add_argument('--vary-auth', action='store_true')
    p.add_argument('--vary-engine', action='store_true', help='explicit engine comparison with the same cache allowances/compression; actual allocations still differ')
    p.add_argument('--vary-compression', action='store_true', help='explicitly permit compression to differ')
    p.add_argument('--vary-retention', action='store_true', help='explicitly compare archive hashdb with native PBSS/history; different retention/checkpoint policies')
    p.add_argument('--out', help='write summary/windows/engine/paper CSV and metadata JSON to this directory')
    p.add_argument('--plot', metavar='PNG_OR_PDF', help='optional matplotlib chart of window averages, not rolling averages')
    a = p.parse_args()
    if not 0 <= a.start < a.end or a.window < 0:
        p.error('require 0 <= start < end and window >= 0')
    if a.vary_auth and a.metric == 'no-hash':
        p.error('authentication comparisons must include hashing')
    rows = []
    try:
        for item in a.compare:
            label, sep, path = item.partition('=')
            if not sep or not label or not path or any(r['label'] == label for r in rows):
                raise ValueError('expected unique LABEL=FILE_OR_DIR')
            row = summarize(path, a.start, a.end, a.window, a.interval)
            row['label'] = label
            if a.metric == 'no-hash' and row['no_hash_ms'] is None:
                raise ValueError('no-hash requires native serial -measure, auth=none; it is a diagnostic, not measured performance')
            rows.append(row)
        compare_rows(rows, a.vary_auth, a.vary_engine, a.vary_compression, a.vary_retention)
        relative_metrics(rows, a.metric)
        if a.vary_retention:
            for row in rows:
                row['notes'].append('Retention/checkpoint policy may differ by explicit request; PBSS vs archive is not an equal-history storage comparison.')
        if a.out:
            save_outputs(rows, a.out)
        if a.plot:
            plot(rows, a.plot)
    except (ValueError, OSError, KeyError, TypeError) as exc:
        p.error(str(exc))
    bounds = f'({a.start},{a.end}]' if a.interval == 'after' else f'[{a.start},{a.end})'
    print(f'Timing {bounds}; disk after block {a.end}. Components can overlap; NA means unavailable/unmeasured.')
    field = 'block_ms' if a.metric == 'block' else 'no_hash_ms'
    columns = ('label', 'timing_metric', 'speedup', 'block_ms', 'import_ms', 'read_ms', 'write_ms', 'hash_work_ms', 'auth_work_ms', 'codec_work_ms', 'no_hash_ms', 'disk_gib')
    print('\t'.join(columns + ('time_delta_%', 'disk_delta_%')))
    for row in rows:
        values = [row.get(k) for k in columns]
        values += [100*(row[field]/rows[0][field]-1) if rows[0][field] else None,
                   100*(row['disk_gib']/rows[0]['disk_gib']-1)]
        print('\t'.join('NA' if v is None else f'{v:.6f}' if isinstance(v, float) else str(v) for v in values))
        for note in row['notes']:
            print(f"{row['label']}: {note}", file=sys.stderr)
        if not row['engine']['available']:
            print(f"{row['label']}: {row['engine']['reason']}", file=sys.stderr)


if __name__ == '__main__':
    main()
