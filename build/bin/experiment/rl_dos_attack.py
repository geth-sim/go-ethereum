import socket
import os, binascii
import sys
import multiprocessing as mp
import subprocess
import json

from web3 import Web3
from datetime import datetime
from os.path import exists
from multiprocessing.pool import ThreadPool as Pool
from brkgen.codegen.utils.fitness import SimulatorSideGate

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





#
### TODO(jmlee): connect to rl agent
#
# rl_connection = make_server_side() # open socket
# bytecode = rl_connection.get_code() # get code from rl agent
# rl_connection.return_fitness(fitness) # return fitness to rl agent
### 

# rl_connection = make_server_side() # open socket

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
    return result


def simulateDoSAttack(attackerAddress, attackerBalance, contractAddr, bytecode, client_socket=client_socket):
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


def _program_proxy(connected_socket, geth_socket, mutex):
    print("_program_proxy executed")

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

    roundNum = 0
    while True:

        try:

            roundNum += 1
            print("\n***start round", roundNum)

            #
            # generate attack contract's bytecode and tx.data as args
            #
            print("***gen attack contract (and tx data)")
            ############## TEMP ################
            bytecode = connected_socket.recv(4096)
            if not bytecode:
                connected_socket.shutdown(socket.SHUT_RDWR)
                connected_socket.close()
                break
            
            ############## TEMP ################
            # print("bytecode:", bytecode)

            #
            # TODO(jmlee): set call tx's input data
            #
            # print("***set call tx")
            # callTx['input'] = txdata
            callTx['input'] = b''

            #
            # clear caches before executing contract
            # TODO(jmlee): is this needed? check broken metre's setting again
            # 
            # dropPageCaches() # TODO(jmlee): drop page caches
            # setDatabase(False) # reopen geth's database such as LevelDB

            #
            # execute attack tx and get execution time
            #
            print("***execute call tx")
            # insertTransactionArgsList(targetBlockNum) # insert normal txs before attack tx
            mutex.acquire()
            insertTransactionArgs(callTx, geth_socket)
            attackResults = simulateDoSAttack(attackerAddress, attackerBalance, contractAddr, bytecode.hex(), geth_socket)
            mutex.release()
            print("  # of executed opcodes:", f'{attackResults[0]:,}')
            print("  opcode execution time:", f'{attackResults[1]:,}', "ns")
            if attackResults[0] != 0:
                print("  execution time per opcode:", f'{int(attackResults[1]/attackResults[0]):,}', "ns")
            print("  tx execution time:", f'{attackResults[2]:,}', "ns")
            print("  opcode gas cost:", f'{attackResults[3]:,}', "gas")
            print("  tx gas cost:", f'{attackResults[4]:,}', "gas")

            # 
            # return fitness value to RL agent
            #
            fitness: float = attackResults[4] / (attackResults[1]/1e9 + 1e-12) # gas/sec
            print(f'  FITNESS: {int(fitness):,}')

            ############## TEMP ################
            f_bytes = bytes(str(fitness).encode())
            connected_socket.send(f_bytes)
            # client_socket.close()
            ############## TEMP ################
            # del client_socket
        
        except Exception as e:
            print("exception in proxy function:", e)
            sys.exit()


