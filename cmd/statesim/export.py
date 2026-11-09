#!/usr/bin/env python3
"""Export the existing SQL dataset as signed block RLP, without touching its DB.

This adapter supports legacy, EIP-2930 and EIP-1559 transactions through London.
Fail closed on newer encodings; extend this input adapter separately from replay.
Credentials remain in the existing, untracked db_utils.py; never copy them here.
"""
import argparse
import collections
import contextlib
import os
from pathlib import Path
import runpy
import sys

import rlp
from eth_hash.auto import keccak


def integer(value):
    n = int.from_bytes(value, "big") if isinstance(value, bytes) else int(value or 0)
    return n.to_bytes((n.bit_length() + 7) // 8, "big")


def header(row, uncle=False):
    if row.get("withdrawalsroot") is not None:
        raise ValueError("SQL adapter needs withdrawal bodies for post-Shanghai blocks")

    # Fixed-width SQL BINARY fields right-pad short integer byte strings.
    # Restore missing leading zeros only if the canonical header hash matches.
    fields = [row["parenthash"], row["sha3uncles"], row["miner"], row["stateroot"],
              row["transactionsroot"], row["receiptsroot"], row["logsbloom"] or bytes(256),
              integer(row["difficulty"]), integer(row["uncleheight" if uncle else "number"]),
              integer(row["gaslimit"]), integer(row["gasused"]), integer(row["timestamp"]),
              row["extradata"] or b"", row["mixhash"], row["nonce"]]
    if row["basefee"] is not None:
        fields.append(integer(row["basefee"]))
    for nonce in padded_integer_candidates(row["nonce"]):
        fields[14] = nonce
        if keccak(rlp.encode(fields)) == row["hash"]:
            return fields
    raise ValueError(f"SQL header hash mismatch at {row.get('number', row.get('uncleheight'))}")


def padded_integer_candidates(raw):
    yield raw
    for n in range(1, len(raw) - len(raw.rstrip(b"\x00")) + 1):
        yield bytes(n) + raw[:-n]



def transaction(row, access_list=()):
    kind = int.from_bytes(integer(row["type"]), "big")
    if kind not in (0, 1, 2):
        raise ValueError(f"unsupported SQL transaction type {kind}")
    if kind == 0:
        fields = [integer(row["nonce"]), integer(row["gasprice"]), integer(row["gas"]),
                  row["to"] or b"", integer(row["value"]), row["input"] or b""]
        prefix = b""
        parity = (row["v"], 27, 28, 37, 38)
    else:
        fields = [integer(row.get("chainid") or 1), integer(row["nonce"])]
        fields += ([integer(row["gasprice"])] if kind == 1 else
                   [integer(row["maxpriorityfeepergas"]), integer(row["maxfeepergas"])])
        fields += [integer(row["gas"]), row["to"] or b"", integer(row["value"]),
                   row["input"] or b"", list(access_list)]
        prefix, parity = bytes([kind]), (row["v"], 0, 1)
    # The legacy SQL padding/v recovery is accepted only with an exact tx hash.
    for r in padded_integer_candidates(row["r"]):
        for s in padded_integer_candidates(row["s"]):
            for v in parity:
                signed = fields + [integer(v), integer(r), integer(s)]
                raw = prefix + rlp.encode(signed)
                if keccak(raw) == row["hash"]:
                    return raw if kind else signed
    raise ValueError("SQL transaction hash mismatch (including access-list order)")


def access_lists(rows):
    lists = collections.defaultdict(list)
    entries = {}
    for row in rows:
        key = (row["blocknumber"], row["transactionindex"])
        entry_key = key + (row["accesslistindex"],)
        if entry_key not in entries:
            if row["accesslistindex"] != len(lists[key]):
                raise ValueError("noncontiguous SQL access-list order")
            entries[entry_key] = [row["address"], []]
            lists[key].append(entries[entry_key])
        entry = entries[entry_key]
        if entry[0] != row["address"]:
            raise ValueError("access-list address changes within an entry")
        if row["storagekeys"] is not None:
            if len(row["storagekeys"]) != 32:
                raise ValueError("invalid SQL access-list storage key")
            entry[1].append(row["storagekeys"])
    return lists


def export(cursor, output, first, last):
    for start in range(first, last + 1, 1000):
        end = min(start + 999, last)
        cursor.execute("SELECT * FROM blocks WHERE number BETWEEN %s AND %s ORDER BY number", (start, end))
        blocks = cursor.fetchall()
        if [r["number"] for r in blocks] != list(range(start, end + 1)):
            raise ValueError(f"missing SQL blocks in {start}..{end}")
        grouped = []
        for table, order in (("transactions", "transactionindex"), ("uncles", "uncleposition")):
            cursor.execute(f"SELECT * FROM {table} WHERE blocknumber BETWEEN %s AND %s ORDER BY blocknumber,{order}", (start, end))
            group = collections.defaultdict(list)
            for row in cursor.fetchall():
                group[row["blocknumber"]].append(row)
            grouped.append(group)
        transactions, uncles = grouped
        access = {}
        if any(int.from_bytes(integer(tx["type"]), "big") for txs in transactions.values() for tx in txs):
            cursor.execute("SELECT * FROM transactions_accesslist WHERE blocknumber BETWEEN %s AND %s ORDER BY blocknumber,transactionindex,accesslistindex,id", (start, end))
            access = access_lists(cursor.fetchall())
        for block in blocks:
            number = block["number"]
            txs = transactions[number]
            if len(txs) != block["transactions"]:
                raise ValueError(f"missing transactions at {number}")
            if [tx["transactionindex"] for tx in txs] != list(range(len(txs))):
                raise ValueError(f"transaction ordering at {number}")
            uncle_headers = [header(row, True) for row in uncles[number]]
            if keccak(rlp.encode(uncle_headers)) != block["sha3uncles"]:
                raise ValueError(f"uncle list mismatch at {number}")
            output.write(rlp.encode([header(block), [transaction(tx, access.get((number, tx["transactionindex"]), ())) for tx in txs], uncle_headers]))
        print(f"exported {start}..{end}", file=sys.stderr)


@contextlib.contextmanager
def output_file(name):
    """Publish a completed, synced file; leave interrupted exports as .partial."""
    if name == "-":
        yield sys.stdout.buffer
        return
    final = Path(name)
    if os.path.lexists(final):
        raise FileExistsError(f"output already exists: {final}")
    partial = final.with_name(final.name + ".partial")
    with partial.open("xb") as output:
        yield output
        output.flush()
        os.fsync(output.fileno())
    # Atomic publication without replacing an existing destination. Both names
    # are in the same directory; this does not copy the potentially large file.
    os.link(partial, final)
    partial.unlink()
    fd = os.open(final.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db-utils", required=True, help="existing local db_utils.py")
    parser.add_argument("--first", type=int, default=0)
    parser.add_argument("--last", type=int, required=True)
    parser.add_argument("--output", required=True, help="final RLP path (written as PATH.partial until complete), or - for stdout")
    args = parser.parse_args()
    if args.first < 0 or args.last < args.first:
        parser.error("require 0 <= first <= last")
    db = runpy.run_path(args.db_utils)
    try:
        with output_file(args.output) as output:
            export(db["conn"].cursor(), output, args.first, args.last)
    finally:
        db["conn"].close()
        db["conn_thread"].close()


if __name__ == "__main__":
    main()
