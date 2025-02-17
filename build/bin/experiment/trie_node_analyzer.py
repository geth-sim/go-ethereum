import matplotlib.pyplot as plt
from datetime import datetime
import os, sys
import tempfile
from heapq import merge
import glob
from datetime import datetime
import random


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


# params
# protocol = "ethereum"
protocol = "trie-hashimoto"
block_num = 7000000
prefix_len = 4
state_root = state_roots[protocol][block_num]
sort_mode = "sorted"
# sort_mode = "random"


# paths
file_path = "/ethereum/th_plus/stateTries/" + protocol + "/" + "trieNodeInfos/"
input_file_name = "trieNodeInfos_" + str(block_num) + "_" + state_root
output_file_name = "trieNodeInfos_" + str(block_num) + "_" + state_root + "_" + sort_mode


def large_sort(input_file, output_file, chunksize=25_000_000, mode=sort_mode):
    print("large_sort() executed")
    print("  mode:", mode)
    print("  input_file:", input_file)
    print("  output_file:", output_file)

    fid = 1
    lines = []

    # make chunk files
    print("make chunk files")
    with open(input_file, 'r') as f_in:
        f_out = open('chunk_{}.tsv'.format(fid), 'w')
        for line_num, line in enumerate(f_in, 1):
            # print("line num:", line_num)
            lines.append(line)
            if not line_num % chunksize:
                if mode == "random":
                    random.shuffle(lines)
                elif mode == "sorted":
                    lines.sort(key=lambda row: row.split(',')[0])
                else:
                    print("ERROR: wrong mode ->", mode)
                    sys.exit()
                f_out.writelines(lines)

                print('  generate chunk', fid)
                f_out.close()
                lines = []
                fid += 1
                f_out = open('chunk_{}.tsv'.format(fid), 'w')

        # last chunk
        if lines:
            print('  generate chunk', fid)
            if mode == "random":
                random.shuffle(lines)
            elif mode == "sorted":
                lines.sort(key=lambda row: row.split(',')[0])
            else:
                print("ERROR: wrong mode ->", mode)
                sys.exit()
            f_out.writelines(lines)
            f_out.close()
            lines = []
    
    # merge chunk files
    print("merge chunk files")
    chunks = []
    path = "chunk_*.tsv"
    for filename in glob.glob(path):
        chunks += [open(filename, 'r')]    
    with open(output_file, 'w') as f_out:
        if mode == "random":
            for line in merge(*chunks, key=lambda row: random.random()):
                f_out.write(line)
        elif mode == "sorted":
            f_out.writelines(merge(*chunks, key=lambda row: row.split(',')[0]))
        else:
            print("ERROR: wrong mode ->", mode)
            sys.exit()
            
    # Clean up chunk files
    print("clean up chunk files")
    for chunk in chunks:
        chunk.close()
        os.remove(chunk.name)




if __name__ == "__main__":

    startTime = datetime.now()

    # 
    # open trie node infos file
    # 

    # print("open file:", file_path+input_file_name)
    # with open(file_path+input_file_name, 'r') as file:
    #     rows = file.readlines()



    #
    # get stats from trie nodes
    #

    # node_hash_prefix_counter = {}
    # for line in rows:
    #     params = line.split(',')
    #     node_hash = params[0]

    #     prefix = int(node_hash[:2+prefix_len*2], 16) # including "0x"
    #     if prefix in node_hash_prefix_counter:
    #         node_hash_prefix_counter[prefix] += 1
    #     else:
    #         node_hash_prefix_counter[prefix] = 1
        
    #    # print('nodehash:', node_hash, "/ prefix:", prefix, "/ count:", node_hash_prefix_counter[prefix])



    #
    # draw graph
    #

    # keys_to_remove = [key for key in node_hash_prefix_counter if key <= 3000000]
    # for key in keys_to_remove:
    #     del node_hash_prefix_counter[key]

    # x_values = list(node_hash_prefix_counter.keys())
    # y_values = list(node_hash_prefix_counter.values())

    # plt.figure()

    # # histogram
    # bins = 10000
    # counts, _, _ = plt.hist(x_values, bins=bins, weights=y_values, log=False)
    # # dot graph
    # # plt.scatter(x_values, y_values, s=1)  # s: dot size

    # plt.xlabel(f'node hash prefix (blocknum) (bins: {bins})')
    # plt.ylabel('# of prefix')
    # plt.title('Frequency of Keys')
    # plt.grid(True)

    # # max_y_value = max(y_values)
    # # print(f"Maximum y value: {max_y_value}")

    # max_hist_y_value = max(counts)
    # print(f"Maximum histogram y value: {max_hist_y_value}")

    # # plt.ylim(1, max(counts) * 1.1)

    # # 그래프 저장
    # graph_name = 'prefix_stat_'+state_root+'.png'
    # plt.savefig(graph_name)
    # print(f"Graph has been saved as {graph_name}")



    # 
    # sort trie nodes in certain order
    # 

    # # sort lines. 각 줄의 0번 index 요소를 기준으로 정렬합니다.
    # print("sort trie nodes")
    # rows.sort(key=lambda row: row.split(',')[0])
    # # save sorted result as a file
    # with open(file_path+output_file_name, 'w') as file:
    #     file.writelines(rows)
    large_sort(file_path + input_file_name, file_path + output_file_name)

    print("Data has been sorted and written to", output_file_name)


    endTime = datetime.now()
    print("final elapsed time:", endTime-startTime)
