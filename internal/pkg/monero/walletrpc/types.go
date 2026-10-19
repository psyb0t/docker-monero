package walletrpc

// RestoreFromSeedRequest is restore_deterministic_wallet's input.
type RestoreFromSeedRequest struct {
	Filename      string
	Password      string
	Seed          string
	SeedOffset    string
	RestoreHeight uint64
	Language      string
}

// GenerateFromKeysRequest is generate_from_keys' input.
type GenerateFromKeysRequest struct {
	Filename      string
	Password      string
	Address       string
	ViewKey       string
	SpendKey      string
	RestoreHeight uint64
	Language      string
}

// SubaddressBalance is per_subaddress_info.
type SubaddressBalance struct {
	AccountIndex      uint32 `json:"account_index"`
	AddressIndex      uint32 `json:"address_index"`
	Address           string `json:"address"`
	Balance           uint64 `json:"balance"`
	UnlockedBalance   uint64 `json:"unlocked_balance"`
	Label             string `json:"label"`
	NumUnspentOutputs uint64 `json:"num_unspent_outputs"`
	BlocksToUnlock    uint64 `json:"blocks_to_unlock"`
}

// Balance is get_balance's result.
type Balance struct {
	Balance         uint64              `json:"balance"`
	UnlockedBalance uint64              `json:"unlocked_balance"`
	PerSubaddress   []SubaddressBalance `json:"per_subaddress"`
	BlocksToUnlock  uint64              `json:"blocks_to_unlock"`
}

// AddressInfo is one entry of get_address' addresses.
type AddressInfo struct {
	Address      string `json:"address"`
	Label        string `json:"label"`
	AddressIndex uint32 `json:"address_index"`
	Used         bool   `json:"used"`
}

// Addresses is get_address' result.
type Addresses struct {
	Address   string        `json:"address"`
	Addresses []AddressInfo `json:"addresses"`
}

// CreatedAddress is create_address' result.
type CreatedAddress struct {
	Address      string `json:"address"`
	AddressIndex uint32 `json:"address_index"`
}

// TransferFilter selects get_transfers categories.
type TransferFilter struct {
	In           bool
	Out          bool
	Pending      bool
	Failed       bool
	Pool         bool
	AccountIndex uint32
}

// SubaddressIndex is a {major, minor} account/subaddress pair.
type SubaddressIndex struct {
	Major uint32 `json:"major"`
	Minor uint32 `json:"minor"`
}

// Destination is one transfer output.
type Destination struct {
	Amount  uint64 `json:"amount"`
	Address string `json:"address"`
}

// TransferEntry is transfer_entry.
type TransferEntry struct {
	TxID            string          `json:"txid"`
	PaymentID       string          `json:"payment_id"`
	Height          uint64          `json:"height"`
	Timestamp       uint64          `json:"timestamp"`
	Amount          uint64          `json:"amount"`
	Fee             uint64          `json:"fee"`
	Note            string          `json:"note"`
	Destinations    []Destination   `json:"destinations"`
	Type            string          `json:"type"`
	UnlockTime      uint64          `json:"unlock_time"`
	Locked          bool            `json:"locked"`
	SubaddrIndex    SubaddressIndex `json:"subaddr_index"`
	Address         string          `json:"address"`
	DoubleSpendSeen bool            `json:"double_spend_seen"`
	Confirmations   uint64          `json:"confirmations"`
}

// Transfers is get_transfers' result, one list per category.
type Transfers struct {
	In      []TransferEntry `json:"in"`
	Out     []TransferEntry `json:"out"`
	Pending []TransferEntry `json:"pending"`
	Failed  []TransferEntry `json:"failed"`
	Pool    []TransferEntry `json:"pool"`
}

// TransferRequest is transfer's input.
type TransferRequest struct {
	Destinations   []Destination
	AccountIndex   uint32
	SubaddrIndices []uint32
	Priority       uint32
	DoNotRelay     bool
}

// TransferResult is transfer's result.
type TransferResult struct {
	TxHash string `json:"tx_hash"`
	TxKey  string `json:"tx_key"`
	Amount uint64 `json:"amount"`
	Fee    uint64 `json:"fee"`
	Weight uint64 `json:"weight"`
}
