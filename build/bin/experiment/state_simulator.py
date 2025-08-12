import socket
import os, binascii
import sys
import multiprocessing as mp
import subprocess
import json
import random

from web3 import Web3
from datetime import datetime
from os.path import exists
from multiprocessing.pool import ThreadPool as Pool

from db_utils import *

# simulator server IP address
SERVER_IP = "localhost"
SERVER_PORT = 8994

# simulator options
deleteDisk = True # delete disk when reset simulator or not
checkStateValidity = True # check state writes' correctness
saveResults = True # save results as a json file

# maximum byte length of response from the simulator
maxResponseLen = 4096

# open socket
client_socket = socket.socket(socket.AF_INET, socket.SOCK_STREAM)


state_roots = {}
state_roots["ethereum"] = {}
state_roots["ethereum"][1000000] = "0x0e066f3c2297a5cb300593052617d1bca5946f0caa0635fdb1b85ac7e5236f34"
state_roots["ethereum"][3000000] = "0x8e7ab0771fa333e1369fd48374010b8a21283a70690c6064fe2ecf091a1719ec"
state_roots["ethereum"][5000000] = "0x6092dfd6bcdd375764d8718c365ce0e8323034da3d3b0c6d72cf7304996b86ad"
state_roots["ethereum"][7000000] = "0x9bdd6dcc867f4d14df912ec1e70d095ce79bf2c57b5cbb2e782491e2cadf18c0"
state_roots["ethereum"][10000000] = "0x74477eaabece6bce00c346dc12275b2ed74ec9d6c758c4023c2040ba0e72e05d"
state_roots["trie-hashimoto"] = {}
state_roots["trie-hashimoto"][1000000] = "0x000f4240f72d6c8c63f906273b6a1ff8f8720e7a21eada189b91b18821c1bdaa"
state_roots["trie-hashimoto"][3000000] = "0x002dc6c0a72814d9db770a691f6887fd640d319f053f5175f80a889ff3d9721c"
state_roots["trie-hashimoto"][5000000] = "0x004c4b401496775dc4c35fbbb9795487263ccf8ca77b121432156de801a13489"
state_roots["trie-hashimoto"][7000000] = "0x006acfc0b01994385fa35e5298147e6c586929561d275e33746fe0abbca1fa5e"
state_roots["trie-hashimoto"][10000000] = "0x00989680f2bdcbc153f02763ce2944ed4027cd887031b36bbad2e8c1dbb5d13a"



#
# simulator APIs
#

# setDatabase simulator
def setDatabase(deleteDisk):
    doDeleteDisk = 0
    if deleteDisk:
        doDeleteDisk = 1
    cmd = str("setDatabase")
    cmd += str(",")
    cmd += str(doDeleteDisk)
    print("cmd:", cmd)
    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    print("setDatabase result:", result)
    return result

# setDbPath
def setDbPath(dbPath):
    cmd = str("setDbPath")
    cmd += str(",")
    cmd += str(dbPath)
    
    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("setDatabase result:", result)
    return result

# set simulation options
def setSimulationOptions(enableSnapshot, trieNodePrefixLen, loggingOpcodeStats):
    cmd = str("setSimulationOptions")
    cmd += str(",")
    cmd += str(int(enableSnapshot))
    cmd += str(",")
    cmd += str(trieNodePrefixLen)
    cmd += str(",")
    cmd += str(int(loggingOpcodeStats))
    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    print("setSimulationOptions result:", result)
    return result

def getSimulationTypeName():
    cmd = str("getSimulationTypeName")
    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    type_name = data.decode()
    # print("getSimulationTypeName type_name:", type_name)
    return type_name

# insert header
def insertHeader(blockNum):
    header = select_block_header(cursor, blockNum)
    # print(header)

    cmd = str("insertHeader")
    cmd += str(",")
    cmd += str(header['number'])
    cmd += str(",")
    cmd += str(header['timestamp'])
    # cmd += str(",")
    # cmd += str(header['transactions'])
    cmd += str(",")
    cmd += str(header['miner'].hex())
    cmd += str(",")
    cmd += str(header['difficulty'])
    # cmd += str(",")
    # cmd += str(header['totaldifficulty'])
    # cmd += str(",")
    # cmd += str(header['size'])
    cmd += str(",")
    cmd += str(header['gasused'])
    cmd += str(",")
    cmd += str(header['gaslimit'])
    cmd += str(",")
    cmd += str(header['extradata'].hex())
    # cmd += str(",")
    # cmd += str(header['hash'])
    cmd += str(",")
    cmd += str(header['parenthash'].hex())
    cmd += str(",")
    cmd += str(header['sha3uncles'].hex())
    cmd += str(",")
    cmd += str(header['stateroot'].hex())
    cmd += str(",")
    cmd += str(header['nonce'].hex())
    cmd += str(",")
    cmd += str(header['receiptsroot'].hex())
    cmd += str(",")
    cmd += str(header['transactionsroot'].hex())
    cmd += str(",")
    cmd += str(header['mixhash'].hex())
    cmd += str(",")
    if header['logsbloom'] == None:
        cmd += str(header['logsbloom'])
    else:
        cmd += str(header['logsbloom'].hex())
    cmd += str(",")
    cmd += str(header['basefee'])

    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("insertHeader result:", result)

