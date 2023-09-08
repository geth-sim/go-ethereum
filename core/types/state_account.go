// Copyright 2021 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package types

import (
	"bytes"
	"fmt"
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

//go:generate go run ../../rlp/rlpgen -type StateAccount -out gen_account_rlp.go

// StateAccount is the Ethereum consensus representation of accounts.
// These objects are stored in the main account trie.
type StateAccount struct {
	Nonce    uint64
	Balance  *big.Int
	Root     common.Hash // merkle root of the storage trie
	CodeHash []byte
}

// NewEmptyStateAccount constructs an empty state account.
func NewEmptyStateAccount() *StateAccount {
	return &StateAccount{
		Balance:  new(big.Int),
		Root:     EmptyRootHash,
		CodeHash: EmptyCodeHash.Bytes(),
	}
}

// Copy returns a deep-copied state account object.
func (acct *StateAccount) Copy() *StateAccount {
	var balance *big.Int
	if acct.Balance != nil {
		balance = new(big.Int).Set(acct.Balance)
	}
	return &StateAccount{
		Nonce:    acct.Nonce,
		Balance:  balance,
		Root:     acct.Root,
		CodeHash: common.CopyBytes(acct.CodeHash),
	}
}

func (acct *StateAccount) Print() {
	fmt.Println("  Nonce:", acct.Nonce)
	fmt.Println("  Balance:", acct.Balance)
	fmt.Println("  Root:", acct.Root.Hex())
	fmt.Println("  CodeHash:", common.Bytes2Hex(acct.CodeHash))
}

func (acct *StateAccount) ToString() string {
	str := ""
	str += "  Nonce: " + strconv.FormatUint(acct.Nonce, 10) + "\n"
	if acct.Balance != nil {
		str += "  Balance: " + strconv.FormatUint(acct.Balance.Uint64(), 10) + "\n"
	} else {
		str += "  Balance: nil\n"
	}
	str += "  Root: " + acct.Root.Hex() + "\n"
	str += "  CodeHash: " + common.Bytes2Hex(acct.CodeHash) + "\n"

	return str
}

func (acct *StateAccount) Equal(compAcct *StateAccount) bool {
	if acct.Nonce != compAcct.Nonce {
		return false
	}
	if acct.Balance.Cmp(compAcct.Balance) != 0 {
		return false
	}
	if acct.Root != compAcct.Root {
		return false
	}
	if !bytes.Equal(acct.CodeHash, compAcct.CodeHash) {
		return false
	}
	return true
}

// EthanetateAccount is the Ethane consensus representation of accounts. (jmlee)
// These objects are stored in the main account trie.
type EthaneStateAccount struct {
	Nonce    uint64
	Balance  *big.Int
	Root     common.Hash // merkle root of the storage trie
	CodeHash []byte
	Addr     common.Address // address of this account
}

func (acct *EthaneStateAccount) Print() {
	fmt.Println("  Nonce:", acct.Nonce)
	fmt.Println("  Balance:", acct.Balance)
	fmt.Println("  Root:", acct.Root.Hex())
	fmt.Println("  CodeHash:", common.Bytes2Hex(acct.CodeHash))
	fmt.Println("  Addr:", acct.Addr)
}

func (acct *EthaneStateAccount) ToString() string {
	str := ""
	str += "  Nonce: " + strconv.FormatUint(acct.Nonce, 10) + "\n"
	if acct.Balance != nil {
		str += "  Balance: " + strconv.FormatUint(acct.Balance.Uint64(), 10) + "\n"
	} else {
		str += "  Balance: nil\n"
	}
	str += "  Root: " + acct.Root.Hex() + "\n"
	str += "  CodeHash: " + common.Bytes2Hex(acct.CodeHash) + "\n"
	str += "  Addr: " + acct.Addr.Hex() + "\n"

	return str
}

func (acct *EthaneStateAccount) Equal(compAcct *StateAccount) bool {
	if acct.Nonce != compAcct.Nonce {
		return false
	}
	if acct.Balance.Cmp(compAcct.Balance) != 0 {
		return false
	}
	if acct.Root != compAcct.Root {
		return false
	}
	if !bytes.Equal(acct.CodeHash, compAcct.CodeHash) {
		return false
	}
	return true
}

// EthanostateAccount is the Ethanos consensus representation of accounts. (jmlee)
// These objects are stored in the main account trie.
type EthanosStateAccount struct {
	Nonce    uint64
	Balance  *big.Int
	Root     common.Hash // merkle root of the storage trie
	CodeHash []byte
	Restored bool // flag whether this account is restored or not (default: false)
}

func (acct *EthanosStateAccount) Print() {
	fmt.Println("  Nonce:", acct.Nonce)
	fmt.Println("  Balance:", acct.Balance)
	fmt.Println("  Root:", acct.Root.Hex())
	fmt.Println("  CodeHash:", common.Bytes2Hex(acct.CodeHash))
	fmt.Println("  Restored:", acct.Restored)
}

func (acct *EthanosStateAccount) ToString() string {
	str := ""
	str += "  Nonce: " + strconv.FormatUint(acct.Nonce, 10) + "\n"
	if acct.Balance != nil {
		str += "  Balance: " + strconv.FormatUint(acct.Balance.Uint64(), 10) + "\n"
	} else {
		str += "  Balance: nil\n"
	}
	str += "  Root: " + acct.Root.Hex() + "\n"
	str += "  CodeHash: " + common.Bytes2Hex(acct.CodeHash) + "\n"
	if acct.Restored {
		str += "  Restored: true\n"
	} else {
		str += "  Restored: false\n"
	}

	return str
}

func (acct *EthanosStateAccount) Equal(compAcct *StateAccount) bool {
	if acct.Nonce != compAcct.Nonce {
		return false
	}
	if acct.Balance.Cmp(compAcct.Balance) != 0 {
		return false
	}
	if acct.Root != compAcct.Root {
		return false
	}
	if !bytes.Equal(acct.CodeHash, compAcct.CodeHash) {
		return false
	}
	return true
}

// SlimAccount is a modified version of an Account, where the root is replaced
// with a byte slice. This format can be used to represent full-consensus format
// or slim format which replaces the empty root and code hash as nil byte slice.
type SlimAccount struct {
	Nonce    uint64
	Balance  *big.Int
	Root     []byte // Nil if root equals to types.EmptyRootHash
	CodeHash []byte // Nil if hash equals to types.EmptyCodeHash
}

// SlimAccountRLP encodes the state account in 'slim RLP' format.
func SlimAccountRLP(account StateAccount) []byte {
	slim := SlimAccount{
		Nonce:   account.Nonce,
		Balance: account.Balance,
	}
	if account.Root != EmptyRootHash {
		slim.Root = account.Root[:]
	}
	if !bytes.Equal(account.CodeHash, EmptyCodeHash[:]) {
		slim.CodeHash = account.CodeHash
	}
	data, err := rlp.EncodeToBytes(slim)
	if err != nil {
		panic(err)
	}
	return data
}

// FullAccount decodes the data on the 'slim RLP' format and return
// the consensus format account.
func FullAccount(data []byte) (*StateAccount, error) {
	var slim SlimAccount
	if err := rlp.DecodeBytes(data, &slim); err != nil {
		return nil, err
	}
	var account StateAccount
	account.Nonce, account.Balance = slim.Nonce, slim.Balance

	// Interpret the storage root and code hash in slim format.
	if len(slim.Root) == 0 {
		account.Root = EmptyRootHash
	} else {
		account.Root = common.BytesToHash(slim.Root)
	}
	if len(slim.CodeHash) == 0 {
		account.CodeHash = EmptyCodeHash[:]
	} else {
		account.CodeHash = slim.CodeHash
	}
	return &account, nil
}

// FullAccountRLP converts data on the 'slim RLP' format into the full RLP-format.
func FullAccountRLP(data []byte) ([]byte, error) {
	account, err := FullAccount(data)
	if err != nil {
		return nil, err
	}
	return rlp.EncodeToBytes(account)
}
