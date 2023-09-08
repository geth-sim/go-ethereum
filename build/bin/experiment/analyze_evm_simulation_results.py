import json, sys
from datetime import datetime
import numpy as np
import matplotlib.pyplot as plt

LOG_FILE_PATH = "/home/jmlee/projects/evmSimulation/go-ethereum/simulator/logFiles/evm/simBlocks/"
GRAPH_PATH = "./graphs/"

def draw_graph_to_compare(startBlockNum, endBlockNum, first_block_to_draw, last_block_to_draw, graph_window_size, datas, field_name):

    startTime = datetime.now()

    fig, ax1 = plt.subplots()
    for sim_mode_name, data in datas.items():
        
        # extract block nums
        json_keys = list(data.keys())
        blockNums = [int(blockNum) for blockNum in json_keys]

        # extract fields
        print("extract fields")
        y_values = []
        if field_name == "PaymentTxAvgExecute":
            PaymentTxLen = [block_data['PaymentTxLen'] for block_data in data.values()]
            PaymentTxExecutes = [block_data['PaymentTxExecutes'] for block_data in data.values()]
            y_values = [executeTime / txLen if txLen != 0 else 0 for executeTime, txLen in zip(PaymentTxExecutes, PaymentTxLen)]
        elif field_name == "CallTxAvgExecute":
            CallTxLen = [block_data['CallTxLen'] for block_data in data.values()]
            CallTxExecutes = [block_data['CallTxExecutes'] for block_data in data.values()]
            y_values = [executeTime / txLen if txLen != 0 else 0 for executeTime, txLen in zip(CallTxExecutes, CallTxLen)]
        elif field_name == "TotalTxAvgExecute":
            PaymentTxLen = [block_data['PaymentTxLen'] for block_data in data.values()]
            PaymentTxExecutes = [block_data['PaymentTxExecutes'] for block_data in data.values()]
            CallTxLen = [block_data['CallTxLen'] for block_data in data.values()]
            CallTxExecutes = [block_data['CallTxExecutes'] for block_data in data.values()]
            
            TxLen = [paymentLen + callLen for paymentLen, callLen in zip(PaymentTxLen, CallTxLen)]
            TxExecutes = [paymentExecutes + callExecutes for paymentExecutes, callExecutes in zip(PaymentTxExecutes, CallTxExecutes)]
            y_values = [executeTime / txLen if txLen != 0 else 0 for executeTime, txLen in zip(TxExecutes, TxLen)]
        elif field_name == "WithoutCachedAccountReads":
            if sim_mode_name == "Ethanos":
                AccountReads = [block_data["AccountReads"] for block_data in data.values()]
                CachedAccountReads = [block_data["CachedAccountReads"] for block_data in data.values()]
                y_values = [x - y for x, y in zip(AccountReads, CachedAccountReads)]
            else:
                y_values = [block_data["AccountReads"] for block_data in data.values()]
        else:
            y_values = [block_data[field_name] for block_data in data.values()]

        # set range
        blockNums = blockNums[first_block_to_draw:last_block_to_draw+1]
        y_values = y_values[first_block_to_draw:last_block_to_draw+1]

        # remove 0s (TODO(jmlee): is this needed?)
        # blockNums, y_values = zip(*[(x, y) for x, y in zip(blockNums, y_values) if y != 0])

        # moving avg
        y_values_mov_avg = np.convolve(y_values, np.ones(graph_window_size)/graph_window_size, mode='valid')

        # draw graph
        print("draw graph")
        ax1.plot(blockNums[graph_window_size-1:], y_values_mov_avg, marker='o', markersize=1, label=sim_mode_name)
        ax1.set_xlabel('Block Number')
        ax1.set_ylabel(field_name + '_mov_avg')
        ax1.ticklabel_format(axis="y", style="sci", scilimits=(0,0))
        
        # ax2 = ax1.twinx()
    
    plt.title(field_name+ ' in Blocks')
    plt.legend()

    plt.savefig(GRAPH_PATH + field_name + '_' + str(startBlockNum) + "_" + str(endBlockNum) + '.png')

    print("finish analyze")
    print("elapsed time:", datetime.now()-startTime)