# insert uncles
def insertUncles(blockNum):
    uncles = select_uncles(cursor, blockNum)
    # print("uncles at blocknum", blockNum, ":", uncles)

    cmd = str("insertUncles")
    cmd += str(",")
    cmd += str(blockNum)
    for uncle in uncles:
        cmd += str(",")
        cmd += str(uncle['miner'].hex())
        cmd += str(",")
        cmd += str(uncle['uncleheight'])

    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("insertUncles result:", result)
    return result

# insert all transactionArgs in block
def insertTransactionArgsList(blockNum):
    txs = select_txs(cursor, blockNum)
    # print("txs len:", len(txs))
    for tx in txs:
        insertTransactionArgs(tx)

# insert transaction args
def insertTransactionArgs(tx):
    # print(tx)
    cmd = str("insertTransactionArgs")
    cmd += str(",")
    cmd += str(tx['from'].hex())
    cmd += str(",")
    if tx['to'] == None:
        cmd += str(tx['to'])
    else:
        cmd += str(tx['to'].hex())
    cmd += str(",")
    cmd += str(tx['gas'])
    cmd += str(",")
    cmd += str(tx['gasprice'])
    cmd += str(",")
    cmd += str(tx['value'])
    cmd += str(",")
    cmd += str(tx['nonce'])
    cmd += str(",")
    cmd += str(tx['input'].hex())
    cmd += str(",")
    cmd += str(tx['maxfeepergas'])
    cmd += str(",")
    cmd += str(tx['maxpriorityfeepergas'])
    cmd += ",@" # this cmd can be very large, so insert special char to check the end

    client_socket.sendall(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("insertTransactionArgs result:", result)

# clear inserted txArgs list
def clearTransactionArgsList():
    cmd = str("clearTransactionArgsList")
    client_socket.sendall(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("clearTransactionArgsList result:", result)

def executeTransactionArgsList():
    cmd = str("executeTransactionArgsList")
    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("executeTransactionArgsList result:", result)
    return result

# TODO(jmlee): implement this
def insertTransactionAccessLists(blockNum):
    access_lists = select_txs_access_list(cursor, blockNum)
    # print("txs len:", len(txs))
    # cnt = 0
    for access_list in access_lists:
        insertTransactionAccessList(access_list)

        # cnt += 1
        # if cnt > 3:
        #     sys.exit()

def insertTransactionAccessList(access_list):
    cmd = str("insertTransactionAccessList")
    cmd += str(",")
    cmd += str(access_list['transactionindex'])
    cmd += str(",")
    cmd += str(access_list['address'].hex())
    if access_list['storagekeys'] != None:
        cmd += str(",")
        cmd += str(access_list['storagekeys'].hex())


    # print("insert access list")
    # print("  id:", access_list['id'])
    # print("  address:", access_list['address'].hex())
    # if access_list['storagekeys'] != None:
    #     print("  storagekeys:", access_list['storagekeys'].hex())

    
    client_socket.sendall(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("insertTransactionAccessList result:", result)
    return result


# TODO(jmlee): implement this
def insertTransactionAccessListsV2(blockNum):
    access_lists = select_txs_access_list(cursor, blockNum)
    # print("txs len:", len(txs))
    # cnt = 0

    current_index = 0
    current_txindex = -1
    current_address = ""
    storage_keys = []

    for access_list in access_lists:

        if current_txindex != access_list['transactionindex']:
            if current_txindex != -1:
                insertTransactionAccessListV2(current_txindex, current_address, storage_keys)
            current_txindex = access_list['transactionindex']
            current_index = 0
            current_address = ""
            storage_keys = []

        if current_index != access_list['accesslistindex']:
            insertTransactionAccessListV2(current_txindex, current_address, storage_keys)
            current_index += 1
            current_address = ""
            storage_keys = []

        if current_index == access_list['accesslistindex']:
            if access_list['storagekeys'] != None:
                storage_keys.append(access_list['storagekeys'])
            current_address = access_list['address']
    
    if current_address != "":
        insertTransactionAccessListV2(current_txindex, current_address, storage_keys)

    return


def insertTransactionAccessListV2(txindex, address, storage_keys):
    # print("\ninsertTransactionAccessListV2 executed")
    # print("  txindex:", txindex)
    # print("  addr:", address.hex())
    # print("  storage keys:", storage_keys)

    cmd = str("insertTransactionAccessListV2")
    cmd += str(",")
    cmd += str(txindex)
    cmd += str(",")
    cmd += str(address.hex())
    for storage_key in storage_keys:
        cmd += str(",")
        cmd += str(storage_key.hex())
    cmd += ",@" # this cmd can be very large, so insert special char to check the end

    client_socket.sendall(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("insertTransactionAccessListV2 result:", result)
    return result

# TODO(jmlee): implement this
# commit dirty states when finishing simulation
def commitDirtyStates():
    cmd = str("commitDirtyStates")
    client_socket.sendall(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("commitDirtyStates result:", result)
    return result

# set environment for EVM experiment
def setEnvForEVM(blockNum, stateRoot):
    cmd = str("setEnvForEVM")
    cmd += str(",")
    cmd += str(blockNum)
    cmd += str(",")
    cmd += str(stateRoot)
    
    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("setEnvForEVM result:", result)
    return result

def saveLevelDBStats():
    cmd = str("saveLevelDBStats")

    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("saveLevelDBStats result:", result)

def saveSimBlocks(fileName, blockNumToSave):
    cmd = str("saveSimBlocks")
    cmd += str(",")
    cmd += str(fileName)
    cmd += str(",")
    cmd += str(blockNumToSave)

    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("saveSimBlocks result:", result)

def loadSimBlocks(starBlockNum, endBlockNum, lastBlockNumToLoad):
    cmd = str("loadSimBlocks")
    cmd += str(",")
    cmd += str(starBlockNum)
    cmd += str(",")
    cmd += str(endBlockNum)
    cmd += str(",")
    cmd += str(lastBlockNumToLoad)

    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("loadSimBlocks result:", result)

# mimic fast sync
def benchmarkSync(stateRoot):
    cmd = str("benchmarkSync")
    cmd += str(",")
    cmd += str(stateRoot)
    
    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("benchmarkSync result:", result)
    return result

def inspectAndCopyStateByBlockNum(blockNum, copyStateHash, copyStateHashSnap, copyStatePath, copyStatePathSnap):
    myHeader = select_block_header(cursor, blockNum)
    wantedStateRoot = myHeader['stateroot'].hex()
    inspectAndCopyState(blockNum, wantedStateRoot, copyStateHash, copyStateHashSnap, copyStatePath, copyStatePathSnap)

def inspectAndCopyState(blockNum, stateRoot, copyStateHash, copyStateHashSnap, copyStatePath, copyStatePathSnap):
    cmd = str("inspectAndCopyState")
    cmd += str(",")
    cmd += str(blockNum)
    cmd += str(",")
    cmd += str(stateRoot)
    cmd += str(",")
    cmd += str(int(copyStateHash))
    cmd += str(",")
    cmd += str(int(copyStateHashSnap))
    cmd += str(",")
    cmd += str(int(copyStatePath))
    cmd += str(",")
    cmd += str(int(copyStatePathSnap))
    print("inspectAndCopyState() executed -> blockNum:", blockNum, "/ stateRoot:", stateRoot)
    
    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("inspectAndCopyState result:", result)
    return result

# convertKeyalues reinserts kv pairs with different kv pairs to check disk size diffs
def convertKeyalues():
    cmd = str("convertKeyalues")

    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()

# stop simulation
def stopSimulation():
    cmd = str("stopSimulation")

    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()

# call test function for develop
def test():
    cmd = str("test")

    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()

# -----------------------------------------------------------

# replay txs in Ethereum through EVM to simulate ethereum
def simulateEthereumEVM(startBlockNum, endBlockNum, lastKnownBlockNum, temp_result_save_inteval):
    print("run Ethereum simulation")

    # load previous results
    if startBlockNum != 0:
        if lastKnownBlockNum+1 < startBlockNum:
            print("ERROR: cannot meet this condition -> lastKnownBlockNum + 1 >= startBlockNum")
            print("  lastKnownBlockNum:", lastKnownBlockNum)
            print("  startBlockNum:", startBlockNum)
            sys.exit()
        
        print("load previous results")
        # TODO(jmlee): refactoring load state logic
        # insert previous SimBlocks
        # loadSimBlocks(0, lastKnownBlockNum, startBlockNum-1)
        lastKnownHeader = select_block_header(cursor, lastKnownBlockNum)
        setEnvForEVM(startBlockNum, lastKnownHeader['stateroot'].hex())
        # insert recent 256 block headers
        for blockNum in range(max(0, startBlockNum-300), startBlockNum):
            insertHeader(blockNum)

    # simulation result file name
    sim_blocks_file_name = "evm_simulation_result_Ethereum_" + str(0) + "_" + str(endBlockNum) + ".json"
    # temp_result_save_inteval = 500000

    startTime = datetime.now()
    tempStartTime = startTime
    loginterval = 1000

    # execute blocks
    for blockNum in range(startBlockNum, endBlockNum+1):
        # print("\nblock ->", blockNum)
        # show process
        if blockNum % loginterval == 0:
            print("execute block", blockNum, "( port:", SERVER_PORT, "/ mode:", getSimulationTypeName(), "/ block range:", startBlockNum, "~", endBlockNum, ")")
            currentTime = datetime.now()
            elapsedTime = currentTime-startTime
            tempElapsedTime = currentTime-tempStartTime
            tempStartTime = currentTime
            print("elapsed:", elapsedTime, "( total bps:", int((blockNum-startBlockNum)/elapsedTime.total_seconds()), 
                  "/ recent bps:", int(loginterval/tempElapsedTime.total_seconds()), ")")
            print()

        # execute block
        # print("for block", blockNum)
        insertHeader(blockNum)
        insertUncles(blockNum)
        insertTransactionArgsList(blockNum)
        insertTransactionAccessListsV2(blockNum)
        executeTransactionArgsList()

        # save intermediate results
        if saveResults and blockNum % temp_result_save_inteval == 0 and blockNum > lastKnownBlockNum and blockNum != endBlockNum:
            temp_file_name = "evm_simulation_result_Ethereum_" + str(0) + "_" + str(blockNum) + ".json"
            saveSimBlocks(temp_file_name, temp_result_save_inteval)
            saveLevelDBStats()

    # simulation finished
    if saveResults:
        saveSimBlocks(sim_blocks_file_name, temp_result_save_inteval)
        saveLevelDBStats()
        print("save result:", sim_blocks_file_name)

    print("finish Ethereum EVM simulation")
    print("elapsed time:", datetime.now()-startTime)

# execute random txs in Ethereum through EVM
def simulateEthereumEVMRandom(startBlockNum, endBlockNum, lastKnownBlockNum, temp_result_save_inteval, txPerBlock, totalAccountNum):
    print("run random Ethereum simulation")

    # simulation result file name
    sim_blocks_file_name = "evm_random_simulation_result_Ethereum_" + str(0) + "_" + str(endBlockNum) + ".json"
    # temp_result_save_inteval = 500000

    startTime = datetime.now()
    tempStartTime = startTime
    loginterval = 1000

    executedTxNum = 0
    fromAddrInt = 0
    fromAddr = fromAddrInt.to_bytes(20, 'big')
    activeAddrPercentage = 10

    # execute blocks
    for blockNum in range(startBlockNum, endBlockNum+1):
        # print("\nblock ->", blockNum)
        # show process
        if blockNum % loginterval == 0:
            print("execute block", blockNum, "( port:", SERVER_PORT, "/ mode:", getSimulationTypeName(), "/ block range:", startBlockNum, "~", endBlockNum, ")")
            currentTime = datetime.now()
            elapsedTime = currentTime-startTime
            tempElapsedTime = currentTime-tempStartTime
            tempStartTime = currentTime
            print("elapsed:", elapsedTime, "( total bps:", int((blockNum-startBlockNum)/elapsedTime.total_seconds()), 
                  "/ recent bps:", int(loginterval/tempElapsedTime.total_seconds()), ")")
            print()

        # execute block
        # print("for block", blockNum)
        insertHeader(blockNum)
        insertUncles(blockNum)

        tx = {}
        tx['from'] = fromAddr
        tx['gas'] = 21000
        tx['gasprice'] = 1
        tx['input'] = b''
        tx['maxfeepergas'] = None
        tx['maxpriorityfeepergas'] = None
        if blockNum != 0:
            for i in range(txPerBlock):
                executedTxNum += 1
                tx['value'] = executedTxNum
                tx['nonce'] = executedTxNum
                if executedTxNum < totalAccountNum:
                    tx['to'] = executedTxNum.to_bytes(20, 'big')
                elif executedTxNum >= totalAccountNum:
                    activeAddrNum = int(totalAccountNum * activeAddrPercentage / 100)
                    randomInt = random.randint(1, activeAddrNum)
                    tx['to'] = randomInt.to_bytes(20, 'big')
                insertTransactionArgs(tx)

        # insertTransactionAccessListsV2(blockNum)
        executeTransactionArgsList()

        # save intermediate results
        if saveResults and blockNum % temp_result_save_inteval == 0 and blockNum > lastKnownBlockNum and blockNum != endBlockNum:
            temp_file_name = "evm_simulation_result_Ethereum_" + str(0) + "_" + str(blockNum) + ".json"
            saveSimBlocks(temp_file_name, temp_result_save_inteval)
            saveLevelDBStats()

    # simulation finished
    if saveResults:
        saveSimBlocks(sim_blocks_file_name, temp_result_save_inteval)
        saveLevelDBStats()
        print("save result:", sim_blocks_file_name)

    print("finish Ethereum EVM simulation")
    print("elapsed time:", datetime.now()-startTime)

# generate random ethereum address
def generateRandomAddress():
    randHex = binascii.b2a_hex(os.urandom(20))
    return randHex.decode('utf-8')

def intToAddress(n: int) -> str:
    if n < 0 or n >= 2**160:
        raise ValueError("Integer out of range for Ethereum address (0 <= n < 2^160)")
    return '0x' + format(n, '040x')

def dropPageCaches():
    MY_SUDO_PW = 'FILL_PASSWORD'
    if MY_SUDO_PW == 'FILL_PASSWORD':
        print("ERROR: fill 'MY_SUDO_PW' first to drop page caches")
        sys.exit()
    command = f'echo {MY_SUDO_PW} | sudo -S ' + 'sh -c "echo 1 > /proc/sys/vm/drop_caches"'
    subprocess.call(command, shell=True)



if __name__ == "__main__":

    # blockheader = select_block_header(cursor, 15537392)
    # print(blockheader['mixhash'].hex())
    # blockheader = select_block_header(cursor, 15537393)
    # print(blockheader['mixhash'].hex())
    # blockheader = select_block_header(cursor, 15537394)
    # print(blockheader['mixhash'].hex())
    # blockheader = select_block_header(cursor, 15537395)
    # print(blockheader['mixhash'].hex())
    # blockheader = select_block_header(cursor, 15537396)
    # print(blockheader['mixhash'].hex())
    # blockheader = select_block_header(cursor, 15537397)
    # print(blockheader['mixhash'].hex())
    # sys.exit()


    # blockNum = 12250529
    # access_lists = select_txs_access_list(cursor, blockNum)
    # for access_list in access_lists:
    #     print(access_list)
    #     print("  index:", access_list['accesslistindex'])
    #     print("  address:", access_list['address'].hex())
    #     if access_list['storagekeys'] != None:
    #         print("  storageKeys:", access_list['storagekeys'].hex())
    #     print("\n")
    # sys.exit()


    print("start")
    startTime = datetime.now()

    # set threadpool for db querying
    pool = Pool(1)

    #
    # call APIs to simulate MPT
    #

    # set simulation options
    deleteDisk = False
    checkStateValidity = True
    saveResults = True
    # set simulation params
    startBlockNum = 0
    endBlockNum = 10000
    lastKnownBlockNum = 0 # try to load prev results (restore list file is needed)
    temp_result_save_inteval = 500000 # save simulation results periodically
    trieInspectIntervals = range(0, endBlockNum+1-1000000, 1000000)
    fromLevel = 0 # how many parent nodes to omit in Merkle proofs
    flushInterval = 1 # block flush interval (default: 1, at every block / but genesis block is always flushed)

    if len(sys.argv) > 1:
        if len(sys.argv) != 5:
            print("ERROR: wrong # of argv, you need to put")
            print("  SERVER_PORT of simulator")
            print("  startBlockNum, endBlockNum, lastKnownBlockNum")
            sys.exit()

        SERVER_PORT = int(sys.argv[1])
        startBlockNum = int(sys.argv[2])
        endBlockNum = int(sys.argv[3])
        lastKnownBlockNum = int(sys.argv[4])
        print("set new params")
        print("  SERVER_PORT:", SERVER_PORT)
        print("  startBlockNum:", startBlockNum)
        print("  endBlockNum:", endBlockNum)
        print("  lastKnownBlockNum:", lastKnownBlockNum)

    # connect to geth
    client_socket.connect((SERVER_IP, SERVER_PORT))



    # for development
    # test()
    # sys.exit()



    # run convertKeyalues()
    # setDatabase(False)
    # convertKeyalues()
    # sys.exit()



    # 
    # inspect and copy state
    # 

    # ethereum 50,000: 0x2dafcb133d1fb1b907fdbbb3d303145765b2cf7773611e35997764079e4297ec
    # ethereum 100,000: 0x209230089ff328b2d87b721c48dbede5fd163c3fae29920188a7118275ab2013
    # ethereum 0.01M: 0x4de830f589266773eae1a1caa88d75def3f3a321fbd9aeb89570a57c6e7f3dbb
    # ethereum 0.5M: 0xb2bcfa2ffe869085c84a976435f1581a7a0eb7af64bafcbbda710661016aa3ab
    # ethereum 1M: 0x0e066f3c2297a5cb300593052617d1bca5946f0caa0635fdb1b85ac7e5236f34
    # ethereum 3M: 0x8e7ab0771fa333e1369fd48374010b8a21283a70690c6064fe2ecf091a1719ec
    # ethereum 5M: 0x6092dfd6bcdd375764d8718c365ce0e8323034da3d3b0c6d72cf7304996b86ad
    # ethereum 5.58M: 0x25ab955eb900ba009ab336533ea209a4880d3fcab044abc893a611d6ba21257d

    # copyStateHash = True
    # copyStateHashSnap = False
    # copyStatePath = True
    # copyStatePathSnap = False

    # setDatabase(False)
    # inspectAndCopyStateByBlockNum(1000000, copyStateHash, copyStateHashSnap, copyStatePath, copyStatePathSnap)

    # setDbPath("/ethereum/th_plus/stateTries/trie-hasimoto/1000000_0x000f4240f72d6c8c63f906273b6a1ff8f8720e7a21eada189b91b18821c1bdaa_hash")
    # setDatabase(False)
    # inspectAndCopyState("0x000f4240f72d6c8c63f906273b6a1ff8f8720e7a21eada189b91b18821c1bdaa", copyStateHash, copyStateHashSnap, copyStatePath, copyStatePathSnap)

    # setDatabase(False)
    # inspectAndCopyStateByBlockNum(17034870, copyStateHash, copyStateHashSnap, copyStatePath, copyStatePathSnap)
    # inspectAndCopyState("0x00989680f2bdcbc153f02763ce2944ed4027cd887031b36bbad2e8c1dbb5d13a", copyStateHash, copyStateHashSnap, copyStatePath, copyStatePathSnap)
    
    # sys.exit()



    # 
    # sync simulation
    # 
    # dbPathPrefix = "/ethereum/th_plus/stateTries/"
    # protocol = "ethereum"
    # # protocol = "trie-hashimoto"
    # blockNum = 10000000
    # stateRootToSync = state_roots[protocol][blockNum]
    # stateScheme = "hash"
    # sorted = ""
    # # sorted = "_random"
    # # sorted = "_sorted"
    # dbPath = dbPathPrefix + protocol + "/" + str(blockNum) + "_" + stateRootToSync + "_" + stateScheme + sorted + "/"
    # print("dbPath to get trie nodes:", dbPath)
    # # jsonFilePath = 
    # # with open(file_path, 'r') as file:
    # #     data = json.load(file)
    
    # setDbPath(dbPath)
    # setDatabase(False)
    # benchmarkSync(stateRootToSync)
    
    # sys.exit()



    # 
    # run simulation
    # 
    # TODO(jmlee): call setSimulationOptions() function before setDatabase() call
    # setSimulationOptions(enableSnapshot=True, trieNodePrefixLen=6, loggingOpcodeStats=False)
    setDatabase(deleteDisk)
    simulateEthereumEVM(startBlockNum, endBlockNum, lastKnownBlockNum, temp_result_save_inteval)
    # simulateEthereumEVMRandom(startBlockNum, endBlockNum, lastKnownBlockNum, temp_result_save_inteval, 400, 40000000)
    commitDirtyStates()

    print("end")
    endTime = datetime.now()
    print("final elapsed time:", endTime-startTime)