def simulateEthereumDoSAttack(lastKnownBlockNum, targetBlockNum):
    print("run Ethereum DoS attack")

    ############## TEMP ################
    server_socket = socket.socket(socket.AF_INET, socket.SOCK_STREAM) # open socket
    server_socket.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server_socket.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEPORT, 1)
    server_socket.setblocking(True)
    server_socket.bind(('0.0.0.0', 10001))

    server_socket.listen()
    ############## TEMP ################

    print("make rl connection finished")

    # set simulation options for DoS attack
    # setSimulationOptions(False, 0, False)

    # set current state root and target block number (= decide EVM version to run)
    if targetBlockNum != 0:
        print("set state root and target block num")

        # if targetBlockNum <= lastKnownBlockNum:
        #     # target block is known, so set to its state
        #     setStateRootAndTargetBlockNum(0, lastKnownBlockNum, targetBlockNum, targetBlockNum)
        # else:
        #     # target block is unknown, so just set to known latest state (lastKnownBlockNum)
        #     setStateRootAndTargetBlockNum(0, lastKnownBlockNum, lastKnownBlockNum, targetBlockNum)

        # insert recent 256 block headers
        print("insert block headers")
        for blockNum in range(max(0, targetBlockNum-300), targetBlockNum+1):
            insertHeader(blockNum)


    # 
    # TODO(jmlee): warm up cache -> execute some blocks before simulation
    # 
    # for blockNum in range(targetBlockNum, targetBlockNum+10):
    #     # print("\nblock ->", blockNum)
    #     # show process
    #     if blockNum % 1 == 0:
    #         print("execute block", blockNum, "( port:", SERVER_PORT, "/ mode:", getSimulationTypeName())
    #         currentTime = datetime.now()
    #         elapsedTime = currentTime-startTime
    #         tempElapsedTime = currentTime-tempStartTime
    #         tempStartTime = currentTime

    #     # execute block
    #     # print("for block", blockNum)
    #     insertHeader(blockNum)
    #     insertUncles(blockNum)
    #     insertTransactionArgsList(blockNum)
    #     insertTransactionAccessListsV2(blockNum)
    #     executeTransactionArgsList()


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

    manager = mp.Manager()
    mutex = manager.Lock()


    # run rounds to execute attack contract
    print("start simulation")
    totalRoundNum = 10000000000
    startTime = datetime.now()

    while True:
        ############## TEMP ################
        _cTx, client_addr = server_socket.accept()
        print(f'Connected client : {client_addr}')
        p = mp.Process(target=_program_proxy, args=(_cTx, client_socket, mutex), daemon=True)
        p.start()
        ############## TEMP ################
        # for roundNum in range(totalRoundNum): # TODO(jmlee): make infinite loop?
            # print("\n***start round", roundNum)

            # #
            # # generate attack contract's bytecode and tx.data as args
            # #
            # print("***gen attack contract (and tx data)")
            # ############## TEMP ################
            # bytecode = client_socket.recv(4096)
            # if not bytecode:
            #     break
            
            # ############## TEMP ################

            # #
            # # TODO(jmlee): set call tx's input data
            # #
            # # print("***set call tx")
            # # callTx['input'] = txdata
            # callTx['input'] = b''

            # #
            # # clear caches before executing contract
            # # TODO(jmlee): is this needed? check broken metre's setting again
            # # 
            # # dropPageCaches() # TODO(jmlee): drop page caches
            # # setDatabase(False) # reopen geth's database such as LevelDB

            # #
            # # execute attack tx and get execution time
            # #
            # print("***execute call tx")
            # # insertTransactionArgsList(targetBlockNum) # insert normal txs before attack tx
            # insertTransactionArgs(callTx)
            # attackResults = simulateDoSAttack(attackerAddress, attackerBalance, contractAddr, bytecode.hex())
            # print("  # of executed opcodes:", f'{attackResults[0]:,}')
            # print("  opcode execution time:", f'{attackResults[1]:,}', "ns")
            # if attackResults[0] != 0:
            #     print("  execution time per opcode:", f'{int(attackResults[1]/attackResults[0]):,}', "ns")
            # print("  tx execution time:", f'{attackResults[2]:,}', "ns")
            # print("  opcode gas cost:", f'{attackResults[3]:,}', "gas")
            # print("  tx gas cost:", f'{attackResults[4]:,}', "gas")

            # # 
            # # return fitness value to RL agent
            # #
            # fitness: float = attackResults[4] / (attackResults[1]/1e9 + 1e-12) # gas/sec
            # print(f'  FITNESS: {int(fitness):,}')

            # ############## TEMP ################
            # f_bytes = bytes(str(fitness).encode())
            # client_socket.send(f_bytes)
            # # client_socket.close()
            # ############## TEMP ################
            # # del client_socket

    # simulation finished
    print("\nfinish Ethereum DoS attack simulation V2")
    print("elapsed time:", datetime.now()-startTime)
    sys.exit()