def graph_stack_graph(first_block_to_draw, last_block_to_draw, datas):

    startTime = datetime.now()

    blockNums = range(first_block_to_draw, last_block_to_draw+1)
    blockExecutes = {}
    y_values_to_stack = {}
    for sim_mode_name, data in datas.items():
        
        # extract block nums
        # json_keys = list(data.keys())
        # blockNums = [int(blockNum) for blockNum in json_keys]

        # extract fields
        blockExecuteTime = [block_data['BlockExecuteTime'] for block_data in data.values()][first_block_to_draw:last_block_to_draw+1]
        PaymentTxExecutes = [block_data['PaymentTxExecutes'] for block_data in data.values()][first_block_to_draw:last_block_to_draw+1]
        CallTxExecutes = [block_data['CallTxExecutes'] for block_data in data.values()][first_block_to_draw:last_block_to_draw+1]
        AccountCommits = [block_data['AccountCommits'] for block_data in data.values()][first_block_to_draw:last_block_to_draw+1]
        StorageCommits = [block_data['StorageCommits'] for block_data in data.values()][first_block_to_draw:last_block_to_draw+1]
        TrieDBCommits = [block_data['TrieDBCommits'] for block_data in data.values()][first_block_to_draw:last_block_to_draw+1]
        DiskCommits = [block_data['DiskCommits'] for block_data in data.values()][first_block_to_draw:last_block_to_draw+1]

        # collect accumulative values
        blockExecutes[sim_mode_name] = np.cumsum(blockExecuteTime)
        y_values_to_stack[sim_mode_name] = [[], []]
        y_values_to_stack[sim_mode_name][0].append(np.cumsum(PaymentTxExecutes))
        y_values_to_stack[sim_mode_name][0].append(np.cumsum(CallTxExecutes))
        y_values_to_stack[sim_mode_name][0].append(np.cumsum(AccountCommits))
        y_values_to_stack[sim_mode_name][0].append(np.cumsum(StorageCommits))
        y_values_to_stack[sim_mode_name][0].append(np.cumsum(TrieDBCommits))
        y_values_to_stack[sim_mode_name][0].append(np.cumsum(DiskCommits))
        
        y_values_to_stack[sim_mode_name][1].append("PaymentTxExecutes")
        y_values_to_stack[sim_mode_name][1].append("CallTxExecutes")
        y_values_to_stack[sim_mode_name][1].append("AccountCommits")
        y_values_to_stack[sim_mode_name][1].append("StorageCommits")
        y_values_to_stack[sim_mode_name][1].append("TrieDBCommits")
        y_values_to_stack[sim_mode_name][1].append("DiskCommits")

        if sim_mode_name == "Ethane":
            DeleteUpdates = [block_data['DeleteUpdates'] for block_data in data.values()][first_block_to_draw:last_block_to_draw+1]
            y_values_to_stack[sim_mode_name][0].append(np.cumsum(DeleteUpdates))
            y_values_to_stack[sim_mode_name][1].append("DeleteUpdates")

            DeleteHashes = [block_data['DeleteHashes'] for block_data in data.values()][first_block_to_draw:last_block_to_draw+1]
            y_values_to_stack[sim_mode_name][0].append(np.cumsum(DeleteHashes))
            y_values_to_stack[sim_mode_name][1].append("DeleteHashes")

            InactivateUpdates = [block_data['InactivateUpdates'] for block_data in data.values()][first_block_to_draw:last_block_to_draw+1]
            y_values_to_stack[sim_mode_name][0].append(np.cumsum(InactivateUpdates))
            y_values_to_stack[sim_mode_name][1].append("InactivateUpdates")

            UsedProofUpdtaes = [block_data['UsedProofUpdtaes'] for block_data in data.values()][first_block_to_draw:last_block_to_draw+1]
            y_values_to_stack[sim_mode_name][0].append(np.cumsum(UsedProofUpdtaes))
            y_values_to_stack[sim_mode_name][1].append("UsedProofUpdtaes")



    for sim_mode_name, _ in datas.items():
        fig, ax1 = plt.subplots()

        print("draw graph")
        ax1.plot(blockNums, blockExecutes[sim_mode_name], marker='o', markersize=1, label="block execute time")

        ax1.stackplot(blockNums, y_values_to_stack[sim_mode_name][0], labels=y_values_to_stack[sim_mode_name][1], alpha=0.5)

        ax1.set_xlabel('Block Number')
        ax1.set_ylabel('Block execution time analysis')

        ax1.ticklabel_format(axis="y", style="sci", scilimits=(0,0))
        
        plt.title('Block execution time analysis')
        plt.legend()

        plt.savefig(GRAPH_PATH + "block_execution_time_analysis_" + sim_mode_name + '_' + str(first_block_to_draw) + "_" + str(last_block_to_draw) + '.png')

        print("elapsed time:", datetime.now()-startTime)

    
    print("finish analyze")
    print("total elapsed time:", datetime.now()-startTime)



