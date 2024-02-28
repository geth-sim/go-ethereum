from web3 import Web3
import socket
import os, binascii
import sys
import random, time
import json
from datetime import datetime
from os.path import exists
import pymysql.cursors
import csv
import multiprocessing as mp
from multiprocessing.pool import ThreadPool as Pool
import subprocess

#########################################
from eth.vm import opcode_values
from brkgen.program import Program
from brkgen.data_layout import DataLayout
from brkgen import codegen as cg

my_conn, child_conn = mp.Pipe()
# no tx.data input
# p_codegenerator = mp.Process(target=cg.genetic.generate, args=('byzantium', 'stats/eth_throughput_5171000_5588000.json', child_conn, 1))
# can select tx.data input
# p_codegenerator = mp.Process(target=cg.genetic2.generate, args=('byzantium', 'stats/eth_throughput_5171000_5588000.json', child_conn, 1))
# p_codegenerator.start()
#########################################

# ethereum tx data DB options
db_host = 'localhost'
db_user = 'db_user'
db_pass = 'db_pass' # fill in the MariaDB/MySQL password.
db_name = 'db_name' # block 0 ~ 1,000,000
db_port = 3306 # 3306: mariadb default
conn_mariadb = lambda host, port, user, password, database: pymysql.connect(host=host, port=port, user=user, password=password, database=database, cursorclass=pymysql.cursors.DictCursor)
conn = conn_mariadb(db_host, db_port, db_user, db_pass, db_name)
cursor = conn.cursor()
conn_thread = conn_mariadb(db_host, db_port, db_user, db_pass, db_name)
cursor_thread = conn_thread.cursor() # another cursor for async thread

# simulator server IP address
SERVER_IP = "localhost"
SERVER_PORT = 8994

# simulator options
deleteDisk = True # delete disk when reset simulator or not
haveRestoreList = True # have restore list or generate it dynamically
checkStateValidity = True # check state writes' correctness
saveResults = True # save results as a json file

# file paths
restoreListFilePath = "./restoreList/"
txErrLogFilePath = "./txErrLogs/"

# maximum byte length of response from the simulator
maxResponseLen = 4096
# infinite epoch for deletion & inactivation
infiniteEpoch = 100000000

# const for Ethane
emptyCodeHash = "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"
emptyRoot = "56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"

# open socket
client_socket = socket.socket(socket.AF_INET, socket.SOCK_STREAM)

# caching data from db
cache_address = {}
cache_slot = {}

# destructed addresses (needed to check Ethanos's state correctness)
deletedAddrs = {}

#
# mariadb getters
#

# read block header
def select_block_header(cursor, blocknumber):
    sql = "SELECT * FROM blocks WHERE `number`=%s;"
    cursor.execute(sql, (blocknumber,))
    result = cursor.fetchall()
    return result[0]

def select_blocks_header(cursor, startblocknumber, endblocknumber):
    sql = "SELECT `stateroot` FROM blocks WHERE `number`>=%s AND `number`<%s;"
    cursor.execute(sql, (startblocknumber, endblocknumber))
    result = cursor.fetchall()
    return result

# read uncles
def select_uncles(cursor, blocknumber):
    sql = "SELECT * FROM uncles WHERE `blocknumber`=%s;"
    cursor.execute(sql, (blocknumber,))
    result = cursor.fetchall()
    return result

# read tx hash of block [blocknumber]'s [txindex]^th tx
def select_tx_hash(cursor, blocknumber, txindex):
    sql = "SELECT * FROM transactions WHERE `blocknumber`=%s AND `transactionindex`=%s;"
    cursor.execute(sql, (blocknumber, txindex))
    result = cursor.fetchall()
    return result

def select_txs_hash(cursor, blocknumber):
    sql = "SELECT * FROM transactions WHERE `blocknumber`=%s;"
    cursor.execute(sql, (blocknumber, ))
    result = cursor.fetchall()
    return result

# read address of address_id
def select_address(cursor, addressid):
    # sql = "SELECT * FROM addresses WHERE `id`=%s;"
    # cursor.execute(sql, (addressid,))
    # result = cursor.fetchall()
    # return result[0]['address'].hex()
    if addressid in cache_address:
        return cache_address[addressid]
    sql = "SELECT * FROM addresses WHERE `id`=%s;"
    cursor.execute(sql, (addressid,))
    result = cursor.fetchall()
    cache_address[addressid] = result[0]['address'].hex()
    return cache_address[addressid]

# read slot of address_id
def select_slot(cursor, slotid):
    # sql = "SELECT * FROM slots WHERE `id`=%s;"
    # cursor.execute(sql, (slotid,))
    # result = cursor.fetchall()
    # return result[0]['slot'].hex()
    if slotid in cache_slot:
        return cache_slot[slotid]
    sql = "SELECT * FROM slots WHERE `id`=%s;"
    cursor.execute(sql, (slotid,))
    result = cursor.fetchall()
    cache_slot[slotid] = result[0]['slot'].hex()
    return cache_slot[slotid]

# read txs in this block from DB
def select_txs(cursor, blocknumber):
    sql = "SELECT * FROM `transactions` WHERE `blocknumber`=%s;"
    cursor.execute(sql, (blocknumber,))
    result = cursor.fetchall()
    return result

# read accounts r/w list in this block from DB
def select_account_read_write_list(cursor, blocknumber):
    sql = "SELECT * FROM `states` WHERE `blocknumber`=%s;"
    cursor.execute(sql, (blocknumber,))
    result = cursor.fetchall()
    return result

def select_account_read_write_lists(cursor, startblocknumber, endblocknumber):
    sql = "SELECT * FROM `states` WHERE `blocknumber`>=%s AND `blocknumber`<%s;"
    cursor.execute(sql, (startblocknumber, endblocknumber))
    result = cursor.fetchall()
    return result

# read storage trie slots r/w list in this block from DB
def select_slot_write_list(cursor, stateid):
    sql = "SELECT * FROM `slotlogs` WHERE `stateid`=%s;"
    cursor.execute(sql, (stateid,))
    result = cursor.fetchall()
    return result

def select_slot_write_lists(cursor, startstateid, endstateid):
    sql = "SELECT * FROM `slotlogs` WHERE `stateid`>=%s AND `stateid`<%s;"
    cursor.execute(sql, (startstateid, endstateid))
    result = cursor.fetchall()
    slots = {}
    for i in result:
        if i['stateid'] not in slots:
            slots[i['stateid']] = []
        slots[i['stateid']].append(i)
    return slots

# read state & storage r/w list
def select_state_and_storage_list(cursor, startblocknumber, endblocknumber):
    # print("select_state_and_storage_list() start")
    
    # get state r/w list
    rwList = select_account_read_write_lists(cursor, startblocknumber, endblocknumber)
    
    # get storage write list
    if len(rwList) > 0:
        slotList = select_slot_write_lists(cursor, rwList[0]['id'], rwList[-1]['id']+1)
    else:
        slotList = []

    # return all lists
    # print("select_state_and_storage_list() end -> rwList len:", len(rwList), "/ slotList len:", len(slotList))
    return rwList, slotList

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

# select simulation mode (0: Geth, 1: Ethane, 2: Ethanos)
def switchSimulationMode(mode):
    cmd = str("switchSimulationMode")
    cmd += str(",")
    cmd += str(mode)
    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    print("switchSimulationModeResult result:", result)
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

# set Ethanos's system parameters
def setEthanosParams(sweepEpoch, fromLevel):
    cmd = str("setEthanosParams")
    cmd += str(",")
    cmd += str(sweepEpoch)
    cmd += str(",")
    cmd += str(fromLevel)
    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("setEthanosParams result:", result)
    return result

# set Ethane's system parameters
def setEthaneParams(deleteEpoch, inactivateEpoch, inactivateCriterion, fromLevel):
    cmd = str("setEthaneParams")
    cmd += str(",")
    cmd += str(deleteEpoch)
    cmd += str(",")
    cmd += str(inactivateEpoch)
    cmd += str(",")
    cmd += str(inactivateCriterion)
    cmd += str(",")
    cmd += str(fromLevel)
    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("setEthaneParams result:", result)
    return result

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

# insert all transactions in block
def insertTransactions(blockNum):
    txs = select_txs(cursor, blockNum)
    for tx in txs:
        insertTransaction(tx)

# TODO: insert transaction
def insertTransaction(tx):
    cmd = str("insertTransaction")
    cmd += str(",")
    cmd += str(tx['hash'].hex())
    cmd += str(",")
    cmd += str(tx['blocknumber'])
    cmd += str(",")
    cmd += str(tx['from'].hex())
    cmd += str(",")
    cmd += str(tx['to'].hex())
    cmd += str(",")
    cmd += str(tx['gas'])
    cmd += str(",")
    cmd += str(tx['gasprice'])
    cmd += str(",")
    cmd += str(tx['input'].hex())
    cmd += str(",")
    cmd += str(tx['nonce'])
    cmd += str(",")
    cmd += str(tx['transactionindex'])
    cmd += str(",")
    cmd += str(tx['value'])
    cmd += str(",")
    cmd += str(tx['v'].hex())
    cmd += str(",")
    cmd += str(tx['r'].hex())
    cmd += str(",")
    cmd += str(tx['s'].hex())
    cmd += str(",")
    cmd += str(tx['type'].hex())
    cmd += str(",")
    cmd += str(tx['maxfeepergas'])
    cmd += str(",")
    cmd += str(tx['maxpriorityfeepergas'])
    # cmd += str(",")
    # cmd += str(tx['class']) # always None, ignore this

    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("insertTransaction result:", result)

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