def runBytecode(bytecodeName, targetBlockNum):
    print("run Ethereum DoS attack")

    # rl_connection = make_server_side() # open socket
    print("make rl connection finished")

    # set simulation options for DoS attack
    # setSimulationOptions(False, 0, False)

    # set current state root and target block number (= decide EVM version to run)
    if targetBlockNum != 0:
        print("set state root and target block num")

        # if targetBlockNum <= lastKnownBlockNum:
        #     # target block is known, so set to its state
        #     setStateRootAndTargetBlockNum(0, lastKnownBlockNum, targetBlockNum, targetBlockNum)
        # else:
        #     # target block is unknown, so just set to known latest state (lastKnownBlockNum)
        #     setStateRootAndTargetBlockNum(0, lastKnownBlockNum, lastKnownBlockNum, targetBlockNum)

        # insert recent 256 block headers
        print("insert block headers")
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
    print("start simulation")
    totalRoundNum = 1
    startTime = datetime.now()
    for roundNum in range(totalRoundNum):
        print("\n***start round", roundNum)

        #
        # generate attack contract's bytecode and tx.data as args
        #
        print("***gen attack contract (and tx data)")
        # bytecode = rl_connection.get_code()
        with open(bytecodeName, 'rb') as file:
            bytecode = file.read()

        #
        # TODO(jmlee): set call tx's input data
        #
        # print("***set call tx")
        # callTx['input'] = txdata
        callTx['input'] = b''

        #
        # clear caches before executing contract
        # 
        # dropPageCaches() # TODO(jmlee): drop page caches
        setDatabase(False) # reopen geth's database such as LevelDB

        #
        # execute attack tx and get execution time
        #
        print("***execute call tx")
        # insertTransactionArgsList(targetBlockNum) # insert normal txs before attack tx
        insertTransactionArgs(callTx)
        attackResults = simulateDoSAttack(attackerAddress, attackerBalance, contractAddr, bytecode.hex())
        print("  # of executed opcodes:", f'{attackResults[0]:,}')
        print("  opcode execution time:", f'{attackResults[1]:,}', "ns")
        print("  execution time per opcode:", f'{int(attackResults[1]/attackResults[0]):,}', "ns")
        print("  tx execution time:", f'{attackResults[2]:,}', "ns")
        print("  opcode gas cost:", f'{attackResults[3]:,}', "gas")
        print("  tx gas cost:", f'{attackResults[4]:,}', "gas")

        # 
        # return fitness value to RL agent
        #
        fitness: float = attackResults[4] / (attackResults[1]/1e9 + 1e-12) # gas/sec
        print(f'  FITNESS: {int(fitness):,}')
        # rl_connection.return_fitness(fitness)

    # simulation finished
    print("\nfinish Ethereum DoS attack simulation V2")
    print("elapsed time:", datetime.now()-startTime)
    sys.exit()


# def runAsServer(lastKnownBlockNum, targetBlockNum):
#     print("run Ethereum DoS attack")

#     rl_connection = make_server_side() # open socket
#     print("make rl connection finished")

#     # set simulation options for DoS attack
#     # setSimulationOptions(False, 0, False)

#     # set current state root and target block number (= decide EVM version to run)
#     if targetBlockNum != 0:
#         print("set state root and target block num")

#         # if targetBlockNum <= lastKnownBlockNum:
#         #     # target block is known, so set to its state
#         #     setStateRootAndTargetBlockNum(0, lastKnownBlockNum, targetBlockNum, targetBlockNum)
#         # else:
#         #     # target block is unknown, so just set to known latest state (lastKnownBlockNum)
#         #     setStateRootAndTargetBlockNum(0, lastKnownBlockNum, lastKnownBlockNum, targetBlockNum)

#         # insert recent 256 block headers
#         print("insert block headers")
#         for blockNum in range(max(0, targetBlockNum-300), targetBlockNum+1):
#             insertHeader(blockNum)

#     # set attacker's and attack contract's accounts
#     attackerAddress = "8b0725a76aff1bdcd64a005b44a736096221ccf3" # a random address that has never appeared before
#     attackerBalance = 10000000*10**18 # 10M ETH
#     contractAddr = "9e2e9e69723ebb0566d8741056acb1cb894d850d" # a random address that has never appeared before

#     # set call tx
#     callTx = dict()
#     callTx['from'] = bytes.fromhex(attackerAddress)
#     callTx['to'] = bytes.fromhex(contractAddr)
#     callTx['gas'] = 30000000 # large enough tx gas limit for attack
#     # callTx['gasprice'] = 0
#     callTx['gasprice'] = None
#     callTx['value'] = 0
#     callTx['nonce'] = 0 # TODO(jmlee): wrong nonce value does not block tx execution, but correct this later
#     # callTx['maxfeepergas'] = None
#     callTx['maxfeepergas'] = 4236941740
#     callTx['maxpriorityfeepergas'] = None