if __name__ == "__main__":

    startTime = datetime.now()
    
    # set simulation mode
    # -> (EVM simulation) 3: Ethereum, 4: Ethane, 5: Ethanos
    simulationMode = 5
    # set simulation params
    startBlockNum = 0
    endBlockNum = 4000000
    deleteEpoch = 1
    inactivateEpoch = 1
    inactivateCriterion = 200000
    fromLevel = 0 # how many parent nodes to omit in Merkle proofs
    flushInterval = 1 # block flush interval (default: 1, at every block / but genesis block is always flushed)

    # graph options
    first_block_to_draw = 3000000
    last_block_to_draw = endBlockNum
    graph_window_size = 1000

    # collect log data
    datas = {}
    try:
        print("collect Ethereum data")
        sim_blocks_file_name = "evm_simulation_result_Ethereum_" + str(startBlockNum) + "_" + str(endBlockNum) + ".json"
        with open(LOG_FILE_PATH+sim_blocks_file_name, 'r') as json_file:
            data = json.load(json_file)
            datas['Ethereum'] = data 
    except:
        print("  -> no Ethereum data")
        pass
    try:
        print("collect Ethane data")
        sim_blocks_file_name = "evm_simulation_result_Ethane_" + str(startBlockNum) + "_" + str(endBlockNum) + "_" + str(deleteEpoch) + "_" + str(inactivateEpoch) + "_" + str(inactivateCriterion) + ".json"
        with open(LOG_FILE_PATH+sim_blocks_file_name, 'r') as json_file:
            data = json.load(json_file)
            datas['Ethane'] = data 
    except:
        print("  -> no Ethane data")
        pass
    try:
        print("collect Ethanos data")
        sim_blocks_file_name = "evm_simulation_result_Ethanos_" + str(startBlockNum) + "_" + str(endBlockNum) + "_" + str(inactivateCriterion) + ".json"
        with open(LOG_FILE_PATH+sim_blocks_file_name, 'r') as json_file:
            data = json.load(json_file)
            datas['Ethanos'] = data 
    except:
        print("  -> no Ethanos data")
        pass



    field_names = ["TotalTxAvgExecute", "WithoutCachedAccountReads", "CallTxAvgExecute", "CachedAccountReads", "AccountDeleted", "AccountUpdated", "DiskCommits", "PaymentTxAvgExecute", "AccountReads", "AccountHashes", "AccountUpdates", "AccountCommits"]
    for field_name in field_names:
        draw_graph_to_compare(startBlockNum, endBlockNum, first_block_to_draw, last_block_to_draw, graph_window_size, datas, field_name)

    graph_stack_graph(first_block_to_draw, last_block_to_draw, datas)

    print("total elapsed time:", datetime.now()-startTime)
