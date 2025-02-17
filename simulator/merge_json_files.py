from datetime import datetime
import json

FILE_PATH = "logFiles/evm/simBlocks/"

files_to_merge = ['copied_evm_simulation_result_EthereumPR_0_5000000.json',
                  'copied_evm_simulation_result_EthereumPR_5000001_7500000.json']

merged_file_name = 'merged.json'


def combine_jsons(file_list):
    print("start merge json files")

    all_data_dict = {}
    for json_file in file_list:
        print("  try to merge:", json_file)
        with open(FILE_PATH+json_file,'r') as file:
            all_data_dict.update(json.load(file))

    # save to json file
    with open(FILE_PATH+merged_file_name, "w") as outfile:
        # json.dump(all_data_dict, outfile, indent=2, sort_keys=True)
        json.dump(all_data_dict, outfile, indent=2)
    
    print("  => success, merged file name:", merged_file_name)


if __name__ == "__main__":

    start_time = datetime.now()
    combine_jsons(files_to_merge)
    end_time = datetime.now()
    
    print("final elapsed time:", end_time-start_time)