# insert addresses to restore
def insertRestoreAddressList(addrs):

    # sort addrs
    addrs = sorted(addrs, key=lambda x: int(x, 16))

    cmd = str("insertRestoreAddressList")    
    for addr in addrs:
        cmd += str(",")
        cmd += str(addr)
    cmd += ",@" # this cmd can be very large, so insert special char to check the end

    client_socket.sendall(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("insertRestoreAddressList result:", result)

# insert addresses to read/write
def insertAccessList(addrs):

    # sort addrs
    addrs = sorted(addrs, key=lambda x: int(x, 16))

    cmd = str("insertAccessList")    
    for addr in addrs:
        cmd += str(",")
        cmd += str(addr)
    cmd += ",@" # this cmd can be very large, so insert special char to check the end

    client_socket.sendall(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("insertAccessList result:", result)

def executeTransactionArgsList():
    cmd = str("executeTransactionArgsList")
    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("executeTransactionArgsList result:", result)
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

# check tx execution result
def checkTxExecutionResult(nonce, balance, root, codeHash, addr):
    cmd = str("checkTxExecutionResult")
    cmd += str(",")
    cmd += str(nonce)
    cmd += str(",")
    cmd += str(balance)
    cmd += str(",")
    cmd += str(root)
    cmd += str(",")
    cmd += str(codeHash)
    cmd += str(",")
    cmd += str(addr)

    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("checkTxExecutionResult result:", result)
    if result == "correct":
        return True
    else:
        return False

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

# for Ethane
def mergeSimBlocks(lastBlockNum, saveInterval, deleteEpoch, inactivateEpoch, inactivateCriterion, fromLevel):

    switchSimulationMode(1) # 1: Ethane mode
    setEthaneParams(deleteEpoch, inactivateEpoch, inactivateCriterion, fromLevel)

    cmd = str("mergeSimBlocks")
    cmd += str(",")
    cmd += str(lastBlockNum)
    cmd += str(",")
    cmd += str(saveInterval)

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

def benchmarkSync(stateRoot):
    cmd = str("benchmarkSync")
    cmd += str(",")
    cmd += str(stateRoot)
    
    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("benchmarkSync result:", result)
    return result

def inspectAndCopyState(stateRoot, copyStateHash, copyStateHashSnap, copyStatePath, copyStatePathSnap):
    cmd = str("inspectAndCopyState")
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
    
    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("inspectAndCopyState result:", result)
    return result

# stop simulation
def stopSimulation():
    cmd = str("stopSimulation")

    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()

# -----------------------------------------------------------

# replay txs in Ethereum through EVM to simulate Ethanos
def simulateEthereumEVM(startBlockNum, endBlockNum, lastKnownBlockNum, temp_result_save_inteval):
    print("run Ethereum simulation")

    # set Ethereum mode
    switchSimulationMode(0) # 0: Ethereum mode

    # load previous results
    if startBlockNum != 0:
        if lastKnownBlockNum+1 < startBlockNum:
            print("ERROR: cannot meet this condition -> lastKnownBlockNum + 1 >= startBlockNum")
            print("  lastKnownBlockNum:", lastKnownBlockNum)
            print("  startBlockNum:", startBlockNum)
            sys.exit()
        
        print("load previous results")
        # insert previous SimBlocks
        loadSimBlocks(0, lastKnownBlockNum, startBlockNum-1)
        # insert recent 256 block headers
        for blockNum in range(max(0, startBlockNum-300), startBlockNum):
            insertHeader(blockNum)

    # simulation result file name
    sim_blocks_file_name = "evm_simulation_result_Ethereum_" + str(0) + "_" + str(endBlockNum) + ".json"
    # temp_result_save_inteval = 500000

    startTime = datetime.now()
    tempStartTime = startTime
    loginterval = 1000
    batchsize = 1000 # db query batch size

    # TODO(jmlee): do not query r/w lists when checkStateValidity is False
    rwList = select_account_read_write_lists(cursor_thread, startBlockNum, startBlockNum+batchsize)
    rw_list_index = 0

    # execute blocks
    for blockNum in range(startBlockNum, endBlockNum+1):
        # print("\nblock ->", blockNum)
        # show process
        if blockNum % loginterval == 0:
            print("execute block", blockNum, "( Ethereum:", startBlockNum, "~", endBlockNum, ")")
            currentTime = datetime.now()
            elapsedTime = currentTime-startTime
            tempElapsedTime = currentTime-tempStartTime
            tempStartTime = currentTime
            print("elapsed:", elapsedTime, "( total bps:", int((blockNum-startBlockNum)/elapsedTime.total_seconds()), 
                  "/ recent bps:", int(loginterval/tempElapsedTime.total_seconds()), ")")
            print()

        # get read/write list from DB asyncly
        if (blockNum-startBlockNum)%batchsize == 0:
            if blockNum != startBlockNum:
                rwList = queryResult.get()
                rw_list_index = 0
                # print("receive query for block:", rwList[0]['blocknumber'], "~", rwList[-1]['blocknumber'])
            queryResult = pool.apply_async(select_account_read_write_lists, (cursor_thread, blockNum+batchsize, blockNum+batchsize*2,))

        # state diffs in this block: final_writes[addr] = item
        final_writes = {}
        while checkStateValidity:
            # get rw item in this block
            if rw_list_index == len(rwList):
                break
            item = rwList[rw_list_index]

            if item['blocknumber'] != blockNum:
                # go to next block
                break
            else:
                # manage address activeness
                # if item['blocknumber'] != 0:
                #     print("  rwlist of block", item['blocknumber'])
                
                addrId = item['address_id']
                addr = select_address(cursor, addrId)
                addr = '0x' + addr

                if item['type'] % 2 == 1:
                    # account write
                    final_writes[addr] = item
                    if item['type'] == 63:
                        # account delete
                        final_writes[addr] = None

                rw_list_index += 1

        # execute block
        insertHeader(blockNum)
        insertUncles(blockNum)
        insertTransactionArgsList(blockNum)
        executeTransactionArgsList()

        # save intermediate results
        if saveResults and blockNum % temp_result_save_inteval == 0 and blockNum > lastKnownBlockNum and blockNum != endBlockNum:
            temp_file_name = "evm_simulation_result_Ethereum_" + str(0) + "_" + str(blockNum) + ".json"
            saveSimBlocks(temp_file_name, temp_result_save_inteval)

        # compare tx result with write results
        if checkStateValidity:
            for addr, item in final_writes.items():
                isCorrect = False
                if item == None:
                    # this account does not exist
                    isCorrect = checkTxExecutionResult(0, 0, "0x00", "0x00", addr)
                else:
                    nonce = item['nonce']
                    balance = item['balance']
                    codeHash = emptyCodeHash
                    storageRoot = emptyRoot
                    if item['codehash'] != None:
                        codeHash = item['codehash'].hex()
                    if item['storageroot'] != None:
                        storageRoot = item['storageroot'].hex()
                    isCorrect = checkTxExecutionResult(nonce, balance, storageRoot, codeHash, addr)

                if not isCorrect:
                    with open(txErrLogFilePath + 'tx_execution_fail_log_Ethereum.txt', 'a') as file:
                        file.write("at block " + str(blockNum) + '\n')
                        file.write("addr: " + str(addr) + '\n')
                        file.write("expected account state\n")
                        file.write("  nonce: " + str(nonce) + '\n')
                        file.write("  balance: " + str(balance) + '\n')
                        file.write("  codeHash: " + str(codeHash) + '\n')
                        file.write("  storageRoot: " + str(storageRoot) + '\n')
                        file.write("\n")

    # simulation finished
    if saveResults:
        saveSimBlocks(sim_blocks_file_name, temp_result_save_inteval)
        print("save result:", sim_blocks_file_name)

    print("finish Ethereum EVM simulation")
    print("elapsed time:", datetime.now()-startTime)

# generate random ethereum address
def generateRandomAddress():
    randHex = binascii.b2a_hex(os.urandom(20))
    return randHex.decode('utf-8')

def dropPageCaches():
    MY_SUDO_PW = 'FILL_PASSWORD'
    if MY_SUDO_PW == 'FILL_PASSWORD':
        print("ERROR: fill 'MY_SUDO_PW' first to drop page caches")
        sys.exit()
    command = f'echo {MY_SUDO_PW} | sudo -S ' + 'sh -c "echo 1 > /proc/sys/vm/drop_caches"'
    subprocess.call(command, shell=True)

# tx.data is not bytecode
#   -> https://www.rareskills.io/post/ethereum-contract-creation-code
def generateAttackPayload(roundNum):

    program: Program
    datalayout: DataLayout
    program = cg.balance_spam.generate(10)
    datalayout = program.data_layout

    for segment in datalayout:
        if segment.opcode == opcode_values.BALANCE:
            # Fill in the values for the instruction.
            addr = generateRandomAddress()
            segment.fill(int(addr, base=16))
        elif segment.opcode == opcode_values.SLOAD:
            pass
            # hash = "01234" # TODO(jmlee)
            # segment.fill(int(hash))

    bytecode = program.to_bytes()
    txdata = datalayout.implement()
    print("bytecode len:", len(bytecode))
    # brkgen.utils.dump_bytecode(bytecode, f'data/bytecode_{roundNum}.txt', human_readable=True)
    # return bytecode, b''
    return bytecode, txdata



    # these are just sample values, implement this correctly later

    # from bytes to str: use bytes.fromhex(SOME_HEX_STR)
    # from str to bytes: use SOME_BYTES.hex()

    # ex. for CA address: 0xCde4DE4d3baa9f2CB0253DE1b86271152fBf7864
    deploytxdata = '60606040526040516102cd3803806102cd8339016040526060805160600190602001505b806001600050908051906020019082805482825590600052602060002090601f016020900481019282156071579182015b8281111560705782518260005055916020019190600101906054565b5b50905060989190607c565b8082111560945760008181506000905550600101607c565b5090565b50505b50610222806100ab6000396000f30060606040526000357c01000000000000000000000000000000000000000000000000000000009004806341c0e1b51461004f578063cfae32171461005c578063f1eae25c146100d55761004d565b005b61005a600450610110565b005b6100676004506101a4565b60405180806020018281038252838181518152602001915080519060200190808383829060006004602084601f0104600302600f01f150905090810190601f1680156100c75780820380516001836020036101000a031916815260200191505b509250505060405180910390f35b6100e06004506100e2565b005b33600060006101000a81548173ffffffffffffffffffffffffffffffffffffffff021916908302179055505b565b600060009054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff1614156101a157600060009054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff16ff5b5b565b60206040519081016040528060008152602001506001600050805480601f0160208091040260200160405190810160405280929190818152602001828054801561021357820191906000526020600020905b8154815290600101906020018083116101f657829003601f168201915b5050505050905061021f565b9056'
    bytecodehex = '60606040526000357c01000000000000000000000000000000000000000000000000000000009004806341c0e1b51461004f578063cfae32171461005c578063f1eae25c146100d55761004d565b005b61005a600450610110565b005b6100676004506101a4565b60405180806020018281038252838181518152602001915080519060200190808383829060006004602084601f0104600302600f01f150905090810190601f1680156100c75780820380516001836020036101000a031916815260200191505b509250505060405180910390f35b6100e06004506100e2565b005b33600060006101000a81548173ffffffffffffffffffffffffffffffffffffffff021916908302179055505b565b600060009054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff1614156101a157600060009054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff16ff5b5b565b60206040519081016040528060008152602001506001600050805480601f0160208091040260200160405190810160405280929190818152602001828054801561021357820191906000526020600020905b8154815290600101906020018083116101f657829003601f168201915b5050505050905061021f565b9056'

    bytecode = bytes.fromhex(bytecodehex)    
    calltxdata = 'cfae3217'

    txdata = bytes.fromhex(calltxdata)
    print("txdata:", txdata)
    return bytecode, txdata

def simulateDoSAttack(attackerAddress, attackerBalance, contractAddr, bytecode):
    cmd = str("simulateDoSAttack")
    cmd += str(",")
    cmd += str(attackerAddress)
    cmd += str(",")
    cmd += str(attackerBalance)
    cmd += str(",")
    cmd += str(contractAddr)
    cmd += str(",")
    cmd += str(bytecode)
    cmd += ",@" # this cmd can be very large, so insert special char to check the end

    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    attackResults = list(map(int, data.decode().split(',')))
    print("simulateDoSAttack result -> attackResults:", attackResults)
    return attackResults

def setStateRootAndTargetBlockNum(starBlockNum, endBlockNum, lastBlockNumToLoad, targetBlockNum):
    cmd = str("setStateRootAndTargetBlockNum")
    cmd += str(",")
    cmd += str(starBlockNum)
    cmd += str(",")
    cmd += str(endBlockNum)
    cmd += str(",")
    cmd += str(lastBlockNumToLoad)
    cmd += str(",")
    cmd += str(targetBlockNum)

    client_socket.send(cmd.encode())
    data = client_socket.recv(1024)
    result = data.decode()
    # print("setStateRootAndTargetBlockNum result:", result)

# TODO(jmlee): execute DoS attack contracts
def simulateEthereumDoSAttack(lastKnownBlockNum, targetBlockNum):
    print("run Ethereum DoS attack")

    # set Ethereum mode
    switchSimulationMode(0) # 0: Ethereum mode

    # set simulation options for DoS attack
    setSimulationOptions(False, 0, False)

    # set current state root and target block number (= decide EVM version to run)
    if targetBlockNum != 0:
        print("set state root and target block num")
        if targetBlockNum <= lastKnownBlockNum:
            # target block is known, so set to its state
            setStateRootAndTargetBlockNum(0, lastKnownBlockNum, targetBlockNum, targetBlockNum)
        else:
            # target block is unknown, so just set to known latest state (lastKnownBlockNum)
            setStateRootAndTargetBlockNum(0, lastKnownBlockNum, lastKnownBlockNum, targetBlockNum)
        # insert recent 256 block headers
        for blockNum in range(max(0, targetBlockNum-300), targetBlockNum+1):
            insertHeader(blockNum)

    # set attacker's and attack contract's accounts
    attackerAddress = "8b0725a76aff1bdcd64a005b44a736096221ccf3" # a random address that has never appeared before
    attackerBalance = 10000000*10**18 # 10M ETH
    contractAddr = "9e2e9e69723ebb0566d8741056acb1cb894d850d" # a random address that has never appeared before

    # set call tx
    callTx = dict()
    callTx['from'] = bytes.fromhex(attackerAddress)
    callTx['to'] = bytes.fromhex(contractAddr)
    callTx['gas'] = 30000000 # large enough tx gas limit for attack
    # callTx['gasprice'] = 0
    callTx['gasprice'] = None
    callTx['value'] = 0
    callTx['nonce'] = 0 # TODO(jmlee): wrong nonce value does not block tx execution, but correct this later
    # callTx['maxfeepergas'] = None
    callTx['maxfeepergas'] = 4236941740
    callTx['maxpriorityfeepergas'] = None

    # run rounds to execute attack contract
    totalRoundNum = 10000000000
    startTime = datetime.now()
    for roundNum in range(totalRoundNum):
        print("\n***start round", roundNum)

        #
        # generate attack contract's bytecode and tx.data as args
        #
        print("***gen attack contract and tx data")
        bytecode, txdata = generateAttackPayload(roundNum)

        #
        # set call tx's input data
        #
        print("***set call tx")
        callTx['input'] = txdata

        #
        # clear caches before executing contract
        # 
        dropPageCaches() # drop page caches
        setDatabase(False) # reopen geth's database such as LevelDB

        #
        # execute attack tx and get execution time
        #
        print("***execute call tx")
        # insertTransactionArgsList(targetBlockNum) # insert normal txs before attack tx
        insertTransactionArgs(callTx)
        attackResults = simulateDoSAttack(attackerAddress, attackerBalance, contractAddr, bytecode.hex())
        print("  # of executed opcodes:", attackResults[0])
        print("  opcode execution time:", f'{attackResults[1]:,}', "ns")
        print("  execution time per opcode:", f'{int(attackResults[1]/attackResults[0]):,}', "ns")
        print("  tx execution time:", f'{attackResults[2]:,}', "ns")
        print("  opcode gas cost:", attackResults[3], "gas")
        print("  tx gas cost:", attackResults[4], "gas")

        # 
        # TODO(jmlee): give fitness value to Genetic Algorithm process
        #
        fitness: float = attackResults[4] / (attackResults[1]/1e9 + 1e-12) # gas/sec
        print(f'  FITNESS: {fitness}')
        my_conn.send(fitness)

    # simulation finished
    print("\nfinish Ethereum DoS attack simulation V2")
    print("elapsed time:", datetime.now()-startTime)
    sys.exit()

# TODO(jmlee): execute read attack contracts
def simulateEthereumReadAttack(lastKnownBlockNum, targetBlockNum, BlockNumToRun):
    print("run Ethereum read attack")

    # set Ethereum mode
    switchSimulationMode(0) # 0: Ethereum mode

    # set simulation options for DoS attack
    setSimulationOptions(False, 0, False)

    # set current state root and target block number (= decide EVM version to run)
    if targetBlockNum != 0:
        print("set state root and target block num")
        if targetBlockNum <= lastKnownBlockNum:
            # target block is known, so set to its state
            setStateRootAndTargetBlockNum(0, lastKnownBlockNum, targetBlockNum, targetBlockNum)
        else:
            # target block is unknown, so just set to known latest state (lastKnownBlockNum)
            setStateRootAndTargetBlockNum(0, lastKnownBlockNum, lastKnownBlockNum, targetBlockNum)
        # insert recent 256 block headers
        for blockNum in range(max(0, targetBlockNum-300), targetBlockNum+1):
            insertHeader(blockNum)

    # set attacker's and attack contract's accounts
    attackerAddress = "8b0725a76aff1bdcd64a005b44a736096221ccf3" # a random address that has never appeared before
    attackerBalance = 10000000*10**18 # 10M ETH
    contractAddr = "9e2e9e69723ebb0566d8741056acb1cb894d850d" # a random address that has never appeared before

    # set call tx
    callTx = dict()
    callTx['from'] = bytes.fromhex(attackerAddress)
    callTx['to'] = bytes.fromhex(contractAddr)
    callTx['gas'] = 30000000 # large enough tx gas limit for attack
    # callTx['gasprice'] = 0
    callTx['gasprice'] = None
    callTx['value'] = 0
    callTx['nonce'] = 0 # TODO(jmlee): wrong nonce value does not block tx execution, but correct this later
    # callTx['maxfeepergas'] = None
    callTx['maxfeepergas'] = 4236941740
    callTx['maxpriorityfeepergas'] = None

    # run rounds to execute attack contract
    totalRoundNum = 10000000000
    startTime = datetime.now()
    for roundNum in range(totalRoundNum):
        print("\n***start round", roundNum)

        #
        # generate attack contract's bytecode and tx.data as args
        #
        print("***gen attack contract and tx data")
        bytecode, txdata = generateAttackPayload(roundNum)

        #
        # set call tx's input data
        #
        print("***set call tx")
        callTx['input'] = txdata

        #
        # clear caches before executing contract
        # 
        dropPageCaches() # drop page caches
        setDatabase(False) # reopen geth's database such as LevelDB

        #
        # execute attack tx and get execution time
        #
        print("***execute call tx")
        # insertTransactionArgsList(targetBlockNum) # insert normal txs before attack tx
        insertTransactionArgs(callTx)
        attackResults = simulateDoSAttack(attackerAddress, attackerBalance, contractAddr, bytecode.hex())
        print("  # of executed opcodes:", attackResults[0])
        print("  opcode execution time:", f'{attackResults[1]:,}', "ns")
        print("  execution time per opcode:", f'{int(attackResults[1]/attackResults[0]):,}', "ns")
        print("  tx execution time:", f'{attackResults[2]:,}', "ns")
        print("  opcode gas cost:", attackResults[3], "gas")
        print("  tx gas cost:", attackResults[4], "gas")

        # 
        # TODO(jmlee): give fitness value to Genetic Algorithm process
        #
        fitness: float = attackResults[4] / (attackResults[1]/1e9 + 1e-12) # gas/sec
        print(f'  FITNESS: {fitness}')
        my_conn.send(fitness)

    # simulation finished
    print("\nfinish Ethereum read attack simulation V2")
    print("elapsed time:", datetime.now()-startTime)
    sys.exit()

# replay txs in Ethereum through EVM to simulate Ethanos
def simulateEthanosEVM(startBlockNum, endBlockNum, sweepEpoch, fromLevel, temp_result_save_inteval):
    print("run Ethanos simulation")

    # set Ethanos's options
    switchSimulationMode(2) # 2: Ethanos mode
    setEthanosParams(sweepEpoch, fromLevel)
    # setFlushInterval(flushInterval)

    # load previous results
    if startBlockNum != 0:
        if lastKnownBlockNum+1 < startBlockNum:
            print("ERROR: cannot meet this condition -> lastKnownBlockNum + 1 >= startBlockNum")
            print("  lastKnownBlockNum:", lastKnownBlockNum)
            print("  startBlockNum:", startBlockNum)
            sys.exit()
        
        print("load previous results")
        # insert previous SimBlocks
        loadSimBlocks(0, lastKnownBlockNum, startBlockNum-1)
        # insert recent 256 block headers
        for blockNum in range(max(0, startBlockNum-300), startBlockNum):
            insertHeader(blockNum)

    # ready for restore list
    active_addrs = {}
    cached_addrs = {}
    inactive_addrs = {}
    restore_list = {}
    if haveRestoreList:
        last_block_num_of_restore_list = 10000000
        if last_block_num_of_restore_list < endBlockNum:
            print("ERROR: last_block_num_of_restore_list is less than endBlockNum")
            print("  last_block_num_of_restore_list:", last_block_num_of_restore_list)
            print("  endBlockNum:", endBlockNum)
            sys.exit()

        restore_list_file_name = "restore_list_Ethanos_" + str(0) + "_" + str(last_block_num_of_restore_list) + "_" + str(sweepEpoch) + ".json"
        with open(restoreListFilePath+restore_list_file_name, 'r') as json_file:
            restore_list = json.load(json_file)
            print("finished loading restore list ->", restore_list_file_name)

    # simulation result file name
    sim_blocks_file_name = "evm_simulation_result_Ethanos_" + str(0) + "_" + str(endBlockNum) + "_" + str(sweepEpoch) + ".json"
    # temp_result_save_inteval = 500000

    startTime = datetime.now()
    tempStartTime = startTime
    loginterval = 1000
    batchsize = 1000 # db query batch size

    rwList = select_account_read_write_lists(cursor_thread, startBlockNum, startBlockNum+batchsize)
    rw_list_index = 0

    # execute blocks
    for blockNum in range(startBlockNum, endBlockNum+1):
        # print("\nblock ->", blockNum)
        # show process
        if blockNum % loginterval == 0:
            print("execute block", blockNum, "( Ethanos:", startBlockNum, "~", endBlockNum, "/ TH_I:", sweepEpoch, ")")
            currentTime = datetime.now()
            elapsedTime = currentTime-startTime
            tempElapsedTime = currentTime-tempStartTime
            tempStartTime = currentTime
            print("elapsed:", elapsedTime, "( total bps:", int((blockNum-startBlockNum)/elapsedTime.total_seconds()), 
                  "/ recent bps:", int(loginterval/tempElapsedTime.total_seconds()), ")")
            print()

        # get read/write list from DB asyncly
        if (blockNum-startBlockNum)%batchsize == 0:
            if blockNum != startBlockNum:
                rwList = queryResult.get()
                rw_list_index = 0
                # print("receive query for block:", rwList[0]['blocknumber'], "~", rwList[-1]['blocknumber'])
            queryResult = pool.apply_async(select_account_read_write_lists, (cursor_thread, blockNum+batchsize, blockNum+batchsize*2,))

        # state diffs in this block: final_writes[addr] = item
        final_writes = {}
        # check which address to restore
        restore_addrs = []
        while not haveRestoreList or checkStateValidity:
            # get rw item in this block
            if rw_list_index == len(rwList):
                break
            item = rwList[rw_list_index]

            if item['blocknumber'] != blockNum:
                # go to next block
                break
            else:
                # manage address activeness
                # if item['blocknumber'] != 0:
                #     print("  rwlist of block", item['blocknumber'])
                
                addrId = item['address_id']
                addr = select_address(cursor, addrId)
                addr = '0x' + addr

                if item['type'] % 2 == 1:
                    # account write

                    if checkStateValidity:
                        final_writes[addr] = item

                    if not haveRestoreList:
                        active_addrs[addr] = blockNum
                        if addr in cached_addrs:
                            del cached_addrs[addr]
                        elif addr in inactive_addrs:
                            restore_addrs.append(addr)
                            del inactive_addrs[addr]
                    
                    if item['type'] == 63:
                        # account delete
                        # TODO: Ethanos에선 바로 account를 지우지 말고 restored flag만 true로 해놓고 account를 남겨놔야 복구 되었음 정도는 보여줘야할듯
                        # codeHash나 Root는 0x0000 이런 걸로 밀어놔서 복구 시도 조차 못하게 해도 좋을 것 같고
                        # -> 근데 실험에서는 어차피 지우기 전에 복구할 거고 위와 같은 점을 악용하거나 하진 않을거니까 그냥 write로 봐도 무방할듯
                        # 대신 더이상 존재하지 않는 주소로 봐야할 것 같음
                        if checkStateValidity:
                            final_writes[addr] = None

                        if not haveRestoreList:
                            del active_addrs[addr]

                else:
                    # account read
                    if not haveRestoreList:
                        if addr in active_addrs:
                            # not to search large inactive_addrs
                            pass
                        elif addr in cached_addrs:
                            # not to search large inactive_addrs
                            pass
                        elif addr in inactive_addrs:
                            restore_addrs.append(addr)
                            del inactive_addrs[addr]
                            active_addrs[addr] = blockNum
                        else:
                            # new address
                            pass

                rw_list_index += 1

        # restore inactive accounts
        if haveRestoreList and str(blockNum) in restore_list:
            restore_addrs = restore_list[str(blockNum)]
        if len(restore_addrs) != 0:
            insertRestoreAddressList(restore_addrs)

        # execute transactions in this block
        insertHeader(blockNum)
        insertUncles(blockNum)
        insertTransactionArgsList(blockNum)
        executeTransactionArgsList()

        # save intermediate results
        if saveResults and blockNum % temp_result_save_inteval == 0 and blockNum > lastKnownBlockNum and blockNum != endBlockNum:
            temp_file_name = "evm_simulation_result_Ethanos_" + str(0) + "_" + str(blockNum) + "_" + str(sweepEpoch) + ".json"
            saveSimBlocks(temp_file_name, temp_result_save_inteval)

        # compare tx result with write results
        if checkStateValidity:
            wrongCnt = 0
            for addr, item in final_writes.items():
                isCorrect = False
                if item == None:
                    # this account does not exist
                    isCorrect = checkTxExecutionResult(0, 0, "0x00", "0x00", addr)
                else:
                    nonce = item['nonce']
                    balance = item['balance']
                    codeHash = emptyCodeHash
                    storageRoot = emptyRoot
                    if item['codehash'] != None:
                        codeHash = item['codehash'].hex()
                    if item['storageroot'] != None:
                        storageRoot = item['storageroot'].hex()
                    isCorrect = checkTxExecutionResult(nonce, balance, storageRoot, codeHash, addr)
                if not isCorrect:
                    wrongCnt += 1
                    with open(txErrLogFilePath + 'tx_execution_fail_log_Ethanos_' + str(sweepEpoch) + '.txt', 'a') as file:
                        file.write("at block " + str(blockNum) + '\n')
                        file.write("addr: " + str(addr) + '\n')
                        file.write("expected account state\n")
                        file.write("  nonce: " + str(nonce) + '\n')
                        file.write("  balance: " + str(balance) + '\n')
                        file.write("  codeHash: " + str(codeHash) + '\n')
                        file.write("  storageRoot: " + str(storageRoot) + '\n')
                        file.write("\n")
            if wrongCnt != 0:
                endTime = datetime.now()
                print("ERROR: at block", blockNum, "tx is wrongly executed")
                print("stop simulation, elapsed time:", endTime-startTime)
                stopSimulation()

        # sweep
        if (blockNum+1)%sweepEpoch == 0:
            for addr, writtenBlockNum in cached_addrs.items():
                inactive_addrs[addr] = writtenBlockNum
            cached_addrs = active_addrs
            active_addrs = {}

    # simulation finished
    if saveResults:
        saveSimBlocks(sim_blocks_file_name, temp_result_save_inteval)
        print("save result:", sim_blocks_file_name)

    print("finish Ethanos EVM simulation")
    print("elapsed time:", datetime.now()-startTime)



# might be deprecated: use v2 if there is no problem
# replay txs in Ethereum through EVM to simulate Ethanos
def simulateEthaneEVM(startBlockNum, endBlockNum, deleteEpoch, inactivateEpoch, inactivateCriterion, fromLevel, temp_result_save_inteval):
    print("run Ethane simulation")

    # set Ethanos's options
    switchSimulationMode(1) # 1: Ethane mode
    setEthaneParams(deleteEpoch, inactivateEpoch, inactivateCriterion, fromLevel)
    # setFlushInterval(flushInterval)

    # load previous results
    if startBlockNum != 0:
        if lastKnownBlockNum+1 < startBlockNum:
            print("ERROR: cannot meet this condition -> lastKnownBlockNum + 1 >= startBlockNum")
            print("  lastKnownBlockNum:", lastKnownBlockNum)
            print("  startBlockNum:", startBlockNum)
            sys.exit()
        
        print("load previous results")
        # insert previous SimBlocks
        loadSimBlocks(0, lastKnownBlockNum, startBlockNum-1)
        # insert recent 256 block headers
        for blockNum in range(max(0, startBlockNum-300), startBlockNum):
            insertHeader(blockNum)

    # ready for restore list
    active_addrs = {} # active_addrs[addr] = blockNum
    inactive_addrs = {} # inactive_addrs[addr] = blockNum
    updated_addrs = {} # updated_addrs[blockNum] = addrs
    restore_list = {}
    if haveRestoreList:
        last_block_num_of_restore_list = 10000000
        if last_block_num_of_restore_list < endBlockNum:
            print("ERROR: last_block_num_of_restore_list is less than endBlockNum")
            print("  last_block_num_of_restore_list:", last_block_num_of_restore_list)
            print("  endBlockNum:", endBlockNum)
            sys.exit()

        restore_list_file_name = "restore_list_Ethane_" + str(0) + "_" + str(last_block_num_of_restore_list) + "_" + str(inactivateEpoch) + "_" + str(inactivateCriterion) + ".json"
        with open(restoreListFilePath+restore_list_file_name, 'r') as json_file:
            restore_list = json.load(json_file)

    # simulation result file name
    sim_blocks_file_name = "evm_simulation_result_Ethane_" + str(0) + "_" + str(endBlockNum) + "_" + str(deleteEpoch) + "_" + str(inactivateEpoch) + "_" + str(inactivateCriterion) + ".json"
    # temp_result_save_inteval = 500000

    startTime = datetime.now()
    tempStartTime = startTime
    loginterval = 1000
    batchsize = 1000 # db query batch size

    rwList = select_account_read_write_lists(cursor_thread, startBlockNum, startBlockNum+batchsize)
    rw_list_index = 0

    # execute blocks
    for blockNum in range(startBlockNum, endBlockNum+1):
        # print("\nblock ->", blockNum)
        # show process
        if blockNum % loginterval == 0:
            print("execute block", blockNum, "( Ethane:", startBlockNum, "~", endBlockNum, "/ E_D:", deleteEpoch, ", E_I:", inactivateEpoch, ", TH_I:", inactivateCriterion, ")")
            currentTime = datetime.now()
            elapsedTime = currentTime-startTime
            tempElapsedTime = currentTime-tempStartTime
            tempStartTime = currentTime
            print("elapsed:", elapsedTime, "( total bps:", int((blockNum-startBlockNum)/elapsedTime.total_seconds()), 
                  "/ recent bps:", int(loginterval/tempElapsedTime.total_seconds()), ")")
            print()

        # get read/write list from DB asyncly
        if (blockNum-startBlockNum)%batchsize == 0:
            if blockNum != startBlockNum:
                rwList = queryResult.get()
                rw_list_index = 0
                # print("receive query for block:", rwList[0]['blocknumber'], "~", rwList[-1]['blocknumber'])
            queryResult = pool.apply_async(select_account_read_write_lists, (cursor_thread, blockNum+batchsize, blockNum+batchsize*2,))

        # state diffs in this block: final_writes[addr] = item
        final_writes = {}
        # check which address to restore
        restore_addrs = []
        updated_addrs[blockNum] = set()
        while not haveRestoreList or checkStateValidity:
            # get rw item in this block
            if rw_list_index == len(rwList):
                break
            item = rwList[rw_list_index]

            if item['blocknumber'] != blockNum:
                # go to next block
                break
            else:
                # manage address activeness
                # if item['blocknumber'] != 0:
                #     print("  rwlist of block", item['blocknumber'])
                
                addrId = item['address_id']
                addr = select_address(cursor, addrId)
                addr = '0x' + addr

                if item['type'] % 2 == 1:
                    # account write

                    final_writes[addr] = item
                    if not haveRestoreList:
                        active_addrs[addr] = blockNum
                        updated_addrs[blockNum].add(addr)
                        if addr in inactive_addrs:
                            restore_addrs.append(addr)
                            del inactive_addrs[addr]
                    
                    if item['type'] == 63:
                        # account delete
                        # TODO: Ethanos에선 바로 account를 지우지 말고 restored flag만 true로 해놓고 account를 남겨놔야 복구 되었음 정도는 보여줘야할듯
                        # codeHash나 Root는 0x0000 이런 걸로 밀어놔서 복구 시도 조차 못하게 해도 좋을 것 같고
                        # -> 근데 실험에서는 어차피 지우기 전에 복구할 거고 위와 같은 점을 악용하거나 하진 않을거니까 그냥 write로 봐도 무방할듯
                        # 대신 더이상 존재하지 않는 주소로 봐야할 것 같음
                        final_writes[addr] = None
                        if not haveRestoreList:
                            del active_addrs[addr]
                            updated_addrs[blockNum].remove(addr)

                else:
                    # account read
                    if not haveRestoreList:
                        if addr in active_addrs:
                            # not to search large inactive_addrs
                            pass
                        elif addr in inactive_addrs:
                            restore_addrs.append(addr)
                            del inactive_addrs[addr]
                            active_addrs[addr] = blockNum
                            updated_addrs[blockNum].add(addr)
                        else:
                            # new address
                            pass

                rw_list_index += 1
        # print("  updated addrs:", updated_addrs[blockNum])
        
        # restore inactive accounts
        if haveRestoreList and str(blockNum) in restore_list:
            restore_addrs = restore_list[str(blockNum)]
        if len(restore_addrs) != 0:
            # print("  restore addrs:", restore_addrs)
            insertRestoreAddressList(restore_addrs)

        # execute transactions in this block
        insertHeader(blockNum)
        insertUncles(blockNum)
        insertTransactionArgsList(blockNum)
        executeTransactionArgsList()

        # save intermediate results
        if saveResults and blockNum % temp_result_save_inteval == 0 and blockNum > lastKnownBlockNum and blockNum != endBlockNum:
            temp_file_name = "evm_simulation_result_Ethane_" + str(0) + "_" + str(blockNum) + "_" + str(deleteEpoch) + "_" + str(inactivateEpoch) + "_" + str(inactivateCriterion) + ".json"
            saveSimBlocks(temp_file_name, temp_result_save_inteval)

        # compare tx result with write results
        if checkStateValidity:
            wrongCnt = 0
            for addr, item in final_writes.items():
                isCorrect = False
                if item == None:
                    # this account does not exist
                    isCorrect = checkTxExecutionResult(0, 0, "0x00", "0x00", addr)
                else:
                    nonce = item['nonce']
                    balance = item['balance']
                    codeHash = emptyCodeHash
                    storageRoot = emptyRoot
                    if item['codehash'] != None:
                        codeHash = item['codehash'].hex()
                    if item['storageroot'] != None:
                        storageRoot = item['storageroot'].hex()
                    isCorrect = checkTxExecutionResult(nonce, balance, storageRoot, codeHash, addr)
                if not isCorrect:
                    wrongCnt += 1
                    with open(txErrLogFilePath + 'tx_execution_fail_log_Ethane_' + str(deleteEpoch) + "_" + str(inactivateEpoch) + "_" + str(inactivateCriterion) + '.txt', 'a') as file:
                        file.write("at block " + str(blockNum) + '\n')
                        file.write("addr: " + str(addr) + '\n')
                        file.write("expected account state\n")
                        file.write("  nonce: " + str(nonce) + '\n')
                        file.write("  balance: " + str(balance) + '\n')
                        file.write("  codeHash: " + str(codeHash) + '\n')
                        file.write("  storageRoot: " + str(storageRoot) + '\n')
                        file.write("\n")
            if wrongCnt != 0:
                endTime = datetime.now()
                print("ERROR: at block", blockNum, "tx is wrongly executed")
                print("stop simulation, elapsed time:", endTime-startTime)
                stopSimulation()

        # inactivate
        if (blockNum+1)%inactivateEpoch == 0 and blockNum >= inactivateCriterion:
            bns = updated_addrs.keys()
            bns = sorted(bns)
            # print("bns:", bns)
            for bn in bns:
                if bn <= blockNum-inactivateCriterion:
                    # print("  check block", bn)
                    for addr in updated_addrs[bn]:
                        # print("    addr:", addr)
                        if addr in active_addrs and bn == active_addrs[addr]:
                            inactive_addrs[addr] = bn
                            del active_addrs[addr]
                            # print("      -> inactivated")
                    del updated_addrs[bn]
                else:
                    break
        # print("\n\n\n")

    # simulation finished
    if saveResults:
        saveSimBlocks(sim_blocks_file_name, temp_result_save_inteval)
        print("save result:", sim_blocks_file_name)

    print("finish Ethane EVM simulation")
    print("elapsed time:", datetime.now()-startTime)


# replay txs in Ethereum through EVM to simulate Ethanos
def simulateEthaneEVM_v2(startBlockNum, endBlockNum, deleteEpoch, inactivateEpoch, inactivateCriterion, fromLevel, temp_result_save_inteval):
    print("run Ethane simulation")

    # set Ethanos's options
    switchSimulationMode(1) # 1: Ethane mode
    setEthaneParams(deleteEpoch, inactivateEpoch, inactivateCriterion, fromLevel)
    # setFlushInterval(flushInterval)

    # load previous results
    if startBlockNum != 0:
        if lastKnownBlockNum+1 < startBlockNum:
            print("ERROR: cannot meet this condition -> lastKnownBlockNum + 1 >= startBlockNum")
            print("  lastKnownBlockNum:", lastKnownBlockNum)
            print("  startBlockNum:", startBlockNum)
            sys.exit()
        
        print("load previous results")
        # insert previous SimBlocks
        loadSimBlocks(0, lastKnownBlockNum, startBlockNum-1)
        # insert recent 256 block headers
        for blockNum in range(max(0, startBlockNum-300), startBlockNum):
            insertHeader(blockNum)

    # simulation result file name
    sim_blocks_file_name = "evm_simulation_result_Ethane_" + str(0) + "_" + str(endBlockNum) + "_" + str(deleteEpoch) + "_" + str(inactivateEpoch) + "_" + str(inactivateCriterion) + ".json"
    # temp_result_save_inteval = 500000

    startTime = datetime.now()
    tempStartTime = startTime
    loginterval = 1000
    batchsize = 1000 # db query batch size

    rwList = select_account_read_write_lists(cursor_thread, startBlockNum, startBlockNum+batchsize)
    rw_list_index = 0

    # execute blocks
    for blockNum in range(startBlockNum, endBlockNum+1):
        # print("\nblock ->", blockNum)
        # show process
        if blockNum % loginterval == 0:
            print("execute block", blockNum, "( Ethane:", startBlockNum, "~", endBlockNum, "/ E_D:", deleteEpoch, ", E_I:", inactivateEpoch, ", TH_I:", inactivateCriterion, ")")
            currentTime = datetime.now()
            elapsedTime = currentTime-startTime
            tempElapsedTime = currentTime-tempStartTime
            tempStartTime = currentTime
            print("elapsed:", elapsedTime, "( total bps:", int((blockNum-startBlockNum)/elapsedTime.total_seconds()), 
                  "/ recent bps:", int(loginterval/tempElapsedTime.total_seconds()), ")")
            print()

        # get read/write list from DB asyncly
        if (blockNum-startBlockNum)%batchsize == 0:
            if blockNum != startBlockNum:
                rwList = queryResult.get()
                rw_list_index = 0
                # print("receive query for block:", rwList[0]['blocknumber'], "~", rwList[-1]['blocknumber'])
            queryResult = pool.apply_async(select_account_read_write_lists, (cursor_thread, blockNum+batchsize, blockNum+batchsize*2,))

        # state diffs in this block: final_writes[addr] = item
        final_writes = {}
        # candidate addresses which might need restoration
        access_addrs = set()
        while not haveRestoreList or checkStateValidity:
            # get rw item in this block
            if rw_list_index == len(rwList):
                break
            item = rwList[rw_list_index]

            if item['blocknumber'] != blockNum:
                # go to next block
                break
            else:
                # manage address activeness
                # if item['blocknumber'] != 0:
                #     print("  rwlist of block", item['blocknumber'])
                
                addrId = item['address_id']
                addr = select_address(cursor, addrId)
                addr = '0x' + addr

                if item['type'] % 2 == 1:
                    # account write
                    # print("write addr:", addr)
                    final_writes[addr] = item
                    access_addrs.add(addr)
                    if item['type'] == 63:
                        # account delete
                        final_writes[addr] = None
                else:
                    # account read
                    # print("read addr:", addr)
                    access_addrs.add(addr)

                rw_list_index += 1
        # print("  updated addrs:", updated_addrs[blockNum])
        
        # restore inactive accounts
        if blockNum != 0:
            # print("access addrs:", list(access_addrs))
            insertAccessList(list(access_addrs))

        # execute transactions in this block
        insertHeader(blockNum)
        insertUncles(blockNum)
        insertTransactionArgsList(blockNum)
        executeTransactionArgsList()

        # save intermediate results
        if saveResults and blockNum % temp_result_save_inteval == 0 and blockNum > lastKnownBlockNum and blockNum != endBlockNum:
            temp_file_name = "evm_simulation_result_Ethane_" + str(0) + "_" + str(blockNum) + "_" + str(deleteEpoch) + "_" + str(inactivateEpoch) + "_" + str(inactivateCriterion) + ".json"
            saveSimBlocks(temp_file_name, temp_result_save_inteval)

        # compare tx result with write results
        if checkStateValidity:
            wrongCnt = 0
            for addr, item in final_writes.items():
                isCorrect = False
                if item == None:
                    # this account does not exist
                    isCorrect = checkTxExecutionResult(0, 0, "0x00", "0x00", addr)
                else:
                    nonce = item['nonce']
                    balance = item['balance']
                    codeHash = emptyCodeHash
                    storageRoot = emptyRoot
                    if item['codehash'] != None:
                        codeHash = item['codehash'].hex()
                    if item['storageroot'] != None:
                        storageRoot = item['storageroot'].hex()
                    isCorrect = checkTxExecutionResult(nonce, balance, storageRoot, codeHash, addr)
                if not isCorrect:
                    wrongCnt += 1
                    with open(txErrLogFilePath + 'tx_execution_fail_log_Ethane_' + str(deleteEpoch) + "_" + str(inactivateEpoch) + "_" + str(inactivateCriterion) + '.txt', 'a') as file:
                        file.write("at block " + str(blockNum) + '\n')
                        file.write("addr: " + str(addr) + '\n')
                        file.write("expected account state\n")
                        file.write("  nonce: " + str(nonce) + '\n')
                        file.write("  balance: " + str(balance) + '\n')
                        file.write("  codeHash: " + str(codeHash) + '\n')
                        file.write("  storageRoot: " + str(storageRoot) + '\n')
                        file.write("\n")
            if wrongCnt != 0:
                endTime = datetime.now()
                print("ERROR: at block", blockNum, "tx is wrongly executed")
                print("stop simulation, elapsed time:", endTime-startTime)
                stopSimulation()



    # simulation finished
    if saveResults:
        saveSimBlocks(sim_blocks_file_name, temp_result_save_inteval)
        print("save result:", sim_blocks_file_name)

    print("finish Ethane EVM simulation")
    print("elapsed time:", datetime.now()-startTime)





# generate restore list for Ethanos
def generateRestoreListEthanos(startBlockNum, endBlockNum, sweepEpoch):

    if startBlockNum != 0:
        print("ERROR: start block num of restore list should be 0")
        print("  startBlockNum:", startBlockNum)
        sys.exit()

    startTime = datetime.now()
    tempStartTime = startTime
    loginterval = 10000
    batchsize = 1000 # db query batch size
    temp_result_save_inteval = 500000

    # check which address to restore
    active_addrs = {}
    cached_addrs = {}
    inactive_addrs = {}
    total_restore_list = {}

    rwList = select_account_read_write_lists(cursor_thread, startBlockNum, startBlockNum+batchsize)
    rw_list_index = 0

    # execute blocks
    for blockNum in range(startBlockNum, endBlockNum+1):
        # print("\nblock ->", blockNum)
        # show process
        if blockNum % loginterval == 0:
            print("gen restore list at block", blockNum, "( Ethanos:", startBlockNum, "~", endBlockNum, "/ TH_I:", sweepEpoch, ")")
            currentTime = datetime.now()
            elapsedTime = currentTime-startTime
            tempElapsedTime = currentTime-tempStartTime
            tempStartTime = currentTime
            print("elapsed:", elapsedTime, "( total bps:", int((blockNum-startBlockNum)/elapsedTime.total_seconds()), 
                  "/ recent bps:", int(loginterval/tempElapsedTime.total_seconds()), ")")
            print()

        # get read/write list from DB asyncly
        if (blockNum-startBlockNum)%batchsize == 0:
            if blockNum != startBlockNum:
                rwList = queryResult.get()
                rw_list_index = 0
                # print("receive query for block:", rwList[0]['blocknumber'], "~", rwList[-1]['blocknumber'])
            queryResult = pool.apply_async(select_account_read_write_lists, (cursor_thread, blockNum+batchsize, blockNum+batchsize*2,))

        # check which address to restore
        restore_addrs = []
        while True:
            # get rw item in this block
            if rw_list_index == len(rwList):
                break
            item = rwList[rw_list_index]

            if item['blocknumber'] != blockNum:
                # go to next block
                break
            else:
                # manage address activeness
                # if item['blocknumber'] != 0:
                #     print("  rwlist of block", item['blocknumber'])
                
                addrId = item['address_id']
                addr = select_address(cursor, addrId)
                addr = '0x' + addr

                if item['type'] % 2 == 1:
                    # account write
                    active_addrs[addr] = blockNum
                    if addr in cached_addrs:
                        del cached_addrs[addr]
                    elif addr in inactive_addrs:
                        restore_addrs.append(addr)
                        del inactive_addrs[addr]
                    
                    if item['type'] == 63:
                        # account delete
                        # TODO: Ethanos에선 바로 account를 지우지 말고 restored flag만 true로 해놓고 account를 남겨놔야 복구 되었음 정도는 보여줘야할듯
                        # codeHash나 Root는 0x0000 이런 걸로 밀어놔서 복구 시도 조차 못하게 해도 좋을 것 같고
                        # -> 근데 실험에서는 어차피 지우기 전에 복구할 거고 위와 같은 점을 악용하거나 하진 않을거니까 그냥 write로 봐도 무방할듯
                        # 대신 더이상 존재하지 않는 주소로 봐야할 것 같음
                        del active_addrs[addr]
                else:
                    # account read
                    if addr in active_addrs:
                        # not to search large inactive_addrs
                        pass
                    elif addr in cached_addrs:
                        # not to search large inactive_addrs
                        pass
                    elif addr in inactive_addrs:
                        restore_addrs.append(addr)
                        del inactive_addrs[addr]
                        active_addrs[addr] = blockNum
                    else:
                        # new address
                        pass

                rw_list_index += 1

        if len(restore_addrs) != 0:
            total_restore_list[blockNum] = restore_addrs
        
        # save temp restore list
        if blockNum % temp_result_save_inteval == 0:
            restore_list_file_name = "restore_list_Ethanos_" + str(0) + "_" + str(blockNum) + "_" + str(sweepEpoch) + ".json"
            if len(total_restore_list) != 0:
                with open(restoreListFilePath+restore_list_file_name, 'w') as json_flie:
                    json.dump(total_restore_list, json_flie, indent=2)

        # sweep
        if (blockNum+1)%sweepEpoch == 0:
            for addr, writtenBlockNum in cached_addrs.items():
                inactive_addrs[addr] = writtenBlockNum
            cached_addrs = active_addrs
            active_addrs = {}

    # save restore list
    restore_list_file_name = "restore_list_Ethanos_" + str(0) + "_" + str(endBlockNum) + "_" + str(sweepEpoch) + ".json"
    if len(total_restore_list) != 0:
        with open(restoreListFilePath+restore_list_file_name, 'w') as json_flie:
            json.dump(total_restore_list, json_flie, indent=2)
        print("finish restore list generation for Ethanos")
        print("  file name:", restore_list_file_name)
    else:
        print("there is no address to restore")
    
    print("  elapsed time:", datetime.now()-startTime)

# generate restore list for Ethane
def generateRestoreListEthane(startBlockNum, endBlockNum, deleteEpoch, inactivateEpoch, inactivateCriterion):
    print("generate restore list for Ethane")

    # ready for restore list
    active_addrs = {} # active_addrs[addr] = blockNum
    inactive_addrs = {} # inactive_addrs[addr] = blockNum
    updated_addrs = {} # updated_addrs[blockNum] = addrs
    total_restore_list = {}

    startTime = datetime.now()
    tempStartTime = startTime
    loginterval = 1000
    batchsize = 1000 # db query batch size
    temp_result_save_inteval = 500000

    rwList = select_account_read_write_lists(cursor_thread, startBlockNum, startBlockNum+batchsize)
    rw_list_index = 0

    # execute blocks
    for blockNum in range(startBlockNum, endBlockNum+1):
        # print("\nblock ->", blockNum)
        # show process
        if blockNum % loginterval == 0:
            print("execute block", blockNum, "( Ethane:", startBlockNum, "~", endBlockNum, "/ E_D:", deleteEpoch, ", E_I:", inactivateEpoch, ", TH_I:", inactivateCriterion, ")")
            currentTime = datetime.now()
            elapsedTime = currentTime-startTime
            tempElapsedTime = currentTime-tempStartTime
            tempStartTime = currentTime
            print("elapsed:", elapsedTime, "( total bps:", int((blockNum-startBlockNum)/elapsedTime.total_seconds()), 
                  "/ recent bps:", int(loginterval/tempElapsedTime.total_seconds()), ")")
            print()

        # get read/write list from DB asyncly
        if (blockNum-startBlockNum)%batchsize == 0:
            if blockNum != startBlockNum:
                rwList = queryResult.get()
                rw_list_index = 0
                # print("receive query for block:", rwList[0]['blocknumber'], "~", rwList[-1]['blocknumber'])
            queryResult = pool.apply_async(select_account_read_write_lists, (cursor_thread, blockNum+batchsize, blockNum+batchsize*2,))

        # check which address to restore
        restore_addrs = []
        updated_addrs[blockNum] = set()
        while True:
            # get rw item in this block
            if rw_list_index == len(rwList):
                break
            item = rwList[rw_list_index]

            if item['blocknumber'] != blockNum:
                # go to next block
                break
            else:
                # manage address activeness
                # if item['blocknumber'] != 0:
                #     print("  rwlist of block", item['blocknumber'])
                
                addrId = item['address_id']
                addr = select_address(cursor, addrId)
                addr = '0x' + addr

                if item['type'] % 2 == 1:
                    # account write
                    
                    active_addrs[addr] = blockNum
                    updated_addrs[blockNum].add(addr)
                    if addr in inactive_addrs:
                        restore_addrs.append(addr)
                        del inactive_addrs[addr]
                    
                    if item['type'] == 63:
                        # account delete
                        
                        del active_addrs[addr]
                        updated_addrs[blockNum].remove(addr)

                else:
                    # account read
                    if addr in active_addrs:
                        # not to search large inactive_addrs
                        pass
                    elif addr in inactive_addrs:
                        restore_addrs.append(addr)
                        del inactive_addrs[addr]
                        active_addrs[addr] = blockNum
                        updated_addrs[blockNum].add(addr)
                    else:
                        # new address
                        pass

                rw_list_index += 1
        # print("  updated addrs:", updated_addrs[blockNum])
        
        # save restore addrs
        if len(restore_addrs) != 0:
            total_restore_list[blockNum] = restore_addrs

        # save temp restore list
        if blockNum % temp_result_save_inteval == 0:
            restore_list_file_name = "restore_list_Ethane_" + str(0) + "_" + str(blockNum) + "_" + str(inactivateEpoch) + "_" + str(inactivateCriterion)+ ".json"
            if len(total_restore_list) != 0:
                with open(restoreListFilePath+restore_list_file_name, 'w') as json_flie:
                    json.dump(total_restore_list, json_flie, indent=2)
                print("finish restore list generation for Ethanos")
                print("  file name:", restore_list_file_name)
            else:
                print("there is no address to restore")

        # # inactivate
        # if (blockNum+1)%inactivateEpoch == 0 and blockNum >= inactivateCriterion:
        #     bns = updated_addrs.keys()
        #     bns = sorted(bns)
        #     # print("bns:", bns)
        #     for bn in bns:
        #         if bn <= blockNum-inactivateCriterion:
        #             # print("  check block", bn)
        #             for addr in updated_addrs[bn]:
        #                 # print("    addr:", addr)
        #                 if addr in active_addrs and bn == active_addrs[addr]:
        #                     inactive_addrs[addr] = bn
        #                     del active_addrs[addr]
        #                     # print("      -> inactivated")
        #             del updated_addrs[bn]
        #         else:
        #             break
        # inactivate
        if (blockNum+1)%inactivateEpoch == 0 and blockNum >= inactivateCriterion:
            bns = updated_addrs.keys()
            # bns = sorted(bns)
            # print("bns:", bns)

            bn = min(bns)

            while bn <= blockNum-inactivateCriterion:
                if bn in bns:
                    # print("  check block", bn)
                    for addr in updated_addrs[bn]:
                        # print("    addr:", addr)
                        if addr in active_addrs and bn == active_addrs[addr]:
                            inactive_addrs[addr] = bn
                            del active_addrs[addr]
                            # print("      -> inactivated")
                    del updated_addrs[bn]
                
                bn += 1

    # save restore list
    restore_list_file_name = "restore_list_Ethane_" + str(0) + "_" + str(endBlockNum) + "_" + str(inactivateEpoch) + "_" + str(inactivateCriterion)+ ".json"
    if len(total_restore_list) != 0:
        with open(restoreListFilePath+restore_list_file_name, 'w') as json_flie:
            json.dump(total_restore_list, json_flie, indent=2)
        print("finish restore list generation for Ethanos")
        print("  file name:", restore_list_file_name)
    else:
        print("there is no address to restore")

    print("  elapsed time:", datetime.now()-startTime)





if __name__ == "__main__":
    print("start")
    startTime = datetime.now()

    # set threadpool for db querying
    pool = Pool(1)

    #
    # call APIs to simulate MPT
    #

    # set simulation options
    deleteDisk = False
    haveRestoreList = False
    checkStateValidity = True
    saveResults = True
    # set simulation mode
    # -> (EVM simulation) 3: Ethereum, 4: Ethane, 5: Ethanos
    simulationMode = 3
    # set simulation params
    startBlockNum = 0
    endBlockNum = 10000
    lastKnownBlockNum = 0 # try to load prev results (restore list file is needed)
    deleteEpoch = 1
    inactivateEpoch = 1
    inactivateCriterion = 200000
    temp_result_save_inteval = 500000 # save simulation results periodically
    trieInspectIntervals = range(0, endBlockNum+1-1000000, 1000000)
    fromLevel = 0 # how many parent nodes to omit in Merkle proofs
    flushInterval = 1 # block flush interval (default: 1, at every block / but genesis block is always flushed)

    if len(sys.argv) > 1:
        if len(sys.argv) != 9:
            print("ERROR: wrong # of argv, you need to put")
            print("  SERVER_PORT -> 8997: Ethereum, 8996: Ethanos, 8995: Ethane")
            print("  simulationMode: 3: Ethereum, 4: Ethane, 5: Ethanos")
            print("  startBlockNum, endBlockNum, lastKnownBlockNum, deleteEpoch, inactivateEpoch, inactivateCriterion")
            sys.exit()

        SERVER_PORT = int(sys.argv[1])
        simulationMode = int(sys.argv[2])
        startBlockNum = int(sys.argv[3])
        endBlockNum = int(sys.argv[4])
        lastKnownBlockNum = int(sys.argv[5])
        deleteEpoch = int(sys.argv[6])
        inactivateEpoch = int(sys.argv[7])
        inactivateCriterion = int(sys.argv[8])
        print("set new params")
        print("  SERVER_PORT:", SERVER_PORT)
        print("  simulationMode:", simulationMode)
        print("  startBlockNum:", startBlockNum)
        print("  endBlockNum:", endBlockNum)
        print("  lastKnownBlockNum:", lastKnownBlockNum)
        print("  deleteEpoch:", deleteEpoch)
        print("  inactivateEpoch:", inactivateEpoch)
        print("  inactivateCriterion:", inactivateCriterion)

    # generate restore list for Ethanos & Ethane
    # generateRestoreListEthanos(startBlockNum, endBlockNum, inactivateCriterion)
    # generateRestoreListEthane(startBlockNum, endBlockNum, deleteEpoch, inactivateEpoch, inactivateCriterion)
    # sys.exit()

    # connect to geth
    client_socket.connect((SERVER_IP, SERVER_PORT))

    # optional: merge simblocks for Ethane
    # mergeSimBlocks(10000000, temp_result_save_inteval, deleteEpoch, inactivateEpoch, inactivateCriterion, fromLevel)
    # sys.exit(1)

    # optional: mimic sync
    # ethereumTH trie at 5M: 0x004c4b40f7af27f30c2128f3aba8055eb25d73e4545628e7cc2af0a7c6e972a6
    # setDatabase(deleteDisk)
    # benchmarkSync("0x004c4b3fe2fe5730a95d8e09274d0cc5e43d27fc62f3924f41f0c325e983f23d")
    # sys.exit(1)



    # inspect and copy state
    # ethereum 100,000: 0x209230089ff328b2d87b721c48dbede5fd163c3fae29920188a7118275ab2013
    # ethereum 0.01M: 0x4de830f589266773eae1a1caa88d75def3f3a321fbd9aeb89570a57c6e7f3dbb
    # ethereum 0.5M: 0xb2bcfa2ffe869085c84a976435f1581a7a0eb7af64bafcbbda710661016aa3ab
    # ethereum 1M: 0x0e066f3c2297a5cb300593052617d1bca5946f0caa0635fdb1b85ac7e5236f34
    # ethereum 3M: 0x8e7ab0771fa333e1369fd48374010b8a21283a70690c6064fe2ecf091a1719ec
    # ethereum 5M: 0x6092dfd6bcdd375764d8718c365ce0e8323034da3d3b0c6d72cf7304996b86ad
    # ethereum 5.58M: 0x25ab955eb900ba009ab336533ea209a4880d3fcab044abc893a611d6ba21257d
    # copyStateHash = True
    # copyStateHashSnap = True
    # copyStatePath = False
    # copyStatePathSnap = False
    # setDatabase(False)
    # inspectAndCopyState("0x0e066f3c2297a5cb300593052617d1bca5946f0caa0635fdb1b85ac7e5236f34", copyStateHash, copyStateHashSnap, copyStatePath, copyStatePathSnap)
    # inspectAndCopyState("0x8e7ab0771fa333e1369fd48374010b8a21283a70690c6064fe2ecf091a1719ec", copyStateHash, copyStateHashSnap, copyStatePath, copyStatePathSnap)
    # inspectAndCopyState("0xe7a73d3c05829730c750ca483b5a65f8321adb25d8abb9da23a4cbb6473464ee", copyStateHash, copyStateHashSnap, copyStatePath, copyStatePathSnap)
    # inspectAndCopyState("0x6092dfd6bcdd375764d8718c365ce0e8323034da3d3b0c6d72cf7304996b86ad", copyStateHash, copyStateHashSnap, copyStatePath, copyStatePathSnap)
    # inspectAndCopyState("0x25ab955eb900ba009ab336533ea209a4880d3fcab044abc893a611d6ba21257d", copyStateHash, copyStateHashSnap, copyStatePath, copyStatePathSnap)
    # sys.exit()



    # DoS attack simulation
    # simulateEthereumDoSAttack(5600000, 5580000) # byzantium
    # simulateEthereumDoSAttack(5600000, 12250000) # berlin
    # simulateEthereumDoSAttack(5600000, 15550000) # paris
    # simulateEthereumDoSAttack(5600000, 17000000)

    # TODO(jmlee): call this function
    # setSimulationOptions(enableSnapshot, trieNodePrefixLen, loggingOpcodeStats)

    # run simulation
    setDatabase(deleteDisk)
    if simulationMode == 3:
        checkStateValidity = False # Ethereum can be easily verified by comparing state roots, so disable rich verification
        simulateEthereumEVM(startBlockNum, endBlockNum, lastKnownBlockNum, temp_result_save_inteval)
    elif simulationMode == 4:
        # simulateEthaneEVM(startBlockNum, endBlockNum, deleteEpoch, inactivateEpoch, inactivateCriterion, fromLevel, temp_result_save_inteval)
        haveRestoreList = False # v2 does not need restore list
        simulateEthaneEVM_v2(startBlockNum, endBlockNum, deleteEpoch, inactivateEpoch, inactivateCriterion, fromLevel, temp_result_save_inteval)
    elif simulationMode == 5:
        simulateEthanosEVM(startBlockNum, endBlockNum, inactivateCriterion, fromLevel, temp_result_save_inteval)
    else:
        print("wrong mode:", simulationMode)

    print("end")
    endTime = datetime.now()
    print("final elapsed time:", endTime-startTime)
