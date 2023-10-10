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
import multiprocessing
from multiprocessing.pool import ThreadPool as Pool

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

def saveSimBlocks(fileName):
    cmd = str("saveSimBlocks")
    cmd += str(",")
    cmd += str(fileName)

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
            saveSimBlocks(temp_file_name)

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
        saveSimBlocks(sim_blocks_file_name)
        print("save result:", sim_blocks_file_name)

    print("finish Ethereum EVM simulation")
    print("elapsed time:", datetime.now()-startTime)

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
            saveSimBlocks(temp_file_name)

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
        saveSimBlocks(sim_blocks_file_name)
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
            saveSimBlocks(temp_file_name)

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
        saveSimBlocks(sim_blocks_file_name)
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
            saveSimBlocks(temp_file_name)

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
        saveSimBlocks(sim_blocks_file_name)
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

    # run simulation
    setDatabase(deleteDisk)
    if simulationMode == 3:
        checkStateValidity = False # Ethereum can be easily verified by comparing state roots, so disable rich verification
        simulateEthereumEVM(startBlockNum, endBlockNum, lastKnownBlockNum, temp_result_save_inteval)
    elif simulationMode == 4:
        # simulateEthaneEVM(startBlockNum, endBlockNum, deleteEpoch, inactivateEpoch, inactivateCriterion, fromLevel, temp_result_save_inteval)
        simulateEthaneEVM_v2(startBlockNum, endBlockNum, deleteEpoch, inactivateEpoch, inactivateCriterion, fromLevel, temp_result_save_inteval)
    elif simulationMode == 5:
        simulateEthanosEVM(startBlockNum, endBlockNum, inactivateCriterion, fromLevel, temp_result_save_inteval)
    else:
        print("wrong mode:", simulationMode)

    print("end")
    endTime = datetime.now()
    print("final elapsed time:", endTime-startTime)