#     # run rounds to execute attack contract
#     print("start simulation")
#     totalRoundNum = 10000000000
#     startTime = datetime.now()
#     for roundNum in range(totalRoundNum): # TODO(jmlee): make infinite loop?
#         print("\n***start round", roundNum)

#         #
#         # generate attack contract's bytecode and tx.data as args
#         #
#         print("***gen attack contract (and tx data)")
#         bytecode = rl_connection.get_code()

#         #
#         # TODO(jmlee): set call tx's input data
#         #
#         # print("***set call tx")
#         # callTx['input'] = txdata
#         callTx['input'] = b''

#         #
#         # clear caches before executing contract
#         # 
#         # dropPageCaches() # TODO(jmlee): drop page caches
#         setDatabase(False) # reopen geth's database such as LevelDB

#         #
#         # execute attack tx and get execution time
#         #
#         print("***execute call tx")
#         # insertTransactionArgsList(targetBlockNum) # insert normal txs before attack tx
#         insertTransactionArgs(callTx)
#         attackResults = simulateDoSAttack(attackerAddress, attackerBalance, contractAddr, bytecode.hex())
#         print("  # of executed opcodes:", f'{attackResults[0]:,}')
#         print("  opcode execution time:", f'{attackResults[1]:,}', "ns")
#         print("  execution time per opcode:", f'{int(attackResults[1]/attackResults[0]):,}', "ns")
#         print("  tx execution time:", f'{attackResults[2]:,}', "ns")
#         print("  opcode gas cost:", f'{attackResults[3]:,}', "gas")
#         print("  tx gas cost:", f'{attackResults[4]:,}', "gas")

#         # 
#         # return fitness value to RL agent
#         #
#         fitness: float = attackResults[4] / (attackResults[1]/1e9 + 1e-12) # gas/sec
#         print(f'  FITNESS: {int(fitness):,}')
#         rl_connection.return_fitness(fitness)

#     # simulation finished
#     print("\nfinish Ethereum DoS attack simulation V2")
#     print("elapsed time:", datetime.now()-startTime)
#     sys.exit()






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
def insertTransactionArgs(tx, client_socket=client_socket):
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
    inspectAndCopyState(wantedStateRoot, copyStateHash, copyStateHashSnap, copyStatePath, copyStatePathSnap)

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
    # checkStateValidity = True
    # saveResults = True
    # set simulation params
    lastKnownBlockNum = 0 # try to load prev results (restore list file is needed)
    temp_result_save_inteval = 500000 # save simulation results periodically
    # fromLevel = 0 # how many parent nodes to omit in Merkle proofs
    # flushInterval = 1 # block flush interval (default: 1, at every block / but genesis block is always flushed)

    if len(sys.argv) > 1:
        if len(sys.argv) != 4:
            print("ERROR: wrong # of argv, you need to put")
            print("  SERVER_PORT of simulator")
            print("  lastKnownBlockNum, targetBlockNum")
            sys.exit()

        SERVER_PORT = int(sys.argv[1])
        lastKnownBlockNum = int(sys.argv[2])
        targetBlockNum = int(sys.argv[3])

        print("set new params")
        print("  SERVER_PORT:", SERVER_PORT)
        print("  lastKnownBlockNum:", lastKnownBlockNum)
        print("  targetBlockNum:", targetBlockNum)

    # connect to geth
    client_socket.connect((SERVER_IP, SERVER_PORT))



    # run attack simulation
    # TODO(jmlee): call setSimulationOptions() function before setDatabase() call
    # setSimulationOptions(enableSnapshot, trieNodePrefixLen, loggingOpcodeStats)
    targetBlockNum = lastKnownBlockNum
    setDatabase(False)

    simulateEthereumDoSAttack(lastKnownBlockNum, targetBlockNum)
    # runBytecode('batch_32.ebc', targetBlockNum)

    # simulateEthereumEVM(startBlockNum, endBlockNum, lastKnownBlockNum, temp_result_save_inteval)
    # commitDirtyStates()

    print("end")
    endTime = datetime.now()
    print("final elapsed time:", endTime-startTime)
