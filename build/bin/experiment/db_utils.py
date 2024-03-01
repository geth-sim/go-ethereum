import pymysql.cursors

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

# caching data from db
cache_address = {}
cache_slot = {}

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
