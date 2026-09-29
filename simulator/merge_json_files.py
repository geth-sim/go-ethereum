from datetime import datetime
import argparse
import hashlib
import json
from pathlib import Path
import sys

FILE_PATH = "logFiles/evm/simBlocks/"

files_to_merge = ['copied_evm_simulation_result_EthereumPR_0_5000000.json',
                  'copied_evm_simulation_result_EthereumPR_5000001_7500000.json']

merged_file_name = 'merged.json'


def combine_jsons(file_list):
    print("start merge json files")

    all_data_dict = {}
    merged_schema_version = None
    merged_config_sha256 = None
    merged_config = None
    input_format = None
    for json_file in file_list:
        print("  try to merge:", json_file)
        with open(FILE_PATH+json_file,'r') as file:
            data = json.load(file)

        is_envelope = "schema_version" in data and "blocks" in data
        current_format = "envelope" if is_envelope else "legacy"
        if input_format is None:
            input_format = current_format
        elif input_format != current_format:
            raise ValueError("cannot merge legacy and metadata-envelope simulation results")

        if is_envelope:
            if merged_schema_version is None:
                merged_schema_version = data["schema_version"]
                merged_config_sha256 = data.get("config_sha256")
                merged_config = data["config"]
            elif data["schema_version"] != merged_schema_version or data["config"] != merged_config:
                raise ValueError("simulation configs differ; refusing to merge incompatible results")
            blocks = data["blocks"]
        else:
            blocks = data

        for block_num, block in blocks.items():
            if block_num in all_data_dict and all_data_dict[block_num] != block:
                raise ValueError("conflicting data for block " + block_num)
            all_data_dict[block_num] = block

    # save to json file
    with open(FILE_PATH+merged_file_name, "w") as outfile:
        if input_format == "envelope":
            output = {
                "schema_version": merged_schema_version,
                "config": merged_config,
                "blocks": all_data_dict,
            }
            if merged_config_sha256 is not None:
                output["config_sha256"] = merged_config_sha256  # preserve archived envelopes
        else:
            output = all_data_dict
        json.dump(output, outfile, indent=2)
    
    print("  => success, merged file name:", merged_file_name)


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


def summarize_simblocks(path, start, end):
    """Reuse the experiments' [start,end) timing and disk-at-end convention."""
    if not 0 <= start < end:
        raise ValueError("require 0 <= start < end")
    metadata, sums, previous, count, disk_size = {}, {}, -1, 0, None
    workload = hashlib.sha256()
    required = ("BlockExecuteTime", "AccountHashes", "StorageHashes",
                "PaymentTxLen", "CallTxLen", "GasUsed")
    timing = required + ("AccountReads", "StorageReads", "SnapshotAccountReads",
                         "SnapshotStorageReads", "AccountCommits", "StorageCommits",
                         "SnapshotCommits", "TrieDBCommits", "DiskCommits")
    for number, block in iter_simblocks(path, metadata):
        if number != previous + 1 or block.get("Number") != number:
            raise ValueError(f"{path}: missing, duplicate or unordered block at {number}")
        previous = number
        if start <= number < end:
            for field in required:
                if field not in block:
                    raise ValueError(f"{path}: missing {field} at block {number}")
            for field in timing:
                value = block.get(field, 0)
                if type(value) is not int or value < 0:
                    raise ValueError(f"{path}: invalid {field} at block {number}")
                sums[field] = sums.get(field, 0) + value
            workload.update(f"{number}:{block['PaymentTxLen']}:{block['CallTxLen']}:{block['GasUsed']}\n".encode())
            count += 1
        if number == end:
            disk_size = block.get("DiskSize")
            break
    if count != end - start or previous != end or not isinstance(disk_size, int) or disk_size <= 0:
        raise ValueError(f"{path}: incomplete interval or missing DiskSize at block {end}")
    scale = count * 1_000_000
    read = sum(sums[f] for f in ("AccountReads", "StorageReads", "SnapshotAccountReads", "SnapshotStorageReads"))
    write = sum(sums[f] for f in ("AccountCommits", "StorageCommits", "SnapshotCommits", "TrieDBCommits", "DiskCommits"))
    return dict(path=str(path), start=start, end_exclusive=end, blocks=count,
                workload_sha256=workload.hexdigest(), metadata=metadata,
                block_ms=sums["BlockExecuteTime"] / scale,
                no_hash_ms=(sums["BlockExecuteTime"] - sums["AccountHashes"] - sums["StorageHashes"]) / scale,
                read_ms=read / scale, write_ms=write / scale, disk_gib=disk_size / 2**30)


