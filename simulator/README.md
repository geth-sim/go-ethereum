# How to run simulator

- at go-ethereum/simulator

for measuring leveldb stats, git clone https://github.com/geth-sim/goleveldb.git at the same directory as go-ethereum/ (set branch: benchmark), then

```shell
go run main.go <port-num>
```

- at go-ethereum/build/bin/experiment

```shell
python3 evm_simulator.py <port-num> <simulation-mode> <start-block-num> <end-block-num> <last-known-block-num> <delete-epoch> <inactivate-epoch> <inactivate-threshold>
```

# Simulator options

## at common/evm_sim_globals.go

- `SimulationMode`: select protocol (ethereum, ethanos, ethane)

- `EnableSnapshot`: use snapshot while executing transactions or not

- `EnableNodePrefixing`: forcely prefixing trie node hashes or not

- `PrefixLength`: length of trie node prefix

- `LoggingOpcodeStats`: measure opcodes num / time / gas cost or not

## at simulator/evmsim/ethereum.go

- `leveldbPathPrefix`, `leveldbPath`: path of leveldb

- `leveldbCache`: size of leveldb cache

- `trieCacheSize`: size of Geth's trie cache

- `snapshotCacheSize`: size of Geth's snapshot cache