def compare_simblocks(inputs, start, end):
    """The first input is the baseline; results are printed without new files."""
    results = []
    for item in inputs:
        label, separator, path = item.partition("=")
        if not separator or not label or not path:
            raise ValueError("each comparison input must be LABEL=FILE")
        row = summarize_simblocks(path, start, end)
        row["label"] = label
        config = row["metadata"].get("config", {})
        measurement = config.get("measurement", {})
        if any(measurement.get(flag) for flag in ("measure_read_stats", "measure_child_stats", "measure_leveldb_read_stats", "measure_key_profile", "measure_key_design_stats", "measure_cohort_reads", "measure_trie_write_stats", "measure_trie_read_age_stats")) or measurement.get("layout_capture", {}).get("enabled"):
            raise ValueError(f"{label}: detailed diagnostic run cannot be used as a timing baseline")
        if results:
            baseline = results[0]
            if row["workload_sha256"] != baseline["workload_sha256"]:
                raise ValueError(f"{label}: block workloads differ from baseline")
            base_config = baseline["metadata"].get("config", {})
            if config and base_config:
                for field in ("cache", "features"):
                    if config.get(field) != base_config.get(field):
                        raise ValueError(f"{label}: {field} settings differ from baseline")
                checks = {
                    "simulation": ("simulation_mode", "chain_config", "is_archive_mode", "is_path_scheme", "enable_snapshot", "is_dos_attacking"),
                    "database": ("backend", "compression", "leveldb_logging_build", "leveldb_handles", "pebble_ephemeral", "trie_preimages", "path_state_history", "trie_flush_interval_ns"),
                    "measurement": ("enabled_expensive", "metrics_expensive", "logging_read_stats", "logging_opcode_stats", "disk_size_measure_epoch", "save_leveldb_stats_epoch"),
                    "workload": ("type", "random_seed", "transactions_per_block", "total_account_num", "active_address_percentage"),
                }
                for section, fields in checks.items():
                    if section == "workload" and not (config.get(section) and base_config.get(section)):
                        continue  # only archived files carry client workload metadata
                    for field in fields:
                        if config.get(section, {}).get(field) != base_config.get(section, {}).get(field):
                            raise ValueError(f"{label}: {section}.{field} differs from baseline")
        if not config:
            print(f"{label}: legacy file; database/cache settings cannot be verified", file=sys.stderr)
        results.append(row)
    baseline = results[0]
    print(f"Timing [{start}, {end}); storage at block {end}. Negative deltas are improvements.")
    print("scheme\tblock_ms\tno_hash_ms\tread_ms\twrite_ms\tdisk_GiB\ttime_delta_%\tdisk_delta_%")
    for row in results:
        time_delta = 100 * (row["no_hash_ms"] / baseline["no_hash_ms"] - 1) if baseline["no_hash_ms"] else float("nan")
        disk_delta = 100 * (row["disk_gib"] / baseline["disk_gib"] - 1)
        print(f"{row['label']}\t{row['block_ms']:.6f}\t{row['no_hash_ms']:.6f}\t{row['read_ms']:.6f}\t{row['write_ms']:.6f}\t{row['disk_gib']:.6f}\t{time_delta:+.3f}\t{disk_delta:+.3f}")
    return results


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Merge or compare simulator result files")
    parser.add_argument("--compare", nargs="+", metavar="LABEL=FILE", help="compare runs; first input is baseline")
    parser.add_argument("--start", type=int, default=5_000_000)
    parser.add_argument("--end", type=int, default=6_000_000)
    args = parser.parse_args()
    if args.compare:
        try:
            compare_simblocks(args.compare, args.start, args.end)
        except (ValueError, OSError) as exc:
            parser.error(str(exc))
        sys.exit(0)

    start_time = datetime.now()
    combine_jsons(files_to_merge)
    end_time = datetime.now()
    
    print("final elapsed time:", end_time-start_time)
